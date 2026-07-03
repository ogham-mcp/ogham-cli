package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/native/filters"
	"github.com/ogham-mcp/ogham-cli/internal/native/outbox"
	"github.com/spf13/cobra"
)

// drainDeadline bounds how long runNativeSessionStart spends draining
// queued PostToolUse records before falling through to the actual
// session-context build. Mirrors the council perf-seat 30s figure.
const drainDeadline = 30 * time.Second

// defaultPostToolMatcher scopes PostToolUse to write-class tools so the
// gateway post-tool hook fires only on calls that produce content worth
// capturing (Write / Edit / Bash). Pre-v0.8 the matcher was "" which fires
// on every tool call -- read-class tools (Read, Grep, Glob) produce noise
// the gateway's smart filter then discards anyway. See #10.
const defaultPostToolMatcher = "Write|Edit|Bash"

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
	return oghamHookCommandFor(oghamBinaryPath(), verb)
}

// oghamHookCommandFor is the testable form of oghamHookCommand: callers
// can pass an explicit binary path instead of resolving from
// os.Executable(). The split lets buildOghamHookSet stay pure (no
// filesystem reads) so the matcher/wired-or-skipped logic is unit-testable.
func oghamHookCommandFor(binPath, verb string) string {
	return fmt.Sprintf("%s hooks run %s", binPath, verb)
}

// buildOghamHookSet returns the map of Claude Code hook event names to
// their hook entries. v0.9 (#278) wires PostToolUse unconditionally:
// the native post-tool path (Classify -> MaskSecrets -> outbox.Write)
// works without a gateway api_key, so v0.8's apiKey gate is gone.
// Users with active gateway setups can still opt back into the
// synchronous gateway path with `ogham hooks run post-tool --gateway`.
//
// Pure helper: no filesystem access, no config loading. apiKey is
// accepted for ABI compatibility with v0.7/v0.8 callers and unit-test
// fixtures but no longer affects the output.
//
// PostToolUse uses defaultPostToolMatcher rather than "" so the hook
// only fires on write-class tools (Write / Edit / Bash).
//
// #11: PreCompact -> inscribe is NOT in the default scaffold from v0.8
// onwards. The legacy native inscribe writes a metadata-only stub on
// every compact event (session_id / cwd / timestamp only -- no
// transcript content), which dilutes recall at scale. Users keep their
// existing entries until they `hooks uninstall` then `hooks install`.
// The new explicit `ogham inscribe` verb is the preferred commit
// primitive for pre-distilled content (whether from a transcript
// reader, a skill, or a future plugin -- see the superpowers-memory
// bridge spec §4.3 for the signal-gated + staged + distilled pattern
// the verb is designed to compose with).
func buildOghamHookSet(apiKey, binPath string) map[string]map[string]any {
	_ = apiKey // parameter kept for ABI compat; see doc comment.
	return map[string]map[string]any{
		"SessionStart": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "session-start")}},
		},
		"PostCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "recall")}},
		},
		"PostToolUse": {
			"matcher": defaultPostToolMatcher,
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "post-tool")}},
		},
	}
}

// postToolNoticeMarkerPath returns the cache-dir marker that tracks
// whether the "post-tool fired without gateway key" notice has already
// been emitted on this machine. Empty string when UserCacheDir errors --
// callers treat that as "always emit" so the user still gets diagnostic
// output even on systems without a usable cache dir.
func postToolNoticeMarkerPath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "ogham", "post-tool-unconfigured-notice")
}

// noticePostToolUnconfiguredOnce emits a one-time stderr diagnostic when
// the post-tool hook fires without a gateway api_key configured. Uses
// markerPath as a stash file so subsequent invocations stay silent.
// Returns true when it actually wrote a notice, false when the marker
// already existed (idempotent re-entry).
//
// Defense-in-depth for #10: settings.json may pre-date the v0.8
// install-time skip; without this fallback, every tool call would spawn a
// subprocess that exits non-zero and Claude Code would log a hook error
// on every turn.
func noticePostToolUnconfiguredOnce(markerPath string, w io.Writer) bool {
	if markerPath != "" {
		if _, err := os.Stat(markerPath); err == nil {
			return false // already notified on this machine
		}
		_ = os.MkdirAll(filepath.Dir(markerPath), 0700)
		_ = os.WriteFile(markerPath, []byte("noticed\n"), 0600)
	}
	fmt.Fprintln(w, "ogham: post-tool ran with --gateway but no gateway api_key configured -- skipping (exit 0).")
	fmt.Fprintln(w, "  To use the v0.9 native path: drop the --gateway flag (or omit it in your settings.json hook entry).")
	fmt.Fprintln(w, "  To keep the gateway path: run `ogham auth login --api-key KEY`.")
	if markerPath != "" {
		fmt.Fprintf(w, "  This notice will not repeat (marker: %s).\n", markerPath)
	}
	return true
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

Routing (v0.9):
  - Native (Supabase / Postgres direct) is the default for all four
    events. session-start, recall, and inscribe run synchronously
    against the local backend. post-tool classifies + secret-masks
    the event and queues to a SIGKILL-safe directory outbox; the
    queued records ship to the store on next session-start.
  - Gateway is the legacy synchronous path. --gateway forces it for
    any event, useful only for installs that still have a working
    gateway api_key in config.toml.

DEPRECATED (v0.8, #11): the 'inscribe' event runner stays for users
with pre-v0.8 hook entries in their settings.json, but PreCompact ->
inscribe is no longer wired by ` + "`hooks install`" + ` -- the native
implementation writes a metadata-only stub on every compact event,
which dilutes recall at scale. Use the explicit ` + "`ogham inscribe`" + ` verb
instead and let the caller (orchestrator / skill / scribe / plugin)
decide what to commit.

See issue #6 for the rationale behind the native routing and #11 for
the inscribe verb reshape.`,
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
			// v0.9 (#278): native path is now the default. Classify
			// the event with the embedded shared-data ruleset, secret-
			// mask the content, then queue to the SIGKILL-safe outbox.
			// The next session-start drains the queue into the store.
			// --gateway forces the legacy synchronous path for users
			// with active gateway setups.
			if useNative {
				return runNativePostTool(ctx, nativeCfg, input, profile)
			}
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
	// Drain any PostToolUse records queued by hook fires that happened
	// since the last SessionStart. Best-effort -- a failed drain (e.g.
	// transient DB outage) logs but does not block the session-start
	// context that follows.
	if err := drainOutbox(ctx, cfg, profile); err != nil {
		fmt.Fprintf(os.Stderr, "ogham: outbox drain warning: %v\n", err)
	}

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

// runNativePostTool is the v0.9 default PostToolUse path. Classifies
// the event against the embedded shared-data ruleset, secret-masks
// any content, builds a minimal memory string, and queues to the
// SIGKILL-safe outbox. The actual store write happens at next
// session-start when the drainer runs. Returns nil on skip-classified
// events (Read, Glob, etc.) so the hook always exits 0.
func runNativePostTool(ctx context.Context, _ *native.Config, input map[string]any, profile string) error {
	toolName := getField(input, "tool_name", "")
	if toolName == "" {
		return nil
	}

	verdict := filters.Classify(toolName)
	if !verdict.ShouldCapture() {
		return nil
	}

	toolInput, _ := input["tool_input"].(map[string]any)
	cwd := getField(input, "cwd", "")
	sessionID := getField(input, "session_id", "")
	toolResponse := readToolResponse(input)

	content, target := buildPostToolContent(toolName, toolInput, toolResponse)
	if content == "" {
		return nil
	}
	content = filters.MaskSecrets(content)

	if filters.DefaultDeduper.IsDuplicate(sessionID, toolName, target) {
		return nil
	}

	tags := []string{"type:action", "tool:" + toolName}
	if sessionID != "" {
		tags = append(tags, "session:"+sessionID)
	}

	dir, err := outbox.DefaultDir()
	if err != nil {
		return fmt.Errorf("post-tool: resolve outbox dir: %w", err)
	}
	box, err := outbox.New(dir)
	if err != nil {
		return fmt.Errorf("post-tool: open outbox: %w", err)
	}
	rec := &outbox.Record{
		Content:   content,
		Profile:   profile,
		Source:    "hook:post-tool",
		Tags:      tags,
		SessionID: sessionID,
		ToolName:  toolName,
		Cwd:       cwd,
	}
	if err := box.Write(rec); err != nil {
		return fmt.Errorf("post-tool: queue: %w", err)
	}
	_ = ctx
	return nil
}

// drainOutbox is called from runNativeSessionStart to flush queued
// PostToolUse records into the store via native.Store. Bounded by
// drainDeadline and DefaultDrainBatch to keep SessionStart snappy
// even after a long-idle gap.
func drainOutbox(ctx context.Context, cfg *native.Config, profile string) error {
	dir, err := outbox.DefaultDir()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return nil // nothing queued
	}
	box, err := outbox.New(dir)
	if err != nil {
		return err
	}

	dctx, cancel := context.WithTimeout(ctx, drainDeadline)
	defer cancel()

	stats, err := box.Drain(dctx, func(c context.Context, rec *outbox.Record) error {
		recProfile := rec.Profile
		if recProfile == "" {
			recProfile = profile
		}
		_, sErr := native.Store(c, cfg, rec.Content, native.StoreOptions{
			Tags:    rec.Tags,
			Source:  rec.Source,
			Profile: recProfile,
		})
		return sErr
	})
	if stats.Processed+stats.Failed+stats.Orphaned+stats.Malformed > 0 {
		fmt.Fprintf(os.Stderr,
			"ogham: drained outbox -- processed=%d failed=%d orphaned=%d malformed=%d remaining=%d\n",
			stats.Processed, stats.Failed, stats.Orphaned, stats.Malformed, stats.Remaining)
	}
	return err
}

// readToolResponse pulls the tool-output text out of the hook input,
// trying the various field names Claude Code has used over time.
// Mirrors the equivalent dispatch in src/ogham/hooks.py post_tool.
// Truncated to 2000 chars to match the Python truncation cap.
func readToolResponse(input map[string]any) string {
	for _, k := range []string{"tool_response", "response", "tool_output", "output"} {
		if v, ok := input[k]; ok {
			if s, ok := v.(string); ok {
				return truncateResponse(s, 2000)
			}
		}
	}
	return ""
}

func truncateResponse(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// buildPostToolContent produces a short, human-readable memory string
// + a dedup target for one PostToolUse event. The shape mirrors what
// Python's _extract_memory_content emits today, but the v0.9 port is
// deliberately minimal -- richer extraction (diff summarisation,
// gh-action classification, etc.) lands incrementally in v0.10+.
func buildPostToolContent(toolName string, toolInput map[string]any, toolResponse string) (content, target string) {
	switch toolName {
	case "Bash":
		cmd := getField(toolInput, "command", "")
		if cmd == "" {
			return "", ""
		}
		content = "Bash: " + cmd
		if toolResponse != "" {
			content += "\n" + toolResponse
		}
		target = cmd
	case "Edit":
		path := getField(toolInput, "file_path", "")
		if path == "" {
			return "", ""
		}
		content = "Edit: " + path
		target = path
	case "Write":
		path := getField(toolInput, "file_path", "")
		if path == "" {
			return "", ""
		}
		content = "Write: " + path
		target = path
	default:
		return "", ""
	}
	return content, target
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
	cfg, err := config.Load(config.DefaultPath())
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
	// Defense-in-depth: when post-tool fires without a gateway api_key
	// configured, exit 0 with a one-time stderr notice rather than
	// returning a non-zero per-call error. v0.9 makes native the
	// default, so this path is only reached when the user explicitly
	// passed --gateway. The notice now points them at dropping the
	// flag rather than wiring a key.
	cfg, err := config.Load(config.DefaultPath())
	if err != nil || cfg == nil || cfg.APIKey == "" {
		noticePostToolUnconfiguredOnce(postToolNoticeMarkerPath(), os.Stderr)
		return nil
	}
	client := gateway.New(cfg.GatewayURL, cfg.APIKey, "ogham-cli/hooks")
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

	// v0.9 (#278): PostToolUse runs natively (Classify -> MaskSecrets ->
	// outbox). No gateway api_key required. The v0.8 conditional-skip
	// path was retired -- every install gets the full hook set.
	binPath := oghamBinaryPath()
	oghamHooks := buildOghamHookSet("", binPath)

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
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	fmt.Printf("Claude Code hooks installed to %s\n", path)
	fmt.Printf("  Binary: %s\n", binPath)
	fmt.Printf("  Events: SessionStart, PostToolUse (matcher: %s), PostCompact (recall)\n", defaultPostToolMatcher)
	fmt.Println("  PostToolUse runs natively against your local backend -- no gateway api_key required.")
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
	if err := os.WriteFile(path, data, 0600); err != nil {
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
