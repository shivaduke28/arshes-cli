package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	mcpserver "github.com/shivaduke28/arshes-cli/internal/mcp"
	"github.com/shivaduke28/arshes-cli/internal/websocket"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server (stdio) with WebSocket bridge to iPhone",
	Long: `Start an MCP server that communicates via stdio and bridges to the iPhone
via WebSocket. This allows AI agents like Claude Code to compile shaders
on the connected iPhone.

The WebSocket server runs in the background, waiting for an iPhone connection.
MCP tools are exposed via stdio for the AI agent to use.`,
	RunE: runMcp,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMcp(cmd *cobra.Command, args []string) error {
	// Log to stderr so it doesn't interfere with MCP stdio protocol
	logger := log.New(os.Stderr, "[arshes-mcp] ", log.LstdFlags)

	// Create WebSocket server
	wsServer := websocket.NewServer(port)

	wsServer.OnConnect(func(remoteAddr string) {
		logger.Printf("iPhone connected: %s", remoteAddr)
	})

	wsServer.OnDisconnect(func(remoteAddr string) {
		logger.Printf("iPhone disconnected: %s", remoteAddr)
	})

	// Resolve local address
	localIP, err := websocket.GetLocalIP()
	if err != nil {
		localIP = "localhost"
	}
	wsAddr := fmt.Sprintf("%s:%d", localIP, port)

	// Start WebSocket server in background
	go func() {
		logger.Printf("WebSocket server listening on %s", wsAddr)
		if err := wsServer.Start(); err != nil {
			logger.Printf("WebSocket server error: %v", err)
		}
	}()

	// Create and start MCP server
	mcpSrv := mcpserver.NewServer(wsServer, wsAddr)
	mcpSrv.SetupHandlers()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Println("Shutting down...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*1e9)
		defer shutdownCancel()
		if err := wsServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("WebSocket shutdown error: %v", err)
		}
	}()

	logger.Println("MCP server started (stdio)")

	if err := mcpSrv.Listen(ctx); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}

	return nil
}
