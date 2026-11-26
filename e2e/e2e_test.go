package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shivaduke28/arshes-cli/internal/protocol"
	ws "github.com/shivaduke28/arshes-cli/internal/websocket"
)

func TestE2EScenario(t *testing.T) {
	tmpDir := t.TempDir()
	shaderFile := filepath.Join(tmpDir, "test.slang")
	if err := os.WriteFile(shaderFile, []byte("// initial"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// 1. Start server
	server, port := startServer(t, "")

	connected := make(chan bool, 1)
	server.OnConnect(func(remoteAddr string) {
		connected <- true
	})

	syncReceived := make(chan string, 1)
	server.OnSendShader(func(code string) {
		os.WriteFile(shaderFile, []byte(code), 0644)
		syncReceived <- code
	})

	specReceived := make(chan json.RawMessage, 1)
	server.OnSyncShaderSpec(func(spec json.RawMessage) {
		specReceived <- spec
	})

	// 2. App connects and performs the hello handshake
	app := connectAndHello(t, port, "")
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for connection")
	}

	// 3. App sends syncShaderSpec and sendShader notifications
	app.notify("syncShaderSpec", map[string]any{"uniforms": []string{"time"}})
	select {
	case spec := <-specReceived:
		if string(spec) != `{"uniforms":["time"]}` {
			t.Errorf("unexpected spec: %s", spec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for syncShaderSpec")
	}

	syncCode := "// synced from client"
	app.notify("sendShader", map[string]string{"code": syncCode})
	select {
	case <-syncReceived:
		content, _ := os.ReadFile(shaderFile)
		if string(content) != syncCode {
			t.Errorf("file content mismatch: expected %q, got %q", syncCode, string(content))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for sendShader")
	}

	// 4. Server sends compileShader; the app answers with the same id
	updatedCode := "// updated from file"
	done := callAsync(server, protocol.MethodCompileShader, protocol.CompileShaderParams{Code: updatedCode})

	req := app.read()
	if req.Method != "compileShader" || req.ID == nil {
		t.Fatalf("expected compileShader request, got %+v", req)
	}
	var params protocol.CompileShaderParams
	json.Unmarshal(req.Params, &params)
	if params.Code != updatedCode {
		t.Errorf("code mismatch: expected %q, got %q", updatedCode, params.Code)
	}
	app.respond(req.ID, map[string]any{"success": true, "durationMs": 120})

	res := waitCall(t, done)
	if res.err != nil {
		t.Fatalf("call failed: %v", res.err)
	}
	var result protocol.CompileShaderResult
	json.Unmarshal(res.result, &result)
	if !result.Success || result.DurationMs != 120 {
		t.Errorf("unexpected result: %+v", result)
	}

	// 5. App disconnects
	app.conn.Close()
	waitFor(t, func() bool { return server.ConnectionCount() == 0 })
}

func TestHelloResponse(t *testing.T) {
	_, port := startServer(t, "")
	app := connectApp(t, port)
	app.request("hello-1", "hello", map[string]any{"protocolVersion": 2, "token": ""})
	msg := app.read()
	if msg.JSONRPC != "2.0" || string(msg.ID) != `"hello-1"` || msg.Error != nil {
		t.Fatalf("unexpected hello response: %+v", msg)
	}
	var result protocol.HelloResult
	json.Unmarshal(msg.Result, &result)
	if result.ProtocolVersion != 2 {
		t.Errorf("expected protocolVersion 2, got %d", result.ProtocolVersion)
	}
}

func TestHelloFailures(t *testing.T) {
	tests := []struct {
		name     string
		send     func(a *fakeApp)
		wantID   string
		wantCode int
	}{
		{
			name: "wrong token",
			send: func(a *fakeApp) {
				a.request(1, "hello", map[string]any{"protocolVersion": 2, "token": "wrong-secret"})
			},
			wantID:   "1",
			wantCode: protocol.CodeUnauthorized,
		},
		{
			name: "unsupported version",
			send: func(a *fakeApp) {
				a.request(1, "hello", map[string]any{"protocolVersion": 1, "token": "test-secret"})
			},
			wantID:   "1",
			wantCode: protocol.CodeUnsupportedVersion,
		},
		{
			name: "missing protocolVersion",
			send: func(a *fakeApp) {
				a.request(1, "hello", map[string]any{"token": "test-secret"})
			},
			wantID:   "1",
			wantCode: protocol.CodeInvalidParams,
		},
		{
			name:     "not JSON",
			send:     func(a *fakeApp) { a.sendRaw("{not json") },
			wantID:   "null",
			wantCode: protocol.CodeParseError,
		},
		{
			name: "v1 hello",
			send: func(a *fakeApp) {
				a.sendRaw(`{"type":"hello","payload":{"protocolVersion":1,"token":"test-secret"}}`)
			},
			wantID:   "null",
			wantCode: protocol.CodeInvalidRequest,
		},
		{
			name:     "notification before hello",
			send:     func(a *fakeApp) { a.notify("sendShader", map[string]string{"code": "x"}) },
			wantID:   "null",
			wantCode: protocol.CodeInvalidRequest,
		},
		{
			name: "other request before hello",
			send: func(a *fakeApp) {
				a.request(7, "compileShader", map[string]string{"code": "x"})
			},
			wantID:   "7",
			wantCode: protocol.CodeInvalidRequest,
		},
	}

	server, port := startServer(t, "test-secret")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := connectApp(t, port)
			tt.send(app)
			msg := app.read()
			if string(msg.ID) != tt.wantID {
				t.Errorf("expected id %s, got %s", tt.wantID, msg.ID)
			}
			if msg.Error == nil || msg.Error.Code != tt.wantCode {
				t.Fatalf("expected error %d, got %+v", tt.wantCode, msg)
			}
			// The server closes the connection after a failed hello.
			app.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := app.conn.ReadMessage(); err == nil {
				t.Error("expected the connection to be closed")
			}
			if server.ConnectionCount() != 0 {
				t.Errorf("expected 0 connections, got %d", server.ConnectionCount())
			}
		})
	}

	// The correct token still works.
	connectAndHello(t, port, "test-secret")
	waitFor(t, func() bool { return server.ConnectionCount() == 1 })
}

func TestMessagesFromAppAfterHello(t *testing.T) {
	_, port := startServer(t, "")
	app := connectAndHello(t, port, "")

	tests := []struct {
		name     string
		send     func()
		wantID   string
		wantCode int
	}{
		{"unknown method", func() { app.request(10, "fooBar", nil) }, "10", protocol.CodeMethodNotFound},
		{"second hello", func() {
			app.request(11, "hello", map[string]any{"protocolVersion": 2, "token": ""})
		}, "11", protocol.CodeInvalidRequest},
		{"not JSON", func() { app.sendRaw("[1,") }, "null", protocol.CodeParseError},
		{"batch", func() { app.sendRaw(`[{"jsonrpc":"2.0","method":"sendShader","params":{"code":""}}]`) }, "null", protocol.CodeInvalidRequest},
		{"wrong jsonrpc version", func() { app.sendRaw(`{"jsonrpc":"1.0","id":12,"method":"fooBar"}`) }, "12", protocol.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.send()
			msg := app.read()
			if string(msg.ID) != tt.wantID {
				t.Errorf("expected id %s, got %s", tt.wantID, msg.ID)
			}
			if msg.Error == nil || msg.Error.Code != tt.wantCode {
				t.Fatalf("expected error %d, got %+v", tt.wantCode, msg)
			}
		})
	}

	// Unknown notifications and stray responses get no reply.
	app.notify("someFutureNotification", nil)
	app.respond(json.RawMessage("9999"), map[string]any{})
	app.request(13, "fooBar", nil)
	if msg := app.read(); string(msg.ID) != "13" {
		t.Errorf("expected only the reply to id 13, got %+v", msg)
	}
}

func TestCallErrorFromApp(t *testing.T) {
	server, port := startServer(t, "")
	app := connectAndHello(t, port, "")

	done := callAsync(server, protocol.MethodCaptureImage, nil)
	req := app.read()
	if req.Method != "captureImage" || req.Params != nil {
		t.Fatalf("unexpected request: %+v", req)
	}
	app.respondError(req.ID, protocol.CodeTimeout, "no frame within 5 seconds")

	res := waitCall(t, done)
	var rpcErr *protocol.Error
	if !errors.As(res.err, &rpcErr) || rpcErr.Code != protocol.CodeTimeout {
		t.Fatalf("expected timeout error, got %v", res.err)
	}
}

func TestCallResultMissing(t *testing.T) {
	server, port := startServer(t, "")
	app := connectAndHello(t, port, "")

	done := callAsync(server, protocol.MethodSetTouch, protocol.SetTouchParams{X: 0.5, Y: 0.5, Z: 1})
	req := app.read()
	app.sendRaw(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s}`, req.ID))
	if res := waitCall(t, done); res.err != nil {
		t.Fatalf("expected success, got %v", res.err)
	}
}

func TestConcurrentCallsMatchByID(t *testing.T) {
	server, port := startServer(t, "")
	app := connectAndHello(t, port, "")

	doneA := callAsync(server, protocol.MethodGetUniforms, nil)
	reqA := app.read()
	doneB := callAsync(server, protocol.MethodGetDiagnostics, nil)
	reqB := app.read()
	if string(reqA.ID) == string(reqB.ID) {
		t.Fatalf("request ids must differ: %s", reqA.ID)
	}

	// Respond in reverse order.
	app.respond(reqB.ID, map[string]any{"fps": 60, "resolution": []int{1, 2}})
	app.respond(reqA.ID, map[string]any{"values": map[string]any{"a": 1}})

	resA, resB := waitCall(t, doneA), waitCall(t, doneB)
	if string(resA.result) != `{"values":{"a":1}}` {
		t.Errorf("unexpected result A: %s", resA.result)
	}
	if string(resB.result) != `{"fps":60,"resolution":[1,2]}` {
		t.Errorf("unexpected result B: %s", resB.result)
	}
}

func TestCallMultipleClients(t *testing.T) {
	server, port := startServer(t, "")
	app1 := connectAndHello(t, port, "")
	app2 := connectAndHello(t, port, "")
	waitFor(t, func() bool { return server.ConnectionCount() == 2 })

	t.Run("first success wins over an earlier error", func(t *testing.T) {
		done := callAsync(server, protocol.MethodCompileShader, protocol.CompileShaderParams{Code: "x"})
		req1, req2 := app1.read(), app2.read()
		app1.respondError(req1.ID, protocol.CodeBusy, "busy")
		app2.respond(req2.ID, map[string]any{"success": true, "durationMs": 1})
		res := waitCall(t, done)
		if res.err != nil {
			t.Fatalf("expected success, got %v", res.err)
		}
	})

	t.Run("first success is used", func(t *testing.T) {
		done := callAsync(server, protocol.MethodCompileShader, protocol.CompileShaderParams{Code: "x"})
		req1, req2 := app1.read(), app2.read()
		app2.respond(req2.ID, map[string]any{"success": true, "durationMs": 2})
		res := waitCall(t, done)
		if res.err != nil || string(res.result) != `{"durationMs":2,"success":true}` {
			t.Fatalf("unexpected result: %s, %v", res.result, res.err)
		}
		// A late response is dropped.
		app1.respond(req1.ID, map[string]any{"success": true, "durationMs": 3})
	})

	t.Run("all errors returns the first error", func(t *testing.T) {
		done := callAsync(server, protocol.MethodSetAspectRatio, protocol.SetAspectRatioParams{AspectRatio: "1:1"})
		req1, req2 := app1.read(), app2.read()
		app1.respondError(req1.ID, protocol.CodeBusy, "busy")
		time.Sleep(50 * time.Millisecond)
		app2.respondError(req2.ID, protocol.CodeInvalidParams, "bad")
		res := waitCall(t, done)
		var rpcErr *protocol.Error
		if !errors.As(res.err, &rpcErr) || rpcErr.Code != protocol.CodeBusy {
			t.Fatalf("expected busy error, got %v", res.err)
		}
	})

	t.Run("disconnect of one client while the other answers", func(t *testing.T) {
		done := callAsync(server, protocol.MethodGetDiagnostics, nil)
		req1, _ := app1.read(), app2.read()
		app2.conn.Close()
		time.Sleep(50 * time.Millisecond)
		app1.respond(req1.ID, map[string]any{"fps": 30, "resolution": []int{1, 1}})
		if res := waitCall(t, done); res.err != nil {
			t.Fatalf("expected success, got %v", res.err)
		}
	})
}

func TestCallClientDisconnects(t *testing.T) {
	server, port := startServer(t, "")
	app := connectAndHello(t, port, "")

	done := callAsync(server, protocol.MethodCompileShader, protocol.CompileShaderParams{Code: "x"})
	app.read()
	app.conn.Close()

	res := waitCall(t, done)
	if !errors.Is(res.err, ws.ErrConnectionClosed) {
		t.Fatalf("expected ErrConnectionClosed, got %v", res.err)
	}
}

func TestCallTimeoutAndNoClients(t *testing.T) {
	server, port := startServer(t, "")

	if _, err := server.Call(context.Background(), protocol.MethodGetUniforms, nil); !errors.Is(err, ws.ErrNoClients) {
		t.Fatalf("expected ErrNoClients, got %v", err)
	}

	app := connectAndHello(t, port, "")
	waitFor(t, func() bool { return server.ConnectionCount() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := server.Call(ctx, protocol.MethodGetUniforms, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	// The response after the timeout is dropped without breaking the connection.
	req := app.read()
	app.respond(req.ID, map[string]any{"values": map[string]any{}})

	done := callAsync(server, protocol.MethodGetUniforms, nil)
	req = app.read()
	app.respond(req.ID, map[string]any{"values": map[string]any{}})
	if res := waitCall(t, done); res.err != nil {
		t.Fatalf("expected success, got %v", res.err)
	}
}

// Helpers

func startServer(t *testing.T, token string) (*ws.Server, int) {
	t.Helper()
	port := getFreePort(t)
	server := ws.NewServer(port, token)
	go server.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})
	return server, port
}

func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type callOutcome struct {
	result json.RawMessage
	err    error
}

func callAsync(server *ws.Server, method string, params any) <-chan callOutcome {
	done := make(chan callOutcome, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := server.Call(ctx, method, params)
		done <- callOutcome{result, err}
	}()
	return done
}

func waitCall(t *testing.T, done <-chan callOutcome) callOutcome {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for Call")
		return callOutcome{}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeApp plays the Arshes app side of the protocol.
type fakeApp struct {
	t    *testing.T
	conn *websocket.Conn
}

func connectApp(t *testing.T, port int) *fakeApp {
	t.Helper()
	url := fmt.Sprintf("ws://localhost:%d/", port)
	var err error
	for range 20 {
		var conn *websocket.Conn
		conn, _, err = websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return &fakeApp{t: t, conn: conn}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("failed to connect: %v", err)
	return nil
}

func connectAndHello(t *testing.T, port int, token string) *fakeApp {
	t.Helper()
	app := connectApp(t, port)
	app.request(1, "hello", map[string]any{"protocolVersion": protocol.ProtocolVersion, "token": token})
	msg := app.read()
	if msg.Error != nil {
		t.Fatalf("hello failed: %v", msg.Error)
	}
	return app
}

func (a *fakeApp) sendRaw(data string) {
	a.t.Helper()
	if err := a.conn.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
		a.t.Fatalf("failed to send: %v", err)
	}
}

func (a *fakeApp) sendJSON(v any) {
	a.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		a.t.Fatalf("failed to marshal: %v", err)
	}
	a.sendRaw(string(data))
}

func (a *fakeApp) request(id any, method string, params any) {
	a.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	a.sendJSON(msg)
}

func (a *fakeApp) notify(method string, params any) {
	a.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	a.sendJSON(msg)
}

func (a *fakeApp) respond(id json.RawMessage, result any) {
	a.t.Helper()
	a.sendJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (a *fakeApp) respondError(id json.RawMessage, code int, message string) {
	a.t.Helper()
	a.sendJSON(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (a *fakeApp) read() protocol.Message {
	a.t.Helper()
	a.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := a.conn.ReadMessage()
	if err != nil {
		a.t.Fatalf("failed to read message: %v", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		a.t.Fatalf("failed to unmarshal %s: %v", data, err)
	}
	return msg
}
