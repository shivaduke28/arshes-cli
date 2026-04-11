package websocket

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

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
	token        string
	connections  map[*websocket.Conn]string // conn -> remoteAddr
	connWriteMu map[*websocket.Conn]*sync.Mutex
	mu           sync.RWMutex
	handlers     map[string]func(json.RawMessage)
	onConnect    func(remoteAddr string)
	onDisconnect func(remoteAddr string)
	httpServer   *http.Server
	logger       *log.Logger
}

// NewServer creates a new WebSocket server.
// If token is non-empty, clients must provide it via the hello handshake message to connect.
func NewServer(port int, token string) *Server {
	return &Server{
		port:        port,
		token:       token,
		connections: make(map[*websocket.Conn]string),
		connWriteMu: make(map[*websocket.Conn]*sync.Mutex),
		handlers:    make(map[string]func(json.RawMessage)),
		logger:      log.New(os.Stderr, "[ws] ", log.LstdFlags),
	}
}

// Start starts the WebSocket server and blocks until a client connects
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.HandleConnection)

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

// HandleConnection handles a WebSocket upgrade request.
func (s *Server) HandleConnection(w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	s.logger.Printf("Received connection request from %s", remoteAddr)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Printf("Failed to upgrade connection: %v", err)
		return
	}
	s.logger.Printf("WebSocket upgrade successful for %s", remoteAddr)

	// Perform hello handshake
	if !s.performHandshake(conn, remoteAddr) {
		conn.Close()
		return
	}

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

const handshakeTimeout = 10 * time.Second

// performHandshake performs the hello/helloResult handshake with a client.
// Returns true if handshake succeeded, false otherwise.
func (s *Server) performHandshake(conn *websocket.Conn, remoteAddr string) bool {
	// Set deadline for handshake; reset after success
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	// Read the first message, which must be hello
	_, message, err := conn.ReadMessage()
	if err != nil {
		s.logger.Printf("Failed to read hello from %s: %v", remoteAddr, err)
		return false
	}

	var msg struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		s.logger.Printf("Failed to parse hello from %s: %v", remoteAddr, err)
		return false
	}

	if msg.Type != "hello" {
		s.logger.Printf("Expected hello from %s, got %s", remoteAddr, msg.Type)
		return false
	}

	var hello protocol.HelloPayload
	if err := json.Unmarshal(msg.Payload, &hello); err != nil {
		s.logger.Printf("Failed to parse hello payload from %s: %v", remoteAddr, err)
		return false
	}

	// Check protocol version
	if hello.ProtocolVersion != protocol.ProtocolVersion {
		s.logger.Printf("Unsupported protocol version from %s: %d", remoteAddr, hello.ProtocolVersion)
		result := protocol.NewHelloResultMessage("unsupported_version", 0,
			fmt.Sprintf("server supports protocol version %d", protocol.ProtocolVersion))
		s.writeMessage(conn, result)
		return false
	}

	// Check token (if configured)
	if s.token != "" {
		if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.token)) != 1 {
			s.logger.Printf("Rejected connection from %s: invalid token", remoteAddr)
			result := protocol.NewHelloResultMessage("unauthorized", 0, "invalid token")
			s.writeMessage(conn, result)
			return false
		}
	}

	// Handshake success
	result := protocol.NewHelloResultMessage("ok", protocol.ProtocolVersion, "")
	if err := s.writeMessage(conn, result); err != nil {
		s.logger.Printf("Failed to send helloResult to %s: %v", remoteAddr, err)
		return false
	}

	// Clear read deadline for normal message processing
	conn.SetReadDeadline(time.Time{})

	s.logger.Printf("Handshake successful with %s (protocol v%d)", remoteAddr, hello.ProtocolVersion)
	return true
}

// writeMessage marshals and sends a ServerMessage to a connection.
// This is only used during handshake, before the connection is added to the connections map,
// so no write mutex is needed.
func (s *Server) writeMessage(conn *websocket.Conn, msg protocol.ServerMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// SendCompileShader sends a compileShader message to all connected clients
func (s *Server) SendCompileShader(code string, image bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.connections) == 0 {
		return fmt.Errorf("no clients connected")
	}

	msg := protocol.NewCompileShaderMessage(code, image)
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

// OnSendShader registers a handler for sendShader messages
func (s *Server) OnSendShader(handler func(code string)) {
	s.handlers["sendShader"] = func(payload json.RawMessage) {
		var p protocol.SendShaderPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return
		}
		handler(p.Code)
	}
}

// OnSyncShaderSpec registers a handler for syncShaderSpec messages
func (s *Server) OnSyncShaderSpec(handler func(spec json.RawMessage)) {
	s.handlers["syncShaderSpec"] = func(payload json.RawMessage) {
		// Pass the entire payload as raw JSON for internal storage
		handler(payload)
	}
}

// OnCompileResult registers a handler for compileResult messages
func (s *Server) OnCompileResult(handler func(success bool, errorMsg *string, image *string)) {
	s.handlers["compileResult"] = func(payload json.RawMessage) {
		var p protocol.CompileResultPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return
		}
		handler(p.Success, p.ErrorMessage, p.Image)
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
