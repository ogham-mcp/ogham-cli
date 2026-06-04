package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

// oghamGoHookCommandRegex matches hook commands owned by THIS Go binary.
// The verb shape `hooks run <verb>` distinguishes the Go CLI's three-token
// form from the Python ogham-mcp's two-token `hooks <verb>` form, so the
// idempotent install pre-pass and `hooks uninstall` only touch Go-owned
// entries -- a user with both Python and Go ogham binaries installed
// won't have their Python hook lines accidentally stripped (#7).
//
// Matches:
//
//	ogham-cli hooks run session-start           (pre-v0.7.4 broken form)
//	/usr/local/bin/ogham hooks run session-start
//	/Users/foo/.local/bin/ogham hooks run recall
//
// Does NOT match (Python ogham-mcp):
//
//	/path/to/.venv/bin/ogham hooks recall
//	/path/to/.venv/bin/ogham hooks inscribe
var oghamGoHookCommandRegex = regexp.MustCompile(`(?:^|/)(ogham-cli|ogham)\s+hooks\s+run\s+`)

// oghamBinaryPath returns the absolute path of the running ogham binary,
// for use when writing hook commands into settings.json. Resolution order:
//
//  1. os.Executable() -- the binary that's actually running, regardless of
//     $PATH state at execution time. Matches the pattern cmd/plugin.go
//     uses for openclaw/agent-zero emitters.
//  2. exec.LookPath("ogham") -- fallback if os.Executable() can't resolve
//     (rare, generally only on platforms where /proc/self/exe is missing).
//  3. Bare "ogham" -- last resort. Will still trigger #2's $PATH issue
//     for users with no ogham on $PATH, but at least the hooks install
//     succeeds and the user gets a diagnostic when the hook fires.
//
// The returned path is what Claude Code will execute when SessionStart /
// PostToolUse / PreCompact / PostCompact fires.
func oghamBinaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	if p, err := exec.LookPath("ogham"); err == nil && p != "" {
		return p
	}
	return "ogham"
}

// oghamHookCommand formats the command string for a Go-side hook event.
// Mirrors `ogham hooks run <verb>` with the resolved absolute binary path.
func oghamHookCommand(verb string) string {
	return fmt.Sprintf("%s hooks run %s", oghamBinaryPath(), verb)
}

// pruneOghamGoHooks walks settings["hooks"] and strips any inner hook
// command matching oghamGoHookCommandRegex. Returns the number of inner
// hook commands removed. Mutates the passed settings map in place.
//
// Used as the idempotent pre-pass on install (so re-running `hooks
// install` after a broken-binary-name install cleans up before adding the
// fresh entries) and as the core of `hooks uninstall`.
//
// Leaves Python `ogham hooks <verb>` entries untouched -- those are owned
// by ogham-mcp, not by us.
func pruneOghamGoHooks(settings map[string]any) int {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	removed := 0
	for event, eventHooksRaw := range hooks {
		eventHooks, ok := eventHooksRaw.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(eventHooks))
		for _, entry := range eventHooks {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			innerHooks, ok := entryMap["hooks"].([]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			filteredInner := make([]any, 0, len(innerHooks))
			for _, h := range innerHooks {
				hm, ok := h.(map[string]any)
				if !ok {
					filteredInner = append(filteredInner, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if oghamGoHookCommandRegex.MatchString(cmd) {
					removed++
					continue
				}
				filteredInner = append(filteredInner, h)
			}
			if len(filteredInner) == 0 {
				continue // drop empty matcher block entirely
			}
			entryMap["hooks"] = filteredInner
			kept = append(kept, entryMap)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	settings["hooks"] = hooks
	return removed
}

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle hooks for AI coding clients",
	Long:  "Run hooks that inject memory context at session start, capture tool activity, and survive compaction.",
}

var hooksRunCmd = &cobra.Command{
	Use:   "run [event]",
	Short: "Run a hook event (session-start, post-tool, inscribe, recall)",
	Long: `Run a lifecycle hook event.

Routing:
  - Native (Supabase / Postgres direct) is used when the local config
    has a database backend configured. session-start, inscribe, and
    recall all run locally with no gateway required -- this is the
    headless / sandboxed-CI path that Claude Code SessionStart hooks
    need on machines that can't complete an interactive OAuth login.
  - Gateway is used as a fallback (or explicitly via --gateway) and
    is currently the only path for the smart-filtered post-tool
    event. Requires a valid api_key in config.toml.

See issue #6 for the rationale behind the native routing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event := args[0]

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		input := readStdin()
		profile, _ := cmd.Flags().GetString("profile")
		forceGateway, _ := cmd.Flags().GetBool("gateway")

		// Decide routing: native (Supabase / Postgres direct) wins by
		// default; --gateway flips back to the legacy gateway path.
		nativeCfg, nativeReady := loadNativeIfReady(profile)
		useNative := nativeReady && !forceGateway

		switch event {
		case "session-start":
			if useNative {
				return runNativeSessionStart(ctx, nativeCfg, input, profile)
			}
			return runGatewaySessionStart(ctx, input, profile)

		case "recall":
			if useNative {
				return runNativeRecall(ctx, nativeCfg, input, profile)
			}
			return runGatewayRecall(ctx, input, profile)

		case "inscribe":
			if useNative {
				return runNativeInscribe(ctx, nativeCfg, input, profile)
			}
			return runGatewayInscribe(ctx, input, profile)

		case "post-tool":
			// post-tool's smart filtering (classification, duplicate
			// detection, secret masking) lives only in the Python
			// implementation today. Gateway is the only path until
			// a native port lands -- see issue #6 follow-up. Even if
			// the user has a native backend configured, we can't use
			// it here because the native pipeline would just store
			// every tool call without filtering. Pass the explicit
			// "post-tool only" hint to requireGateway so the error
			// message doesn't mislead users into thinking native
			// would work.
			return runGatewayPostTool(ctx, input, profile)

		default:
			return fmt.Errorf("unknown hook event: %s (use session-start, post-tool, inscribe, or recall)", event)
		}
	},
}

// loadNativeIfReady returns the native config when a working backend
// is configured. Returns (cfg, true) when SessionStart / Recall /
// Inscribe can run locally, (nil, false) otherwise.
//
// Honours --profile by overriding cfg.Profile when the flag is set,
// so per-invocation profile selection works the same way as the
// existing native commands (store, search, list).
func loadNativeIfReady(profile string) (*native.Config, bool) {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return nil, false
	}
	if _, err := cfg.ResolveBackend(); err != nil {
		return nil, false
	}
	if profile != "" {
		cfg.Profile = profile
	}
	return cfg, true
}

// ---- Native event runners -------------------------------------------

func runNativeSessionStart(ctx context.Context, cfg *native.Config, input map[string]any, profile string) error {
	cwd := getField(input, "cwd", ".")
	out, err := native.SessionStart(ctx, cfg, cwd, native.HookOptions{Profile: profile})
	if err != nil {
		return err
	}
	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func runNativeRecall(ctx context.Context, cfg *native.Config, input map[string]any, profile string) error {
	cwd := getField(input, "cwd", ".")
	out, err := native.Recall(ctx, cfg, cwd, native.HookOptions{Profile: profile})
	if err != nil {
		return err
	}
	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func runNativeInscribe(ctx context.Context, cfg *native.Config, input map[string]any, profile string) error {
	sessionID := getField(input, "session_id", "unknown")
	cwd := getField(input, "cwd", ".")
	_, err := native.Inscribe(ctx, cfg, sessionID, cwd, native.HookOptions{Profile: profile})
	return err
}

// ---- Gateway event runners (legacy / Pro+ path) ---------------------

// requireGateway builds a gateway client and errors out cleanly if no
// API key is configured. Replaces the silent 401 from issue #6 with a
// surfaced hint. The hint mentions native config as an alternative for
// event types that have a working native path (session-start, recall,
// inscribe). post-tool is gateway-only today -- its smart filtering
// hasn't been ported -- so it gets a different message that doesn't
// dangle a "try native instead" suggestion that wouldn't actually help.
func requireGateway(usage string) (*gateway.Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.APIKey == "" {
		if usage == "post-tool" {
			return nil, fmt.Errorf(
				"hooks post-tool: gateway api_key required (run `ogham auth login`). post-tool's smart filtering (classification, duplicate detection, secret masking) is not yet available on the native path; track issue #6 follow-up for the native port",
			)
		}
		return nil, fmt.Errorf(
			"hooks %s: no gateway api_key configured (run `ogham auth login`) and no native database backend configured (set SUPABASE_URL+SUPABASE_KEY or DATABASE_URL in ~/.ogham/config.env to use the native path)",
			usage,
		)
	}
	return gateway.New(cfg.GatewayURL, cfg.APIKey, "ogham-cli/hooks"), nil
}

func runGatewaySessionStart(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("session-start")
	if err != nil {
		return err
	}
	cwd := getField(input, "cwd", ".")
	hookCtx, err := client.HookSessionStart(ctx, cwd, profile)
	if err != nil {
		return err
	}
	if hookCtx != "" {
		fmt.Print(hookCtx)
	}
	return nil
}

func runGatewayRecall(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("recall")
	if err != nil {
		return err
	}
	cwd := getField(input, "cwd", ".")
	hookCtx, err := client.HookRecall(ctx, cwd, profile)
	if err != nil {
		return err
	}
	if hookCtx != "" {
		fmt.Print(hookCtx)
	}
	return nil
}

func runGatewayInscribe(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("inscribe")
	if err != nil {
		return err
	}
	sessionID := getField(input, "session_id", "unknown")
	cwd := getField(input, "cwd", ".")
	return client.HookInscribe(ctx, sessionID, cwd, profile)
}

func runGatewayPostTool(ctx context.Context, input map[string]any, profile string) error {
	toolName := getField(input, "tool_name", "")
	if toolName == "" {
		return nil // nothing to capture
	}
	client, err := requireGateway("post-tool")
	if err != nil {
		return err
	}
	var toolInput map[string]any
	if ti, ok := input["tool_input"].(map[string]any); ok {
		toolInput = ti
	}
	cwd := getField(input, "cwd", "")
	sessionID := getField(input, "session_id", "")
	return client.HookPostTool(ctx, toolName, toolInput, cwd, sessionID, profile)
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

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ogham hook entries from the client's settings",
	Long: `Strip Go-owned ogham hook entries from Claude Code's
~/.claude/settings.json. Leaves Python ogham-mcp hook entries and any
unrelated hooks alone -- detection is by command verb shape (ogham
hooks run <verb> = Go; ogham hooks <verb> = Python).

Remediation path for users stuck with the broken ` + "`ogham-cli hooks run`" + `
commands written by pre-v0.7.4 installs of this tool (#7). After
running uninstall, re-run ` + "`ogham hooks install`" + ` to land the fixed
config.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := detectClient()
		switch client {
		case "claude-code":
			return uninstallClaudeCodeHooks()
		default:
			fmt.Printf("Uninstall is currently only implemented for Claude Code (detected: %s).\n", client)
			fmt.Println("For Kiro / Cursor / generic clients, remove the entries manually from the host's config.")
			return nil
		}
	},
}

func init() {
	hooksRunCmd.Flags().String("profile", "work", "Memory profile")
	hooksRunCmd.Flags().Bool("gateway", false, "Force gateway path even when native backend is configured")
	hooksCmd.AddCommand(hooksRunCmd)
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
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

// installClaudeCodeHooks writes ogham hook entries to Claude Code's global
// settings.json (~/.claude/settings.json). Idempotent by construction:
// pruneOghamGoHooks strips any Go-owned ogham hook entries first, so
// re-running `hooks install` after a broken-binary-name install (#7) ends
// up with a clean, correct config -- no stale `ogham-cli hooks run ...`
// lines left behind alongside the fresh ones.
//
// Hook commands embed the absolute path of the running binary
// (oghamBinaryPath), so they execute correctly regardless of binary name
// or $PATH state -- the load-bearing fix for #7 findings #1 and #2.
func installClaudeCodeHooks() error {
	settings, _ := readClaudeSettings()
	if settings == nil {
		settings = make(map[string]any)
	}

	// Pre-pass: remove any Go-owned ogham hook entries before we add the
	// fresh ones. Leaves Python `ogham hooks <verb>` entries alone.
	removed := pruneOghamGoHooks(settings)

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
	}

	binPath := oghamBinaryPath()
	oghamHooks := map[string]map[string]any{
		"SessionStart": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommand("session-start")}},
		},
		"PostToolUse": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommand("post-tool")}},
		},
		"PreCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommand("inscribe")}},
		},
		"PostCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommand("recall")}},
		},
	}

	for event, hookEntry := range oghamHooks {
		existing, _ := hooks[event].([]any)
		existing = append(existing, hookEntry)
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
	fmt.Printf("  Binary: %s\n", binPath)
	fmt.Println("  Events: SessionStart, PostToolUse, PreCompact (inscribe), PostCompact (recall)")
	if removed > 0 {
		fmt.Printf("  Cleaned %d stale ogham hook entr%s from previous install.\n",
			removed, plural(removed, "y", "ies"))
	}
	return nil
}

// uninstallClaudeCodeHooks strips Go-owned ogham hook entries from Claude
// Code's settings.json. Leaves Python ogham-mcp entries and unrelated
// hooks untouched. The remediation path for users stuck with the broken
// `ogham-cli hooks run ...` commands from pre-v0.7.4 installs (#7).
func uninstallClaudeCodeHooks() error {
	settings, err := readClaudeSettings()
	if err != nil || settings == nil {
		fmt.Println("No Claude Code settings.json found -- nothing to uninstall.")
		return nil
	}

	removed := pruneOghamGoHooks(settings)
	if removed == 0 {
		fmt.Println("No ogham hook entries found in settings.json -- nothing to remove.")
		return nil
	}

	home, _ := os.UserHomeDir()
	path := home + "/.claude/settings.json"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	fmt.Printf("Removed %d ogham hook entr%s from %s\n",
		removed, plural(removed, "y", "ies"), path)
	return nil
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
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

// printKiroInstructions outputs Kiro Hook UI setup steps. Uses the
// resolved absolute binary path so the printed instructions stay correct
// regardless of binary name or $PATH state (#7).
func printKiroInstructions() {
	sessionStartCmd := oghamHookCommand("session-start")
	postToolCmd := oghamHookCommand("post-tool")
	fmt.Println("\nKiro hooks -- manual setup via Hook UI:")
	fmt.Println("")
	fmt.Println("  1. Open Command Palette (Cmd+Shift+P / Ctrl+Shift+P)")
	fmt.Println("  2. Type 'Kiro: Open Kiro Hook UI'")
	fmt.Println("  3. Create these hooks:")
	fmt.Println("")
	fmt.Println("  Hook 1: Session Start")
	fmt.Println("    Event: User prompt submit")
	fmt.Println("    Action: Run Command")
	fmt.Printf("    Command: %s\n", sessionStartCmd)
	fmt.Println("")
	fmt.Println("  Hook 2: Post Tool")
	fmt.Println("    Event: Post tool invocation")
	fmt.Println("    Action: Run Command")
	fmt.Printf("    Command: %s\n", postToolCmd)
}
