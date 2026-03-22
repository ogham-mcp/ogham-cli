package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/spf13/cobra"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle hooks for AI coding clients",
	Long:  "Run hooks that inject memory context at session start, capture tool activity, and survive compaction.",
}

var hooksRunCmd = &cobra.Command{
	Use:   "run [event]",
	Short: "Run a hook event (session-start, post-tool, inscribe, recall)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event := args[0]

		cfg, err := config.Load("")
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		client := gateway.New(cfg.GatewayURL, cfg.APIKey, "ogham-cli/hooks")

		// Read stdin for hook input
		input := readStdin()

		profile, _ := cmd.Flags().GetString("profile")

		switch event {
		case "session-start":
			cwd := getField(input, "cwd", ".")
			context, err := client.HookSessionStart(cwd, profile)
			if err != nil {
				return err
			}
			if context != "" {
				fmt.Print(context)
			}

		case "post-tool":
			toolName := getField(input, "tool_name", "")
			if toolName == "" {
				return nil // nothing to capture
			}
			var toolInput map[string]any
			if ti, ok := input["tool_input"].(map[string]any); ok {
				toolInput = ti
			}
			cwd := getField(input, "cwd", "")
			sessionID := getField(input, "session_id", "")
			return client.HookPostTool(toolName, toolInput, cwd, sessionID, profile)

		case "inscribe":
			sessionID := getField(input, "session_id", "unknown")
			cwd := getField(input, "cwd", ".")
			return client.HookInscribe(sessionID, cwd, profile)

		case "recall":
			cwd := getField(input, "cwd", ".")
			context, err := client.HookRecall(cwd, profile)
			if err != nil {
				return err
			}
			if context != "" {
				fmt.Print(context)
			}

		default:
			return fmt.Errorf("unknown hook event: %s (use session-start, post-tool, inscribe, or recall)", event)
		}

		return nil
	},
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Detect AI client and install hooks configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := detectClient()
		fmt.Printf("Detected client: %s\n", client)

		switch client {
		case "claude-code":
			return installClaudeCodeHooks()
		case "kiro":
			printKiroInstructions()
		default:
			fmt.Printf("%s doesn't support hooks natively.\n", client)
			fmt.Println("Use CLAUDE.md instructions or the Python CLI (ogham hooks install).")
		}
		return nil
	},
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show installed hooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := detectClient()
		fmt.Printf("Client: %s\n", client)

		if client == "claude-code" {
			settings, err := readClaudeSettings()
			if err != nil {
				fmt.Println("No hooks installed (settings.json not found)")
				return nil
			}
			hooks, ok := settings["hooks"].(map[string]any)
			if !ok || len(hooks) == 0 {
				fmt.Println("No hooks installed")
				return nil
			}
			fmt.Println("Installed hooks:")
			for event := range hooks {
				fmt.Printf("  %s\n", event)
			}
		}
		return nil
	},
}

func init() {
	hooksRunCmd.Flags().String("profile", "work", "Memory profile")
	hooksCmd.AddCommand(hooksRunCmd)
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksStatusCmd)
	rootCmd.AddCommand(hooksCmd)
}

// readStdin reads JSON from stdin if available.
func readStdin() map[string]any {
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return nil // interactive terminal, no piped input
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// getField extracts a string field from the input map.
func getField(input map[string]any, key, fallback string) string {
	if input == nil {
		return fallback
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return fallback
}

// detectClient checks which AI coding client is installed.
func detectClient() string {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(home + "/.claude/settings.json"); err == nil {
		return "claude-code"
	}
	if _, err := os.Stat(home + "/.kiro"); err == nil {
		return "kiro"
	}
	if _, err := os.Stat(home + "/.cursor"); err == nil {
		return "cursor"
	}
	return "generic"
}

// installClaudeCodeHooks writes ogham-cli hooks to Claude Code settings.json.
func installClaudeCodeHooks() error {
	settings, _ := readClaudeSettings()
	if settings == nil {
		settings = make(map[string]any)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
	}

	oghamHooks := map[string]map[string]any{
		"SessionStart": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": "ogham-cli hooks run session-start"}},
		},
		"PostToolUse": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": "ogham-cli hooks run post-tool"}},
		},
		"PreCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": "ogham-cli hooks run inscribe"}},
		},
		"PostCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": "ogham-cli hooks run recall"}},
		},
	}

	for event, hookEntry := range oghamHooks {
		existing, _ := hooks[event].([]any)
		// Check if already installed
		found := false
		for _, e := range existing {
			if m, ok := e.(map[string]any); ok {
				if fmt.Sprint(m["hooks"]) == fmt.Sprint(hookEntry["hooks"]) {
					found = true
					break
				}
			}
		}
		if !found {
			existing = append(existing, hookEntry)
		}
		hooks[event] = existing
	}

	settings["hooks"] = hooks

	home, _ := os.UserHomeDir()
	path := home + "/.claude/settings.json"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	fmt.Printf("Claude Code hooks installed to %s\n", path)
	fmt.Println("  SessionStart, PostToolUse, PreCompact (inscribe), PostCompact (recall)")
	return nil
}

// readClaudeSettings reads ~/.claude/settings.json.
func readClaudeSettings() (map[string]any, error) {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(home + "/.claude/settings.json")
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// printKiroInstructions outputs Kiro Hook UI setup steps.
func printKiroInstructions() {
	fmt.Println("\nKiro hooks -- manual setup via Hook UI:")
	fmt.Println("")
	fmt.Println("  1. Open Command Palette (Cmd+Shift+P / Ctrl+Shift+P)")
	fmt.Println("  2. Type 'Kiro: Open Kiro Hook UI'")
	fmt.Println("  3. Create these hooks:")
	fmt.Println("")
	fmt.Println("  Hook 1: Session Start")
	fmt.Println("    Event: User prompt submit")
	fmt.Println("    Action: Run Command")
	fmt.Println("    Command: ogham-cli hooks run session-start")
	fmt.Println("")
	fmt.Println("  Hook 2: Post Tool")
	fmt.Println("    Event: Post tool invocation")
	fmt.Println("    Action: Run Command")
	fmt.Println("    Command: ogham-cli hooks run post-tool")
}
