package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	ws "github.com/shivaduke28/arshes-cli/internal/websocket"
)

const compileTimeout = 30 * time.Second

// Server wraps a WebSocket server and exposes MCP tools via stdio.
type Server struct {
	ws          *ws.Server
	mcpServer   *server.MCPServer
	stdioServer *server.StdioServer

	// compileResult channel for waiting on compile results
	compileCh chan compileResult
	mu        sync.Mutex

	// lastSyncedShader stores the latest shader code received from iPhone
	lastSyncedShader string
	// lastSyncedSpec stores the latest shader spec received from iPhone
	lastSyncedSpec json.RawMessage
	syncMu         sync.RWMutex

	// WebSocket server address info
	wsAddr string
}

type compileResult struct {
	Success bool
	Error   *string
	Image   *string
}

// NewServer creates a new MCP server that bridges to the given WebSocket server.
func NewServer(wsServer *ws.Server, wsAddr string) *Server {
	s := &Server{
		ws:        wsServer,
		compileCh: make(chan compileResult, 1),
		wsAddr:    wsAddr,
	}

	mcpServer := server.NewMCPServer(
		"arshes",
		"0.1.0",
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"compile_shader",
			mcp.WithDescription("Send shader code to the connected iPhone for compilation. Returns the compile result. Specify either 'code' or 'file' (file path to read shader from). If both are given, 'file' takes precedence. If 'image' is specified, the rendered image is saved to that path instead of being returned inline."),
			mcp.WithString("code", mcp.Description("Slang shader source code")),
			mcp.WithString("file", mcp.Description("Path to a .slang file to compile")),
			mcp.WithString("image", mcp.Description("Path to save the rendered image (JPEG). If omitted, image is returned inline as base64.")),
		),
		s.handleCompileShader,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_status",
			mcp.WithDescription("Get the connection status and WebSocket server address of the iPhone client."),
		),
		s.handleGetStatus,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_shader",
			mcp.WithDescription("Get the current shader code from the connected iPhone. Returns the last synced shader code."),
		),
		s.handleGetShader,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_shader_spec",
			mcp.WithDescription("Get the Slang shader API specification. Returns available built-in uniforms, parameter attributes, and shader entry point signature."),
		),
		s.handleGetShaderSpec,
	)

	s.mcpServer = mcpServer
	s.stdioServer = server.NewStdioServer(mcpServer)

	// Register WebSocket callbacks
	s.ws.OnCompileResult(func(success bool, errorMsg *string, image *string) {
		s.mu.Lock()
		defer s.mu.Unlock()

		select {
		case s.compileCh <- compileResult{Success: success, Error: errorMsg, Image: image}:
		default:
			// Drop if no one is waiting
		}
	})

	s.ws.OnSyncShader(func(code string) {
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		s.lastSyncedShader = code
	})

	s.ws.OnSyncShaderSpec(func(spec json.RawMessage) {
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		s.lastSyncedSpec = spec
	})

	return s
}

// Listen starts the MCP stdio server.
func (s *Server) Listen(ctx context.Context) error {
	return s.stdioServer.Listen(ctx, os.Stdin, os.Stdout)
}

func (s *Server) handleCompileShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	code, _ := args["code"].(string)
	filePath, _ := args["file"].(string)
	imagePath, _ := args["image"].(string)

	// Resolve code from file if specified
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to read file: %v", err)), nil
		}
		code = string(data)
	}

	if code == "" {
		return mcp.NewToolResultError("either 'code' or 'file' argument is required"), nil
	}

	if !s.ws.IsConnected() {
		return mcp.NewToolResultError("no iPhone client connected"), nil
	}

	// Drain any stale compile result
	s.mu.Lock()
	select {
	case <-s.compileCh:
	default:
	}
	s.mu.Unlock()

	// Send shader to iPhone
	if err := s.ws.SendUpdateShader(code); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to send shader: %v", err)), nil
	}

	// Wait for compile result
	select {
	case result := <-s.compileCh:
		if result.Success {
			if result.Image != nil {
				// Save image to file if path specified
				if imagePath != "" {
					if err := saveBase64Image(*result.Image, imagePath); err != nil {
						return mcp.NewToolResultError(fmt.Sprintf("compilation successful but failed to save image: %v", err)), nil
					}
					return mcp.NewToolResultText(fmt.Sprintf("compilation successful, image saved to %s", imagePath)), nil
				}
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent("compilation successful"),
						mcp.NewImageContent(*result.Image, "image/jpeg"),
					},
				}, nil
			}
			return mcp.NewToolResultText("compilation successful"), nil
		}
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = *result.Error
		}
		return mcp.NewToolResultText(fmt.Sprintf("compilation failed: %s", errMsg)), nil
	case <-time.After(compileTimeout):
		return mcp.NewToolResultError("compile timeout: no response from iPhone within 30 seconds"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func saveBase64Image(b64 string, path string) error {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("failed to decode base64: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func (s *Server) handleGetShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.syncMu.RLock()
	code := s.lastSyncedShader
	s.syncMu.RUnlock()

	if code == "" {
		return mcp.NewToolResultText("no shader has been synced from iPhone yet"), nil
	}
	return mcp.NewToolResultText(code), nil
}

func (s *Server) handleGetShaderSpec(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.syncMu.RLock()
	spec := s.lastSyncedSpec
	s.syncMu.RUnlock()

	if spec == nil {
		return mcp.NewToolResultText("no shader spec received from client yet"), nil
	}
	return mcp.NewToolResultText(string(spec)), nil
}

func (s *Server) handleGetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	count := s.ws.ConnectionCount()
	if count > 0 {
		return mcp.NewToolResultText(fmt.Sprintf("connected (%d client(s), WebSocket: ws://%s)", count, s.wsAddr)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("No clients connected. Ask the user to connect their iPhone to ws://%s", s.wsAddr)), nil
}
