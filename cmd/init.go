package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Set up Ogham CLI -- authenticate and configure MCP clients",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "Setting up Ogham CLI...")
		if err := authLoginCmd.RunE(cmd, args); err != nil {
			return err
		}

		if _, err := exec.LookPath("claude"); err == nil {
			fmt.Fprintln(os.Stderr, "\nClaude Code detected. Registering Ogham MCP server...")
			register := exec.Command("claude", "mcp", "add", "ogham", "--", "ogham")
			register.Stderr = os.Stderr
			if err := register.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ Auto-registration failed: %v\n", err)
				fmt.Fprintln(os.Stderr, "  You can add manually: claude mcp add ogham -- ogham")
			} else {
				fmt.Fprintln(os.Stderr, "✓ Registered with Claude Code")
			}
		}

		fmt.Fprintln(os.Stderr, "\nFor other MCP clients, add this to your config:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Cursor / Windsurf (.cursor/mcp.json):")
		fmt.Fprintln(os.Stderr, `    {"mcpServers": {"ogham": {"command": "ogham"}}}`)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "✓ Setup complete")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&apiKeyFlag, "api-key", "", "API key (or enter interactively)")
	rootCmd.AddCommand(initCmd)
}
