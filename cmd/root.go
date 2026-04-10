package cmd

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var port int
var secret string

func getVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

const minSecretLength = 8

// getSecret returns the secret from the flag or ARSHES_SECRET env var.
func getSecret() string {
	if secret != "" {
		return secret
	}
	return os.Getenv("ARSHES_SECRET")
}

// warnWeakSecret logs a warning if the secret is set but too short.
func warnWeakSecret(logger interface{ Printf(string, ...any) }) {
	s := getSecret()
	if s != "" && len(s) < minSecretLength {
		logger.Printf("WARNING: secret is shorter than %d characters, consider using a stronger secret", minSecretLength)
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
	rootCmd.PersistentFlags().StringVar(&secret, "secret", "", "Secret token for WebSocket authentication (can also be set via ARSHES_SECRET env var)")
}
