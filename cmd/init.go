package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Set up Ogham CLI -- authenticate and configure MCP clients",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "Setting up Ogham CLI...")

		// Check if already logged in
		cfg, err := config.Load(config.DefaultPath())
		if err == nil && cfg.APIKey != "" && apiKeyFlag == "" {
			fmt.Fprintln(os.Stderr, "✓ Already signed in")
		} else {
			if err := authLoginCmd.RunE(cmd, args); err != nil {
				return err
			}
		}

		// Auto-register with Claude Code if available
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

		fmt.Fprintln(os.Stderr, "  Claude Code (if not auto-detected):")
		fmt.Fprintln(os.Stderr, "    claude mcp add ogham -- ogham")

		fmt.Fprintln(os.Stderr, "  Cursor (.cursor/mcp.json):")
		fmt.Fprintln(os.Stderr, `    {"mcpServers": {"ogham": {"command": "ogham", "args": ["serve"]}}}`)
		fmt.Fprintln(os.Stderr, "")

		fmt.Fprintln(os.Stderr, "  VS Code (.vscode/mcp.json):")
		fmt.Fprintln(os.Stderr, `    {"mcpServers": {"ogham": {"command": "ogham", "args": ["serve"]}}}`)
		fmt.Fprintln(os.Stderr, "")

		fmt.Fprintln(os.Stderr, "  Windsurf (~/.windsurf/mcp.json):")
		fmt.Fprintln(os.Stderr, `    {"mcpServers": {"ogham": {"command": "ogham", "args": ["serve"]}}}`)
		fmt.Fprintln(os.Stderr, "")

		fmt.Fprintln(os.Stderr, "  Kiro (~/.kiro/settings/mcp.json):")
		fmt.Fprintln(os.Stderr, `    {"mcpServers": {"ogham": {"command": "ogham", "args": ["serve"]}}}`)
		fmt.Fprintln(os.Stderr, "")

		fmt.Fprintln(os.Stderr, "  Codex:")
		fmt.Fprintln(os.Stderr, "    codex mcp add ogham -- ogham serve")
		fmt.Fprintln(os.Stderr, "")

		fmt.Fprintln(os.Stderr, "✓ Setup complete")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&apiKeyFlag, "api-key", "", "API key (or enter interactively)")
	rootCmd.AddCommand(initCmd)
}
