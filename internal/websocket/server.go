package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/shivaduke28/arshes-cli/internal/protocol"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local network usage
	},
}

// Server represents a WebSocket server that supports multiple connections
type Server struct {
	port         int
	connections  map[*websocket.Conn]string // conn -> remoteAddr
	connWriteMu map[*websocket.Conn]*sync.Mutex
	mu           sync.RWMutex
	handlers     map[string]func(json.RawMessage)
	onConnect    func(remoteAddr string)
	onDisconnect func(remoteAddr string)
	httpServer   *http.Server
	logger       *log.Logger
}

// NewServer creates a new WebSocket server
func NewServer(port int) *Server {
	return &Server{
		port:        port,
		connections: make(map[*websocket.Conn]string),
		connWriteMu: make(map[*websocket.Conn]*sync.Mutex),
		handlers:    make(map[string]func(json.RawMessage)),
		logger:      log.New(os.Stderr, "[ws] ", log.LstdFlags),
	}
}

// Start starts the WebSocket server and blocks until a client connects
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleConnection)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	return s.httpServer.ListenAndServe()
}

// GetLocalIP returns the local IP address
func GetLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no local IP found")
}

func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	s.logger.Printf("Received connection request from %s", remoteAddr)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Printf("Failed to upgrade connection: %v", err)
		return
	}
	s.logger.Printf("WebSocket upgrade successful for %s", remoteAddr)

	// Add to connections map
	s.mu.Lock()
	s.connections[conn] = remoteAddr
	s.connWriteMu[conn] = &sync.Mutex{}
	count := len(s.connections)
	s.mu.Unlock()

	s.logger.Printf("Active connections: %d", count)

	if s.onConnect != nil {
		s.onConnect(remoteAddr)
	}

	// Start reading messages
	s.readLoop(conn, remoteAddr)

	// Remove from connections map
	s.mu.Lock()
	delete(s.connections, conn)
	delete(s.connWriteMu, conn)
	count = len(s.connections)
	s.mu.Unlock()

	s.logger.Printf("Connection closed: %s (remaining: %d)", remoteAddr, count)

	if s.onDisconnect != nil {
		s.onDisconnect(remoteAddr)
	}
}

// SendUpdateShader sends an updateShader message to all connected clients
func (s *Server) SendUpdateShader(code string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.connections) == 0 {
		return fmt.Errorf("no clients connected")
	}

	msg := protocol.NewUpdateShaderMessage(code)
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	var lastErr error
	successCount := 0
	for conn := range s.connections {
		wmu := s.connWriteMu[conn]
		wmu.Lock()
		err := conn.WriteMessage(websocket.TextMessage, data)
		wmu.Unlock()
		if err != nil {
			lastErr = err
		} else {
			successCount++
		}
	}

	if successCount == 0 && lastErr != nil {
		return lastErr
	}

	return nil
}

// OnSyncShader registers a handler for syncShader messages
func (s *Server) OnSyncShader(handler func(code string)) {
	s.handlers["syncShader"] = func(payload json.RawMessage) {
		var p protocol.SyncShaderPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return
		}
		handler(p.Code)
	}
}

// OnSyncShaderSpec registers a handler for syncShaderSpec messages
func (s *Server) OnSyncShaderSpec(handler func(spec json.RawMessage)) {
	s.handlers["syncShaderSpec"] = func(payload json.RawMessage) {
		var p protocol.SyncShaderSpecPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return
		}
		handler(p.Spec)
	}
}

// OnCompileResult registers a handler for compileResult messages
func (s *Server) OnCompileResult(handler func(success bool, errorMsg *string, image *string)) {
	s.handlers["compileResult"] = func(payload json.RawMessage) {
		var p protocol.CompileResultPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return
		}
		handler(p.Success, p.Error, p.Image)
	}
}

// OnConnect registers a callback for when a client connects
func (s *Server) OnConnect(handler func(remoteAddr string)) {
	s.onConnect = handler
}

// OnDisconnect registers a callback for when a client disconnects
func (s *Server) OnDisconnect(handler func(remoteAddr string)) {
	s.onDisconnect = handler
}

// ConnectionCount returns the number of connected clients
func (s *Server) ConnectionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

// IsConnected returns true if at least one client is connected
func (s *Server) IsConnected() bool {
	return s.ConnectionCount() > 0
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	// Close all WebSocket connections
	s.mu.Lock()
	for conn := range s.connections {
		if wmu, ok := s.connWriteMu[conn]; ok {
			wmu.Lock()
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down"))
			wmu.Unlock()
		}
		conn.Close()
	}
	s.mu.Unlock()

	// Shutdown HTTP server
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

func (s *Server) readLoop(conn *websocket.Conn, remoteAddr string) {
	defer conn.Close()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		if handler, ok := s.handlers[msg.Type]; ok {
			handler(msg.Payload)
		}
	}
}
