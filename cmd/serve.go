package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/shivaduke28/arshes-cli/internal/websocket"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve [file]",
	Short: "Start shader server and watch file for changes",
	Long: `Start a WebSocket server and watch a shader file for changes.
When the file changes, the new shader code is automatically sent to the connected iPhone.
If no file is specified, a new file with timestamp will be created (shader_YYYYMMDDhhmmss.slang).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	var filePath string

	if len(args) == 0 {
		// Generate filename with timestamp
		filePath = fmt.Sprintf("shader_%s.slang", time.Now().Format("20060102150405"))
		// Create the file
		if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
		fmt.Printf("Created new shader file: %s\n", filePath)
	} else {
		filePath = args[0]
		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", filePath)
		}
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Get local IP address
	localIP, err := websocket.GetLocalIP()
	if err != nil {
		fmt.Printf("Warning: Could not determine local IP: %v\n", err)
		localIP = "localhost"
	}

	// Create server
	server := websocket.NewServer(port)

	// Track connection state
	var connected bool
	var connectedMu sync.Mutex

	// Flag to ignore file events triggered by our own writes
	var ignoreNextChange bool
	var ignoreMu sync.Mutex

	// Set up handlers
	server.OnConnect(func(remoteAddr string) {
		connectedMu.Lock()
		connected = true
		connectedMu.Unlock()
		fmt.Printf("\033[32m✓\033[0m Client connected: %s\n", remoteAddr)
	})

	server.OnDisconnect(func(remoteAddr string) {
		connectedMu.Lock()
		connected = server.IsConnected()
		connectedMu.Unlock()
		fmt.Printf("\033[33m!\033[0m Client disconnected: %s\n", remoteAddr)
		if !connected {
			fmt.Println("Waiting for connection...")
		}
	})

	server.OnSyncShader(func(code string) {
		// Save the current shader from iPhone to the file
		ignoreMu.Lock()
		ignoreNextChange = true
		ignoreMu.Unlock()

		if err := os.WriteFile(absPath, []byte(code), 0644); err != nil {
			fmt.Printf("\033[31m✗\033[0m Failed to write shader to file: %v\n", err)
			return
		}
		fmt.Printf("Received and saved current shader from client (%d bytes)\n", len(code))
	})

	server.OnCompileResult(func(success bool, errorMsg *string) {
		if success {
			fmt.Printf("\033[32m✓\033[0m Compiled successfully\n")
		} else {
			errStr := "unknown error"
			if errorMsg != nil {
				errStr = *errorMsg
			}
			fmt.Printf("\033[31m✗\033[0m Compile error:\n%s\n", errStr)
		}
	})

	// Start server in background
	go func() {
		if err := server.Start(); err != nil {
			fmt.Printf("Server error: %v\n", err)
		}
	}()

	fmt.Printf("Server listening on %s:%d\n", localIP, port)
	fmt.Println("Waiting for connection...")
	fmt.Printf("Watching %s for changes\n", filePath)
	fmt.Println("Press Ctrl+C to stop.")

	// Set up file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	// Watch the directory (to handle editors that rename files)
	dir := filepath.Dir(absPath)
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	// Debounce timer
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Check if the event is for our file
			eventPath, _ := filepath.Abs(event.Name)
			if eventPath != absPath {
				continue
			}

			// Handle write and create events (create covers atomic writes/renames)
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Skip if this change was triggered by our own write
			ignoreMu.Lock()
			if ignoreNextChange {
				ignoreNextChange = false
				ignoreMu.Unlock()
				continue
			}
			ignoreMu.Unlock()

			// Debounce: wait a bit before sending to avoid multiple events
			debounceMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
				connectedMu.Lock()
				isConnected := connected
				connectedMu.Unlock()

				if !isConnected {
					return
				}

				content, err := os.ReadFile(absPath)
				if err != nil {
					fmt.Printf("\033[31m✗\033[0m Failed to read file: %v\n", err)
					return
				}

				if err := server.SendUpdateShader(string(content)); err != nil {
					fmt.Printf("\033[31m✗\033[0m Failed to send update: %v\n", err)
					return
				}

				fmt.Printf("Sent shader update (%d bytes)\n", len(content))
			})
			debounceMu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Printf("Watcher error: %v\n", err)

		case <-sigChan:
			fmt.Println("\nShutting down...")
			return nil
		}
	}
}
