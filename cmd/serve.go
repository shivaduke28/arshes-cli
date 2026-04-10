package cmd

import (
	"context"
	"fmt"
	"log"
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

var enableLog bool

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
	serveCmd.Flags().BoolVar(&enableLog, "log", false, "Enable logging to arshes.log (file will be overwritten on server start)")
}

func runServe(cmd *cobra.Command, args []string) error {
	warnWeakSecret(log.Default())

	// Set up log file if enabled
	var logFile *os.File
	if enableLog {
		var err error
		logFile, err = os.Create("arshes.log")
		if err != nil {
			return fmt.Errorf("failed to create log file: %w", err)
		}
		defer logFile.Close()
	}

	// Helper to log to both stdout and file (without ANSI colors in file, with timestamp)
	logPrint := func(format string, colorCode string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		if colorCode != "" {
			fmt.Printf("%s%s\033[0m", colorCode, msg)
		} else {
			fmt.Print(msg)
		}
		if logFile != nil {
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			logFile.WriteString(fmt.Sprintf("[%s] %s", timestamp, msg))
			logFile.Sync()
		}
	}

	var filePath string

	if len(args) == 0 {
		// Generate filename with timestamp
		filePath = fmt.Sprintf("shader_%s.slang", time.Now().Format("20060102150405"))
		// Create the file
		if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
		logPrint("Created new shader file: %s\n", "", filePath)
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
		logPrint("Warning: Could not determine local IP: %v\n", "\033[33m", err)
		localIP = "localhost"
	}

	// Create server
	server := websocket.NewServer(port, getSecret())

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
		logPrint("✓ Client connected: %s\n", "\033[32m", remoteAddr)
	})

	server.OnDisconnect(func(remoteAddr string) {
		connectedMu.Lock()
		connected = server.IsConnected()
		connectedMu.Unlock()
		logPrint("! Client disconnected: %s\n", "\033[33m", remoteAddr)
		if !connected {
			logPrint("Waiting for connection...\n", "")
		}
	})

	server.OnSyncShader(func(code string) {
		// Save the current shader from iPhone to the file
		ignoreMu.Lock()
		ignoreNextChange = true
		ignoreMu.Unlock()

		if err := os.WriteFile(absPath, []byte(code), 0644); err != nil {
			logPrint("✗ Failed to write shader to file: %v\n", "\033[31m", err)
			return
		}
		logPrint("Received and saved current shader from client (%d bytes)\n", "", len(code))
	})

	server.OnCompileResult(func(success bool, errorMsg *string, image *string) {
		if success {
			logPrint("✓ Compiled successfully\n", "\033[32m")
		} else {
			errStr := "unknown error"
			if errorMsg != nil {
				errStr = *errorMsg
			}
			logPrint("✗ Compile error:\n%s\n", "\033[31m", errStr)
		}
	})

	// Start server in background
	go func() {
		if err := server.Start(); err != nil {
			logPrint("Server error: %v\n", "\033[31m", err)
		}
	}()

	logPrint("Server listening on %s:%d\n", "", localIP, port)
	logPrint("Waiting for connection...\n", "")
	logPrint("Watching %s for changes\n", "", filePath)
	logPrint("Press Ctrl+C to stop.\n", "")

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
					logPrint("✗ Failed to read file: %v\n", "\033[31m", err)
					return
				}

				if err := server.SendUpdateShader(string(content)); err != nil {
					logPrint("✗ Failed to send update: %v\n", "\033[31m", err)
					return
				}

				logPrint("Sent shader update (%d bytes)\n", "", len(content))
			})
			debounceMu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logPrint("Watcher error: %v\n", "\033[33m", err)

		case <-sigChan:
			logPrint("\nShutting down...\n", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				logPrint("Shutdown error: %v\n", "\033[33m", err)
			}
			return nil
		}
	}
}
