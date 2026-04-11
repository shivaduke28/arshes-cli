package cmd

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var port int
var token string

func getVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

const minTokenLength = 8

// getToken returns the token from the flag or ARSHES_TOKEN env var.
func getToken() string {
	if token != "" {
		return token
	}
	return os.Getenv("ARSHES_TOKEN")
}

// warnWeakToken logs a warning if the token is set but too short.
func warnWeakToken(logger interface{ Printf(string, ...any) }) {
	t := getToken()
	if t != "" && len(t) < minTokenLength {
		logger.Printf("WARNING: token is shorter than %d characters, consider using a stronger token", minTokenLength)
	}
}

var rootCmd = &cobra.Command{
	Use:     "arshes",
	Version: getVersion(),
	Short:   "CLI tool for Arshes shader development",
	Long: `Arshes CLI allows you to edit shaders on your computer
and send them to your iPhone for real-time preview.

Usage:
  arshes serve <file> [--port 10080]`,
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().IntVarP(&port, "port", "p", 10080, "Server port")
	rootCmd.PersistentFlags().StringVar(&token, "token", "", "Authentication token for client connections (can also be set via ARSHES_TOKEN env var)")
}
