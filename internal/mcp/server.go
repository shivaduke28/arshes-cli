package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/shivaduke28/arshes-cli/internal/protocol"
	ws "github.com/shivaduke28/arshes-cli/internal/websocket"
)

// requestTimeout bounds each tool call that talks to the device,
// including the wait for a device to connect.
const requestTimeout = 30 * time.Second

// Server wraps a WebSocket server and exposes MCP tools.
type Server struct {
	ws         *ws.Server
	mcpServer  *server.MCPServer
	httpServer *server.StreamableHTTPServer

	// lastSyncedShader stores the latest shader code received from the device
	lastSyncedShader string
	// lastSyncedSpec stores the latest shader spec received from the device (raw syncShaderSpec params)
	lastSyncedSpec json.RawMessage
	syncMu         sync.RWMutex

	// WebSocket server address info
	wsAddr string

	// timeout overrides requestTimeout (for tests)
	timeout time.Duration
}

// NewServer creates a new MCP server that bridges to the given WebSocket server.
func NewServer(wsServer *ws.Server, wsAddr string) *Server {
	s := &Server{
		ws:      wsServer,
		wsAddr:  wsAddr,
		timeout: requestTimeout,
	}

	mcpServer := server.NewMCPServer(
		"arshes",
		"0.1.0",
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"compile_shader",
			mcp.WithDescription("Send shader code to the connected device for compilation. On success the device starts rendering with the shader. Returns whether the compilation succeeded, the compiler error message on failure, and the time it took on the device. Does not return an image; use capture_image for that. Specify either 'code' or 'file' (file path to read shader from). If both are given, 'file' takes precedence."),
			mcp.WithString("code", mcp.Description("Slang shader source code")),
			mcp.WithString("file", mcp.Description("Path to a .slang file to compile")),
		),
		s.handleCompileShader,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"capture_image",
			mcp.WithDescription("Capture the first frame the device finishes rendering after the request, as a JPEG (the longer side is at most 512 px). If 'image' is specified, the image is saved to that path instead of being returned inline."),
			mcp.WithString("image", mcp.Description("Path to save the captured image (JPEG). If omitted, the image is returned inline.")),
		),
		s.handleCaptureImage,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_uniforms",
			mcp.WithDescription("Get the current values of the custom uniforms (declared with range, rgb, rgba or toggle attributes) of the shader being rendered. Returns a JSON object keyed by uniform name. Values are numbers for float/int, true/false for bool, and arrays of numbers for vectors."),
		),
		s.handleGetUniforms,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"set_uniforms",
			mcp.WithDescription("Set the values of custom uniforms of the shader being rendered. Values are numbers for float/int, true/false for bool, and arrays of numbers for vectors. If any value has the wrong shape, is out of the attribute's range, or names an unknown uniform, the device rejects the request and changes nothing."),
			mcp.WithObject("values",
				mcp.Required(),
				mcp.Description(`Object keyed by uniform name, e.g. {"brightness": 0.8, "tintColor": [1, 0, 0], "useEffect": true}`),
			),
		),
		s.handleSetUniforms,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"set_aspect_ratio",
			mcp.WithDescription("Change the aspect ratio of the camera view on the device. Behaves the same as choosing the aspect ratio in the app UI."),
			mcp.WithString("aspect_ratio",
				mcp.Required(),
				mcp.Enum(protocol.AspectRatios...),
				mcp.Description("Aspect ratio"),
			),
		),
		s.handleSetAspectRatio,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_diagnostics",
			mcp.WithDescription("Get rendering statistics from the device as JSON: fps (average over the last second), gpuTimeMs (average GPU time per frame over the last second; absent on devices that cannot measure it), and resolution ([width, height] of the render resolution in pixels, same as the built-in resolution uniform)."),
		),
		s.handleGetDiagnostics,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"set_touch",
			mcp.WithDescription("Set the value of the built-in touch uniform on the device."),
			mcp.WithNumber("x", mcp.Required(), mcp.Min(0), mcp.Max(1), mcp.Description("touch.x: 0 (left) to 1 (right)")),
			mcp.WithNumber("y", mcp.Required(), mcp.Min(0), mcp.Max(1), mcp.Description("touch.y: 0 (top) to 1 (bottom)")),
			mcp.WithNumber("z", mcp.Required(), mcp.Description("touch.z: 1 while touching, 0 when released")),
		),
		s.handleSetTouch,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_status",
			mcp.WithDescription("Get the connection status and WebSocket server address of the connected Arshes app client."),
		),
		s.handleGetStatus,
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_shader",
			mcp.WithDescription("Get the current shader code from the connected device. Returns the last synced shader code."),
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
	s.httpServer = server.NewStreamableHTTPServer(mcpServer)

	s.ws.OnSendShader(func(code string) {
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

// ListenStdio starts the MCP server using stdio transport.
func (s *Server) ListenStdio(ctx context.Context) error {
	stdioServer := server.NewStdioServer(s.mcpServer)
	return stdioServer.Listen(ctx, os.Stdin, os.Stdout)
}

// Handler returns the MCP Streamable HTTP handler.
func (s *Server) Handler() *server.StreamableHTTPServer {
	return s.httpServer
}

// Shutdown gracefully shuts down the MCP Streamable HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// call sends a request to the device(s), waiting for a connection first if needed.
// On failure it returns a tool error result describing what went wrong.
func (s *Server) call(ctx context.Context, method string, params any) (json.RawMessage, *mcp.CallToolResult) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if !s.ws.IsConnected() && !s.ws.WaitForConnection(ctx) {
		return nil, mcp.NewToolResultError(fmt.Sprintf(
			"no device connected within %s. Ask the user to connect the Arshes app to ws://%s", s.timeout, s.wsAddr))
	}

	result, err := s.ws.Call(ctx, method, params)
	if err == nil {
		return result, nil
	}

	var rpcErr *protocol.Error
	switch {
	case errors.As(err, &rpcErr):
		msg := fmt.Sprintf("%s failed on the device: %v", method, rpcErr)
		switch rpcErr.Code {
		case protocol.CodeBusy:
			msg += "\nThe device cannot handle the request right now. Retry after a while."
		case protocol.CodeTimeout:
			msg += "\nThe device did not finish the request in time."
		}
		return nil, mcp.NewToolResultError(msg)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, mcp.NewToolResultError(fmt.Sprintf("%s timed out: no response from the device within %s", method, s.timeout))
	default:
		return nil, mcp.NewToolResultError(fmt.Sprintf("%s failed: %v", method, err))
	}
}

func decodeResult(method string, raw json.RawMessage, v any) *mcp.CallToolResult {
	if err := json.Unmarshal(raw, v); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid %s result from the device: %v", method, err))
	}
	return nil
}

func (s *Server) handleCompileShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	code, _ := args["code"].(string)
	filePath, _ := args["file"].(string)

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

	raw, errResult := s.call(ctx, protocol.MethodCompileShader, protocol.CompileShaderParams{Code: code})
	if errResult != nil {
		return errResult, nil
	}
	var result protocol.CompileShaderResult
	if errResult := decodeResult(protocol.MethodCompileShader, raw, &result); errResult != nil {
		return errResult, nil
	}

	if result.Success {
		return mcp.NewToolResultText(fmt.Sprintf("compilation successful (%s ms)", formatNumber(result.DurationMs))), nil
	}
	errMsg := result.ErrorMessage
	if errMsg == "" {
		errMsg = "unknown error"
	}
	return mcp.NewToolResultText(fmt.Sprintf("compilation failed (%s ms): %s", formatNumber(result.DurationMs), errMsg)), nil
}

func (s *Server) handleCaptureImage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	imagePath, _ := request.GetArguments()["image"].(string)

	raw, errResult := s.call(ctx, protocol.MethodCaptureImage, nil)
	if errResult != nil {
		return errResult, nil
	}
	var result protocol.CaptureImageResult
	if errResult := decodeResult(protocol.MethodCaptureImage, raw, &result); errResult != nil {
		return errResult, nil
	}
	if result.Image == "" {
		return mcp.NewToolResultError("invalid captureImage result from the device: image is empty"), nil
	}

	if imagePath != "" {
		if err := saveBase64Image(result.Image, imagePath); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("captured the image but failed to save it: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("image saved to %s (%dx%d)", imagePath, result.Width, result.Height)), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(fmt.Sprintf("captured image (%dx%d)", result.Width, result.Height)),
			mcp.NewImageContent(result.Image, "image/jpeg"),
		},
	}, nil
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

func (s *Server) handleGetUniforms(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, errResult := s.call(ctx, protocol.MethodGetUniforms, nil)
	if errResult != nil {
		return errResult, nil
	}
	var result protocol.GetUniformsResult
	if errResult := decodeResult(protocol.MethodGetUniforms, raw, &result); errResult != nil {
		return errResult, nil
	}
	if result.Values == nil {
		result.Values = protocol.UniformValues{}
	}
	return jsonText(result.Values)
}

func (s *Server) handleSetUniforms(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	values, ok := request.GetArguments()["values"].(map[string]any)
	if !ok {
		return mcp.NewToolResultError("'values' must be an object keyed by uniform name"), nil
	}
	if len(values) == 0 {
		return mcp.NewToolResultError("'values' must contain at least one uniform"), nil
	}

	params := struct {
		Values map[string]any `json:"values"`
	}{Values: values}
	if _, errResult := s.call(ctx, protocol.MethodSetUniforms, params); errResult != nil {
		return errResult, nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return mcp.NewToolResultText("uniforms updated: " + strings.Join(names, ", ")), nil
}

func (s *Server) handleSetAspectRatio(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ratio, _ := request.GetArguments()["aspect_ratio"].(string)
	if !slices.Contains(protocol.AspectRatios, ratio) {
		return mcp.NewToolResultError(fmt.Sprintf("'aspect_ratio' must be one of %s", strings.Join(protocol.AspectRatios, ", "))), nil
	}
	if _, errResult := s.call(ctx, protocol.MethodSetAspectRatio, protocol.SetAspectRatioParams{AspectRatio: ratio}); errResult != nil {
		return errResult, nil
	}
	return mcp.NewToolResultText("aspect ratio set to " + ratio), nil
}

func (s *Server) handleGetDiagnostics(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, errResult := s.call(ctx, protocol.MethodGetDiagnostics, nil)
	if errResult != nil {
		return errResult, nil
	}
	var result protocol.GetDiagnosticsResult
	if errResult := decodeResult(protocol.MethodGetDiagnostics, raw, &result); errResult != nil {
		return errResult, nil
	}
	// Return the raw result so that fields added in the future are not dropped.
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return jsonText(result)
	}
	return mcp.NewToolResultText(buf.String()), nil
}

func (s *Server) handleSetTouch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	var p protocol.SetTouchParams
	for name, dst := range map[string]*float64{"x": &p.X, "y": &p.Y, "z": &p.Z} {
		v, ok := args[name].(float64)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("'%s' is required and must be a number", name)), nil
		}
		*dst = v
	}
	if _, errResult := s.call(ctx, protocol.MethodSetTouch, p); errResult != nil {
		return errResult, nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("touch set to (%s, %s, %s)", formatNumber(p.X), formatNumber(p.Y), formatNumber(p.Z))), nil
}

func jsonText(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func formatNumber(f float64) string {
	return fmt.Sprintf("%g", f)
}

func (s *Server) handleGetShader(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.syncMu.RLock()
	code := s.lastSyncedShader
	s.syncMu.RUnlock()

	if code == "" {
		return mcp.NewToolResultText("no shader has been synced from the device yet"), nil
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
	return mcp.NewToolResultText(fmt.Sprintf("No clients connected. Ask the user to connect the Arshes app to ws://%s", s.wsAddr)), nil
}
