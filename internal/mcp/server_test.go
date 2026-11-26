package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/shivaduke28/arshes-cli/internal/protocol"
	ws "github.com/shivaduke28/arshes-cli/internal/websocket"
)

// appReply is what the fake app answers to one request.
type appReply struct {
	result any
	err    *protocol.Error
}

// received is one request the fake app received.
type received struct {
	method string
	params json.RawMessage
}

// setup starts a WebSocket server, an MCP server on top of it, and a fake app
// that answers every request with reply(method, params).
func setup(t *testing.T, reply func(method string) appReply) (*Server, <-chan received) {
	t.Helper()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	wsServer := ws.NewServer(port, "")
	go wsServer.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		wsServer.Shutdown(ctx)
	})

	s := NewServer(wsServer, fmt.Sprintf("localhost:%d", port))
	s.timeout = 2 * time.Second

	if reply == nil {
		return s, nil
	}

	var conn *websocket.Conn
	for range 20 {
		conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:%d/", port), nil)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "hello", "params": map[string]any{"protocolVersion": 2, "token": ""}})
	var hello protocol.Message
	if err := conn.ReadJSON(&hello); err != nil || hello.Error != nil {
		t.Fatalf("hello failed: %v %+v", err, hello.Error)
	}

	reqs := make(chan received, 16)
	go func() {
		for {
			var msg protocol.Message
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			reqs <- received{msg.Method, msg.Params}
			r := reply(msg.Method)
			res := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
			if r.err != nil {
				res["error"] = r.err
			} else {
				res["result"] = r.result
			}
			conn.WriteJSON(res)
		}
	}()

	for !wsServer.IsConnected() {
		time.Sleep(10 * time.Millisecond)
	}
	return s, reqs
}

func callTool(t *testing.T, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func nextRequest(t *testing.T, reqs <-chan received) received {
	t.Helper()
	select {
	case r := <-reqs:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("the app received no request")
		return received{}
	}
}

func TestCompileShader(t *testing.T) {
	s, reqs := setup(t, func(string) appReply {
		return appReply{result: map[string]any{"success": false, "durationMs": 85.5, "errorMessage": "line 1: syntax error"}}
	})

	res := callTool(t, s.handleCompileShader, map[string]any{"code": "bad"})
	if res.IsError || textOf(t, res) != "compilation failed (85.5 ms): line 1: syntax error" {
		t.Errorf("unexpected result: %+v", res)
	}
	r := nextRequest(t, reqs)
	if r.method != "compileShader" || string(r.params) != `{"code":"bad"}` {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}
	if len(res.Content) != 1 {
		t.Errorf("compile_shader must not return an image, got %d contents", len(res.Content))
	}

	file := filepath.Join(t.TempDir(), "a.slang")
	os.WriteFile(file, []byte("from file"), 0644)
	callTool(t, s.handleCompileShader, map[string]any{"code": "ignored", "file": file})
	if r := nextRequest(t, reqs); string(r.params) != `{"code":"from file"}` {
		t.Errorf("file should take precedence, got %s", r.params)
	}

	if res := callTool(t, s.handleCompileShader, map[string]any{}); !res.IsError {
		t.Error("expected an error without code or file")
	}
}

func TestCompileShaderSuccess(t *testing.T) {
	s, _ := setup(t, func(string) appReply {
		return appReply{result: map[string]any{"success": true, "durationMs": 120}}
	})
	res := callTool(t, s.handleCompileShader, map[string]any{"code": "ok"})
	if res.IsError || textOf(t, res) != "compilation successful (120 ms)" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCaptureImage(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	b64 := base64.StdEncoding.EncodeToString(jpeg)
	s, reqs := setup(t, func(string) appReply {
		return appReply{result: map[string]any{"image": b64, "width": 512, "height": 288}}
	})

	res := callTool(t, s.handleCaptureImage, nil)
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	img, ok := res.Content[1].(mcp.ImageContent)
	if !ok || img.Data != b64 || img.MIMEType != "image/jpeg" {
		t.Errorf("unexpected image content: %+v", res.Content[1])
	}
	if r := nextRequest(t, reqs); r.method != "captureImage" || r.params != nil {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}

	path := filepath.Join(t.TempDir(), "sub", "out.jpg")
	res = callTool(t, s.handleCaptureImage, map[string]any{"image": path})
	if res.IsError || !strings.Contains(textOf(t, res), "512x288") {
		t.Errorf("unexpected result: %+v", res)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(jpeg) {
		t.Errorf("saved image mismatch: %v", err)
	}
}

func TestDeviceErrors(t *testing.T) {
	s, _ := setup(t, func(method string) appReply {
		switch method {
		case "captureImage":
			return appReply{err: &protocol.Error{Code: protocol.CodeTimeout, Message: "no frame"}}
		case "setUniforms":
			return appReply{err: &protocol.Error{Code: protocol.CodeInvalidParams, Message: "unknown uniform: foo"}}
		default:
			return appReply{err: &protocol.Error{Code: protocol.CodeBusy, Message: "compiling"}}
		}
	})

	tests := []struct {
		name    string
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    map[string]any
		want    []string
	}{
		{"busy", s.handleCompileShader, map[string]any{"code": "x"}, []string{"busy (-32002): compiling", "Retry"}},
		{"timeout", s.handleCaptureImage, nil, []string{"timeout (-32003): no frame"}},
		{"invalid params", s.handleSetUniforms, map[string]any{"values": map[string]any{"foo": 1}}, []string{"invalid params (-32602): unknown uniform: foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tt.handler, tt.args)
			if !res.IsError {
				t.Fatalf("expected a tool error, got %+v", res)
			}
			for _, w := range tt.want {
				if !strings.Contains(textOf(t, res), w) {
					t.Errorf("%q does not contain %q", textOf(t, res), w)
				}
			}
		})
	}
}

func TestUniforms(t *testing.T) {
	s, reqs := setup(t, func(method string) appReply {
		if method == "getUniforms" {
			return appReply{result: map[string]any{"values": map[string]any{"brightness": 0.8, "tint": []float64{1, 0, 0}, "on": true}}}
		}
		return appReply{result: map[string]any{}}
	})

	res := callTool(t, s.handleGetUniforms, nil)
	var values map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &values); err != nil || values["brightness"] != 0.8 || values["on"] != true {
		t.Errorf("unexpected get_uniforms result: %s", textOf(t, res))
	}
	if r := nextRequest(t, reqs); r.method != "getUniforms" || r.params != nil {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}

	res = callTool(t, s.handleSetUniforms, map[string]any{"values": map[string]any{"brightness": 0.5, "tint": []any{0.0, 1.0, 0.0}, "on": false}})
	if res.IsError || textOf(t, res) != "uniforms updated: brightness, on, tint" {
		t.Errorf("unexpected set_uniforms result: %+v", res)
	}
	if r := nextRequest(t, reqs); r.method != "setUniforms" || string(r.params) != `{"values":{"brightness":0.5,"on":false,"tint":[0,1,0]}}` {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}

	for _, args := range []map[string]any{{}, {"values": "x"}, {"values": map[string]any{}}} {
		if res := callTool(t, s.handleSetUniforms, args); !res.IsError {
			t.Errorf("expected an error for %v", args)
		}
	}
}

func TestSetAspectRatio(t *testing.T) {
	s, reqs := setup(t, func(string) appReply { return appReply{result: nil} })

	res := callTool(t, s.handleSetAspectRatio, map[string]any{"aspect_ratio": "2.35:1"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	if r := nextRequest(t, reqs); r.method != "setAspectRatio" || string(r.params) != `{"aspectRatio":"2.35:1"}` {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}

	if res := callTool(t, s.handleSetAspectRatio, map[string]any{"aspect_ratio": "3:2"}); !res.IsError {
		t.Error("expected an error for an unknown aspect ratio")
	}
}

func TestGetDiagnostics(t *testing.T) {
	s, reqs := setup(t, func(string) appReply {
		return appReply{result: map[string]any{"fps": 59.9, "resolution": []int{1170, 2532}}}
	})
	res := callTool(t, s.handleGetDiagnostics, nil)
	var got map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil || got["fps"] != 59.9 {
		t.Errorf("unexpected result: %s", textOf(t, res))
	}
	if _, ok := got["gpuTimeMs"]; ok {
		t.Error("gpuTimeMs should be absent when the device does not send it")
	}
	if r := nextRequest(t, reqs); r.method != "getDiagnostics" {
		t.Errorf("unexpected request: %s", r.method)
	}
}

func TestSetTouch(t *testing.T) {
	s, reqs := setup(t, func(string) appReply { return appReply{result: map[string]any{}} })

	res := callTool(t, s.handleSetTouch, map[string]any{"x": 0.25, "y": 0.75, "z": 1.0})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	if r := nextRequest(t, reqs); r.method != "setTouch" || string(r.params) != `{"x":0.25,"y":0.75,"z":1}` {
		t.Errorf("unexpected request: %s %s", r.method, r.params)
	}

	if res := callTool(t, s.handleSetTouch, map[string]any{"x": 0.5, "y": 0.5}); !res.IsError {
		t.Error("expected an error when z is missing")
	}
}

func TestNoDevice(t *testing.T) {
	s, _ := setup(t, nil)
	s.timeout = 100 * time.Millisecond
	res := callTool(t, s.handleGetUniforms, nil)
	if !res.IsError || !strings.Contains(textOf(t, res), "no device connected") {
		t.Errorf("unexpected result: %+v", res)
	}
}
