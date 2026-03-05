package mcp

import (
	"context"
	"fmt"
	"os"
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
	syncMu           sync.RWMutex
}

type compileResult struct {
	Success bool
	Error   *string
}

// NewServer creates a new MCP server that bridges to the given WebSocket server.
func NewServer(wsServer *ws.Server) *Server {
	s := &Server{
		ws:        wsServer,
		compileCh: make(chan compileResult, 1),
	}

	mcpServer := server.NewMCPServer(
		"arshes",
		"0.1.0",
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"compile_shader",
			mcp.WithDescription("Send shader code to the connected iPhone for compilation. Returns the compile result."),
			mcp.WithString("code", mcp.Required(), mcp.Description("Slang shader source code")),
		),
		s.handleCompileShader,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_status",
			mcp.WithDescription("Get the connection status of the iPhone client."),
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

	s.mcpServer = mcpServer
	s.stdioServer = server.NewStdioServer(mcpServer)

	return s
}

// SetupHandlers registers WebSocket callbacks on the WebSocket server.
func (s *Server) SetupHandlers() {
	s.ws.OnCompileResult(func(success bool, errorMsg *string) {
		s.mu.Lock()
		defer s.mu.Unlock()

		select {
		case s.compileCh <- compileResult{Success: success, Error: errorMsg}:
		default:
			// Drop if no one is waiting
		}
	})

	s.ws.OnSyncShader(func(code string) {
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		s.lastSyncedShader = code
	})
}

// Listen starts the MCP stdio server.
func (s *Server) Listen(ctx context.Context) error {
	return s.stdioServer.Listen(ctx, os.Stdin, os.Stdout)
}

func (s *Server) handleCompileShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	code, ok := request.GetArguments()["code"].(string)
	if !ok || code == "" {
		return mcp.NewToolResultError("code argument is required"), nil
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

func (s *Server) handleGetShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.syncMu.RLock()
	code := s.lastSyncedShader
	s.syncMu.RUnlock()

	if code == "" {
		return mcp.NewToolResultText("no shader has been synced from iPhone yet"), nil
	}
	return mcp.NewToolResultText(code), nil
}

func (s *Server) handleGetStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	count := s.ws.ConnectionCount()
	if count > 0 {
		return mcp.NewToolResultText(fmt.Sprintf("connected (%d client(s))", count)), nil
	}
	return mcp.NewToolResultText("no clients connected"), nil
}
