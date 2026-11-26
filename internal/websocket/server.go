package websocket

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shivaduke28/arshes-cli/internal/protocol"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local network usage
	},
}

// ErrNoClients is returned by Call when no Arshes app is connected.
var ErrNoClients = errors.New("no clients connected")

// ErrConnectionClosed is the error of a request whose connection closed before a response arrived.
var ErrConnectionClosed = errors.New("connection closed before the response arrived")

const handshakeTimeout = 10 * time.Second

// callResult is the outcome of one request sent to one client.
type callResult struct {
	result json.RawMessage
	err    error
}

// client is one Arshes app connection that completed the hello handshake.
type client struct {
	conn       *websocket.Conn
	remoteAddr string

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan<- callResult
	closed    bool
}

func (c *client) writeJSON(msg *protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// addPending registers a request id. It returns false if the client is already closed.
func (c *client) addPending(id int64, ch chan<- callResult) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.closed {
		return false
	}
	c.pending[id] = ch
	return true
}

func (c *client) removePending(id int64) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	delete(c.pending, id)
}

// resolve delivers a response to the request with the given id.
// It returns false if no request with the id is pending.
func (c *client) resolve(id int64, res callResult) bool {
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if ok {
		ch <- res // buffered; never blocks
	}
	return ok
}

// close fails all pending requests of the client.
func (c *client) close() {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = map[int64]chan<- callResult{}
	c.closed = true
	c.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- callResult{err: ErrConnectionClosed}
	}
}

// Server is a Remote Editor protocol v2 WebSocket server that supports multiple connections.
type Server struct {
	port           int
	token          string
	clients        map[*client]struct{}
	mu             sync.RWMutex
	nextID         atomic.Int64
	notifyHandlers map[string]func(json.RawMessage)
	onConnect      func(remoteAddr string)
	onDisconnect   func(remoteAddr string)
	connectWaiters []chan struct{}
	httpServer     *http.Server // guarded by mu
	shutdown       bool         // guarded by mu
	logger         *log.Logger
}

// NewServer creates a new WebSocket server.
// If token is non-empty, clients must provide it via the hello request to connect.
func NewServer(port int, token string) *Server {
	return &Server{
		port:           port,
		token:          token,
		clients:        make(map[*client]struct{}),
		notifyHandlers: make(map[string]func(json.RawMessage)),
		logger:         log.New(os.Stderr, "[ws] ", log.LstdFlags),
	}
}

// Start starts the WebSocket server and blocks until it stops.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.HandleConnection)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		return http.ErrServerClosed
	}
	s.httpServer = httpServer
	s.mu.Unlock()

	return httpServer.ListenAndServe()
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

	if !s.performHandshake(conn, remoteAddr) {
		conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "hello failed"),
			time.Now().Add(time.Second))
		conn.Close()
		return
	}

	c := &client{
		conn:       conn,
		remoteAddr: remoteAddr,
		pending:    make(map[int64]chan<- callResult),
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	count := len(s.clients)
	waiters := s.connectWaiters
	s.connectWaiters = nil
	s.mu.Unlock()

	s.logger.Printf("Active connections: %d", count)

	for _, ch := range waiters {
		close(ch)
	}

	if s.onConnect != nil {
		s.onConnect(remoteAddr)
	}

	s.readLoop(c)

	s.mu.Lock()
	delete(s.clients, c)
	count = len(s.clients)
	s.mu.Unlock()
	c.close()

	s.logger.Printf("Connection closed: %s (remaining: %d)", remoteAddr, count)

	if s.onDisconnect != nil {
		s.onDisconnect(remoteAddr)
	}
}

// performHandshake handles the hello request, which must be the first message.
// Returns true if the handshake succeeded. On failure it has already sent an
// error response (when possible), and the caller closes the connection.
// No other goroutine writes to conn during the handshake.
func (s *Server) performHandshake(conn *websocket.Conn, remoteAddr string) bool {
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	_, data, err := conn.ReadMessage()
	if err != nil {
		s.logger.Printf("Failed to read hello from %s: %v", remoteAddr, err)
		return false
	}

	reject := func(id json.RawMessage, code int, message string) bool {
		s.logger.Printf("Rejected hello from %s: %s", remoteAddr, message)
		writeRaw(conn, protocol.NewErrorResponse(id, code, message))
		return false
	}

	msg, perr := parseMessage(data)
	if perr != nil {
		return reject(protocol.NullID, perr.Code, perr.Message)
	}
	if !msg.IsRequest() || msg.Method != protocol.MethodHello {
		return reject(msg.ID, protocol.CodeInvalidRequest, "the first message must be a hello request")
	}

	var hello protocol.HelloParams
	if err := json.Unmarshal(msg.Params, &hello); err != nil || hello.ProtocolVersion == nil {
		return reject(msg.ID, protocol.CodeInvalidParams, "invalid hello params")
	}

	if *hello.ProtocolVersion != protocol.ProtocolVersion {
		return reject(msg.ID, protocol.CodeUnsupportedVersion,
			fmt.Sprintf("server supports protocol version %d", protocol.ProtocolVersion))
	}

	if s.token != "" && subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.token)) != 1 {
		return reject(msg.ID, protocol.CodeUnauthorized, "invalid token")
	}

	res, err := protocol.NewResult(msg.ID, protocol.HelloResult{ProtocolVersion: protocol.ProtocolVersion})
	if err != nil {
		s.logger.Printf("Failed to build hello result: %v", err)
		return false
	}
	if err := writeRaw(conn, res); err != nil {
		s.logger.Printf("Failed to send hello result to %s: %v", remoteAddr, err)
		return false
	}

	conn.SetReadDeadline(time.Time{})
	s.logger.Printf("Handshake successful with %s (protocol v%d)", remoteAddr, protocol.ProtocolVersion)
	return true
}

// writeRaw writes a message to a connection that is not shared yet.
func writeRaw(conn *websocket.Conn, msg *protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// parseMessage decodes one JSON-RPC message.
// It returns a parse error or an invalid request error when the data is not a valid message.
func parseMessage(data []byte) (*protocol.Message, *protocol.Error) {
	if !json.Valid(data) {
		return nil, &protocol.Error{Code: protocol.CodeParseError, Message: "parse error"}
	}
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		// Valid JSON but not an object (e.g. a batch array) or wrong member types.
		return nil, &protocol.Error{Code: protocol.CodeInvalidRequest, Message: "invalid request"}
	}
	if msg.JSONRPC != protocol.JSONRPCVersion {
		return &msg, &protocol.Error{Code: protocol.CodeInvalidRequest, Message: `"jsonrpc" must be "2.0"`}
	}
	return &msg, nil
}

// parseRequestID parses the id of a response to a request sent by Call.
// Call sends integer ids, but a client that stores numbers as floating point
// may echo them back as e.g. 5.0, so any integral number is accepted.
func parseRequestID(raw json.RawMessage) (int64, bool) {
	if id, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return id, true
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

func (s *Server) readLoop(c *client) {
	defer c.conn.Close()

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		s.handleMessage(c, data)
	}
}

func (s *Server) handleMessage(c *client, data []byte) {
	reply := func(msg *protocol.Message) {
		if err := c.writeJSON(msg); err != nil {
			s.logger.Printf("Failed to send to %s: %v", c.remoteAddr, err)
		}
	}

	msg, perr := parseMessage(data)
	if perr != nil {
		var id json.RawMessage
		if msg != nil {
			id = msg.ID
		}
		reply(protocol.NewErrorResponse(id, perr.Code, perr.Message))
		return
	}

	switch {
	case msg.Method == "" && msg.ID != nil:
		// A response. JSON-RPC requires "result" or "error", but a success response
		// without "result" is accepted as a result of null.
		if !msg.IsResponse() {
			s.logger.Printf("Response from %s has neither result nor error; treating it as a null result", c.remoteAddr)
		}
		id, ok := parseRequestID(msg.ID)
		if !ok {
			s.logger.Printf("Ignoring response with unexpected id %s from %s", msg.ID, c.remoteAddr)
			return
		}
		res := callResult{result: msg.Result}
		if msg.Error != nil {
			res = callResult{err: msg.Error}
		}
		// A response to a request that already finished (e.g. another client answered first,
		// or the request timed out) is dropped silently.
		c.resolve(id, res)

	case msg.IsRequest():
		if msg.Method == protocol.MethodHello {
			reply(protocol.NewErrorResponse(msg.ID, protocol.CodeInvalidRequest, "hello already completed"))
			return
		}
		reply(protocol.NewErrorResponse(msg.ID, protocol.CodeMethodNotFound, "method not found: "+msg.Method))

	case msg.IsNotification():
		if handler, ok := s.notifyHandlers[msg.Method]; ok {
			handler(msg.Params)
		} else {
			s.logger.Printf("Ignoring unknown notification %q from %s", msg.Method, c.remoteAddr)
		}

	default:
		reply(protocol.NewErrorResponse(msg.ID, protocol.CodeInvalidRequest, "invalid request"))
	}
}

// Call sends a request to every connected client and waits for the responses.
//
// It returns the result of the first successful response. If every client
// answers with an error (or disconnects), it returns the first error.
// An error returned by the Arshes app is a *protocol.Error.
func (s *Server) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.RLock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	if len(clients) == 0 {
		return nil, ErrNoClients
	}

	results := make(chan callResult, len(clients))
	type sent struct {
		c  *client
		id int64
	}
	var sentTo []sent
	defer func() {
		for _, x := range sentTo {
			x.c.removePending(x.id)
		}
	}()

	var firstErr error
	for _, c := range clients {
		id := s.nextID.Add(1)
		msg, err := protocol.NewRequest(id, method, params)
		if err != nil {
			return nil, err
		}
		if !c.addPending(id, results) {
			if firstErr == nil {
				firstErr = ErrConnectionClosed
			}
			continue
		}
		sentTo = append(sentTo, sent{c, id})
		if err := c.writeJSON(msg); err != nil {
			c.removePending(id)
			sentTo = sentTo[:len(sentTo)-1]
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to send to %s: %w", c.remoteAddr, err)
			}
		}
	}

	for range sentTo {
		select {
		case res := <-results:
			if res.err == nil {
				return res.result, nil
			}
			if firstErr == nil {
				firstErr = res.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, firstErr
}

// OnSendShader registers a handler for sendShader notifications.
func (s *Server) OnSendShader(handler func(code string)) {
	s.notifyHandlers[protocol.MethodSendShader] = func(params json.RawMessage) {
		var p protocol.SendShaderParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.logger.Printf("Invalid sendShader params: %v", err)
			return
		}
		handler(p.Code)
	}
}

// OnSyncShaderSpec registers a handler for syncShaderSpec notifications.
// The params are passed as raw JSON.
func (s *Server) OnSyncShaderSpec(handler func(spec json.RawMessage)) {
	s.notifyHandlers[protocol.MethodSyncShaderSpec] = handler
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
	return len(s.clients)
}

// IsConnected returns true if at least one client is connected
func (s *Server) IsConnected() bool {
	return s.ConnectionCount() > 0
}

// WaitForConnection blocks until a client connects or the context is cancelled.
// Returns true if a client is connected, false on cancellation.
func (s *Server) WaitForConnection(ctx context.Context) bool {
	s.mu.Lock()
	if len(s.clients) > 0 {
		s.mu.Unlock()
		return true
	}
	ch := make(chan struct{})
	s.connectWaiters = append(s.connectWaiters, ch)
	s.mu.Unlock()

	select {
	case <-ch:
		return s.IsConnected()
	case <-ctx.Done():
		s.removeWaiter(ch)
		return false
	}
}

// removeWaiter removes a waiter channel from the connectWaiters slice.
func (s *Server) removeWaiter(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.connectWaiters {
		if w == ch {
			s.connectWaiters = append(s.connectWaiters[:i], s.connectWaiters[i+1:]...)
			return
		}
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	for _, ch := range s.connectWaiters {
		close(ch)
	}
	s.connectWaiters = nil
	for c := range s.clients {
		c.writeMu.Lock()
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down"))
		c.writeMu.Unlock()
		c.conn.Close()
	}
	s.shutdown = true
	httpServer := s.httpServer
	s.mu.Unlock()

	if httpServer != nil {
		return httpServer.Shutdown(ctx)
	}
	return nil
}
