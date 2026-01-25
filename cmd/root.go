package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var port int

var rootCmd = &cobra.Command{
	Use:   "arshes",
	Short: "CLI tool for Arshes shader development",
	Long: `Arshes CLI allows you to edit shaders on your computer
and send them to your iPhone for real-time preview.

Usage:
  arshes serve <file> [--port 8080]`,
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().IntVarP(&port, "port", "p", 8080, "Server port")
}
