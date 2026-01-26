package e2e

import (
	"context"
	"encoding/json"
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
	// Setup: create temp file
	tmpDir := t.TempDir()
	shaderFile := filepath.Join(tmpDir, "test.slang")
	if err := os.WriteFile(shaderFile, []byte("// initial"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// 1. Start server
	port := getFreePort(t)
	server := ws.NewServer(port)

	connected := make(chan bool, 1)
	server.OnConnect(func(remoteAddr string) {
		connected <- true
	})

	syncReceived := make(chan string, 1)
	server.OnSyncShader(func(code string) {
		// Write to file (simulating serve.go behavior)
		os.WriteFile(shaderFile, []byte(code), 0644)
		syncReceived <- code
	})

	go server.Start()
	t.Log("1. Server started")

	// 2. Client connects
	conn := connectWebSocket(t, port)
	select {
	case <-connected:
		t.Log("2. Client connected")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for connection")
	}

	// 3. Client sends syncShader -> file is updated
	syncCode := "// synced from client"
	sendMessage(t, conn, "syncShader", map[string]string{"code": syncCode})

	select {
	case <-syncReceived:
		content, _ := os.ReadFile(shaderFile)
		if string(content) != syncCode {
			t.Errorf("file content mismatch: expected '%s', got '%s'", syncCode, string(content))
		}
		t.Log("3. syncShader received, file updated")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for syncShader")
	}

	// 4. File updated -> sent to client
	updatedCode := "// updated from file"
	os.WriteFile(shaderFile, []byte(updatedCode), 0644)
	content, _ := os.ReadFile(shaderFile)
	server.SendUpdateShader(string(content))

	msg := readMessage(t, conn, 2*time.Second)
	if msg.Type != "updateShader" {
		t.Errorf("expected updateShader, got %s", msg.Type)
	}
	payloadBytes, _ := json.Marshal(msg.Payload)
	var payload protocol.UpdateShaderPayload
	json.Unmarshal(payloadBytes, &payload)
	if payload.Code != updatedCode {
		t.Errorf("payload mismatch: expected '%s', got '%s'", updatedCode, payload.Code)
	}
	t.Log("4. File update sent to client")

	// 5. Client disconnects
	conn.Close()
	time.Sleep(100 * time.Millisecond)
	if server.ConnectionCount() != 0 {
		t.Errorf("expected 0 connections after disconnect, got %d", server.ConnectionCount())
	}
	t.Log("5. Client disconnected")

	// 6. Server shuts down
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Logf("shutdown error (expected): %v", err)
	}
	t.Log("6. Server shut down")
}

// Helper functions

func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func connectWebSocket(t *testing.T, port int) *websocket.Conn {
	t.Helper()
	url := fmt.Sprintf("ws://localhost:%d/", port)

	var conn *websocket.Conn
	var err error
	for range 10 {
		conn, _, err = websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			return conn
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("failed to connect: %v", err)
	return nil
}

func readMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) protocol.ServerMessage {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))

	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}

	var msg protocol.ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	return msg
}

func sendMessage(t *testing.T, conn *websocket.Conn, msgType string, payload any) {
	t.Helper()
	msg := map[string]any{"type": msgType, "payload": payload}
	data, _ := json.Marshal(msg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("failed to send: %v", err)
	}
}
