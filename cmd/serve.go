package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server over stdio",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.ErrOrStderr(), "serve not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
