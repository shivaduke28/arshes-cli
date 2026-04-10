package cmd

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mcpserver "github.com/shivaduke28/arshes-cli/internal/mcp"
	"github.com/shivaduke28/arshes-cli/internal/websocket"
	"github.com/spf13/cobra"
)

var transport string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server with WebSocket bridge to iPhone",
	Long: `Start an MCP server that bridges to the iPhone via WebSocket.
This allows AI agents like Claude Code to compile shaders on the connected iPhone.

Transport modes:
  stdio  - Communicate via stdin/stdout (default, used by Claude Code locally)
  http   - Communicate via Streamable HTTP (for remote deployment)

In http mode, both the MCP endpoint (/mcp) and the WebSocket endpoint (/)
are served on the same port.`,
	RunE: runMcp,
}

func init() {
	mcpCmd.Flags().StringVar(&transport, "transport", "stdio", "MCP transport mode: stdio or http")
	rootCmd.AddCommand(mcpCmd)
}

func runMcp(cmd *cobra.Command, args []string) error {
	logger := log.New(os.Stderr, "[arshes-mcp] ", log.LstdFlags)

	// Create WebSocket server
	wsServer := websocket.NewServer(port, getSecret())

	wsServer.OnConnect(func(remoteAddr string) {
		logger.Printf("iPhone connected: %s", remoteAddr)
	})

	wsServer.OnDisconnect(func(remoteAddr string) {
		logger.Printf("iPhone disconnected: %s", remoteAddr)
	})

	// Resolve local address for display
	localIP, err := websocket.GetLocalIP()
	if err != nil {
		localIP = "localhost"
	}
	wsAddr := fmt.Sprintf("%s:%d", localIP, port)

	// Create MCP server
	mcpSrv := mcpserver.NewServer(wsServer, wsAddr)

	switch transport {
	case "stdio":
		return runMcpStdio(logger, wsServer, mcpSrv, wsAddr)
	case "http":
		return runMcpHTTP(logger, wsServer, mcpSrv, wsAddr)
	default:
		return fmt.Errorf("unknown transport: %s (use 'stdio' or 'http')", transport)
	}
}

func runMcpStdio(logger *log.Logger, wsServer *websocket.Server, mcpSrv *mcpserver.Server, wsAddr string) error {
	// Start WebSocket server in background
	go func() {
		logger.Printf("WebSocket server listening on %s", wsAddr)
		if err := wsServer.Start(); err != nil {
			logger.Printf("WebSocket server error: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Println("Shutting down...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := wsServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("WebSocket shutdown error: %v", err)
		}
	}()

	logger.Println("MCP server started (stdio)")

	if err := mcpSrv.ListenStdio(ctx); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}

	return nil
}

// bearerAuthMiddleware wraps an http.Handler and requires a valid Authorization: Bearer <secret> header.
func bearerAuthMiddleware(secret string, logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if auth == token || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
			logger.Printf("Rejected MCP request from %s: invalid authorization", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func runMcpHTTP(logger *log.Logger, wsServer *websocket.Server, mcpSrv *mcpserver.Server, wsAddr string) error {
	// Create a shared HTTP server with both MCP and WebSocket handlers
	mux := http.NewServeMux()
	secret := getSecret()
	if secret != "" {
		mux.Handle("/mcp", bearerAuthMiddleware(secret, logger, mcpSrv.Handler()))
	} else {
		mux.Handle("/mcp", mcpSrv.Handler())
	}
	mux.HandleFunc("/", wsServer.HandleConnection)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Println("Shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := wsServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("WebSocket shutdown error: %v", err)
		}
		if err := mcpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Printf("MCP shutdown error: %v", err)
		}
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	logger.Printf("Server listening on :%d (MCP: /mcp, WebSocket: /)", port)
	logger.Printf("iPhone WebSocket address: ws://%s", wsAddr)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}
