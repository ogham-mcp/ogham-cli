package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ogham",
	Short: "Ogham MCP client -- persistent shared memory for AI agents",
	Long:  "A lightweight MCP client that connects to the Ogham Cloud gateway.\nNo Python, no database, no embeddings locally.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return serveCmd.RunE(cmd, args)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
