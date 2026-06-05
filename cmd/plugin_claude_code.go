package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/spf13/cobra"
)

// Anthropic Plugins reference (https://code.claude.com/docs/en/plugins-reference)
// blesses `${CLAUDE_PLUGIN_ROOT}` for path resolution inside a plugin
// scaffold. The plugin install dir changes on update so baking
// os.Executable() (the right choice for standalone hooks/settings.json
// installs) goes stale here -- use the env-var indirection instead.
const claudePluginRoot = "${CLAUDE_PLUGIN_ROOT}"

// claudeCodeHookEvents enumerates the four lifecycle hooks the plugin
// scaffold wires. PostToolUse is treated separately because it's gated
// on gateway api_key presence (the #10 install-time skip applies here
// too -- emitting a PostToolUse hook that can never succeed produces the
// same per-call hook-error noise via the plugin path as it does via
// `hooks install`).
type claudeCodeHookEvent struct {
	Event   string // SessionStart / PostToolUse / PreCompact / PostCompact
	Verb    string // session-start / post-tool / inscribe / recall
	Matcher string // "" / "Write|Edit|Bash" / "manual|auto"
}

// claudeCodeHookEvents returns the ordered hook events the scaffold
// emits, filtered by gateway-key presence. When apiKey is empty,
// PostToolUse is omitted -- composes with #10's install-time skip.
//
// Matcher choices:
//   - SessionStart "": fires on every session start (only signal).
//   - PostToolUse "Write|Edit|Bash": only write-class tools reach the
//     gateway, matching the #10 scoped-matcher decision.
//   - PreCompact / PostCompact "manual|auto": Claude Code distinguishes
//     manual `/compact` invocations from automatic ones. Using both
//     keeps the hook firing on every compact event without double-
//     dispatching when the user types `/compact`.
func claudeCodeHookEventList(apiKey string) []claudeCodeHookEvent {
	events := []claudeCodeHookEvent{
		{Event: "SessionStart", Verb: "session-start", Matcher: ""},
		{Event: "PreCompact", Verb: "inscribe", Matcher: "manual|auto"},
		{Event: "PostCompact", Verb: "recall", Matcher: "manual|auto"},
	}
	if apiKey != "" {
		// Insert PostToolUse between SessionStart and PreCompact to
		// match the conventional ordering emitted by `hooks install`.
		events = append(events[:1], append(
			[]claudeCodeHookEvent{{Event: "PostToolUse", Verb: "post-tool", Matcher: defaultPostToolMatcher}},
			events[1:]...,
		)...)
	}
	return events
}

// buildPluginManifest returns the .claude-plugin/plugin.json contents.
// All fields per Anthropic Plugins reference; `defaultEnabled: false`
// is the safe default for plugins that mutate user data on install
// (v2.1.154+).
func buildPluginManifest(version string) map[string]any {
	return map[string]any{
		"name":           "ogham",
		"displayName":    "Ogham",
		"version":        version,
		"description":    "Ogham MCP -- semantic memory and knowledge graph for AI agents",
		"author":         map[string]any{"name": "Ogham MCP"},
		"defaultEnabled": false,
	}
}

// buildPluginHooksFile returns the hooks/hooks.json contents. Hook
// commands use `${CLAUDE_PLUGIN_ROOT}/bin/ogham` in exec form (command
// + args) -- the Anthropic-prescribed pattern for plugin-scoped hooks
// (#9 council finding: NOT os.Executable, which is right for
// settings.json but stale for plugin scope).
//
// Mirrors #10's install-time gateway-key skip: omits PostToolUse when
// apiKey is empty so the plugin doesn't produce hook-error noise on
// native-only setups.
func buildPluginHooksFile(apiKey string) map[string]any {
	binPath := claudePluginRoot + "/bin/ogham"
	hooks := map[string]any{}
	for _, ev := range claudeCodeHookEventList(apiKey) {
		hooks[ev.Event] = []map[string]any{
			{
				"matcher": ev.Matcher,
				"hooks": []map[string]any{
					{
						"type":    "command",
						"command": binPath,
						"args":    []string{"hooks", "run", ev.Verb},
					},
				},
			},
		}
	}
	return map[string]any{"hooks": hooks}
}

// buildMcpManifest returns the .mcp.json contents emitted ONLY when
// --with-mcp is passed. At plugin scope every subagent gains access to
// the registered MCP server's tools -- by default we refuse this
// because it violates the superpowers-memory bridge's "subagents
// structurally never touch the store" invariant (#9 council finding).
func buildMcpManifest() map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			"ogham": map[string]any{
				"command": claudePluginRoot + "/bin/ogham",
				"args":    []string{"serve"},
			},
		},
	}
}

// claudePluginTargetDir resolves the scaffold's target directory from
// the --scope and --output flags. Skills-directory layout is the
// default (fastest install path -- loads next session as
// `ogham@skills-dir`, no marketplace plumbing required).
func claudePluginTargetDir(scope, outputOverride string) (string, error) {
	if outputOverride != "" {
		return outputOverride, nil
	}
	switch scope {
	case "user", "":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", "ogham"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve project cwd: %w", err)
		}
		return filepath.Join(cwd, ".claude", "skills", "ogham"), nil
	default:
		return "", fmt.Errorf("--scope must be 'user' or 'project' (got %q)", scope)
	}
}

// copyOghamBinary copies the running ogham binary to <dir>/bin/ogham
// with executable permissions. Idempotent when source and destination
// resolve to the same path (avoid clobbering ourselves mid-copy).
func copyOghamBinary(dir string) error {
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve running binary: %w", err)
	}
	if srcAbs, err := filepath.EvalSymlinks(src); err == nil {
		src = srcAbs
	}
	dstDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstDir, err)
	}
	dst := filepath.Join(dstDir, "ogham")
	if dstAbs, err := filepath.EvalSymlinks(dst); err == nil && dstAbs == src {
		return nil // already in place, nothing to copy
	}
	in, err := os.Open(src) // #nosec G304 -- src is os.Executable, not user input
	if err != nil {
		return fmt.Errorf("open src binary %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	// #nosec G302 -- a binary that Claude Code is going to exec needs
	// the user-exec bit. 0700 is owner-only RWX (no group/world bits).
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		return fmt.Errorf("create dst binary %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy binary contents: %w", err)
	}
	return nil
}

// writeJSONFile writes pretty-printed JSON to path, creating parent
// dirs as needed. Refuses to overwrite an existing file unless force
// is true -- protects against accidentally clobbering a hand-edited
// plugin.json on re-emit.
func writeJSONFile(path string, content map[string]any, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing %s (re-run with --force to replace)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// pluginScaffoldPlan is the inspectable description of what
// `plugin claude-code` would write. Returned by buildPluginScaffold so
// --dry-run can print without touching the filesystem and tests can
// assert on shape directly.
type pluginScaffoldPlan struct {
	Dir         string         `json:"dir"`
	WithMcp     bool           `json:"with_mcp"`
	ApiKeyKnown bool           `json:"api_key_known"`
	Files       map[string]any `json:"files"`
}

// buildPluginScaffold returns the in-memory plan -- no filesystem
// writes. The actual emitter writes each file in plan.Files; --dry-run
// prints the plan as JSON.
func buildPluginScaffold(dir, version, apiKey string, withMcp bool) pluginScaffoldPlan {
	plan := pluginScaffoldPlan{
		Dir:         dir,
		WithMcp:     withMcp,
		ApiKeyKnown: apiKey != "",
		Files: map[string]any{
			filepath.Join(".claude-plugin", "plugin.json"): buildPluginManifest(version),
			filepath.Join("hooks", "hooks.json"):           buildPluginHooksFile(apiKey),
		},
	}
	if withMcp {
		plan.Files[".mcp.json"] = buildMcpManifest()
	}
	return plan
}

var (
	pluginClaudeCodeScope         string
	pluginClaudeCodeOutput        string
	pluginClaudeCodeWithMcp       bool
	pluginClaudeCodeMigrate       bool
	pluginClaudeCodeDryRun        bool
	pluginClaudeCodeForce         bool
	pluginClaudeCodeSkipBinaryCpy bool
)

var pluginClaudeCodeCmd = &cobra.Command{
	Use:   "claude-code",
	Short: "Emit a Claude Code plugin scaffold for Ogham",
	Long: `Emit an Anthropic-prescribed plugin scaffold (.claude-plugin/plugin.json,
hooks/hooks.json, bin/ogham) that wires Ogham's lifecycle hooks via the
plugin path rather than mutating ~/.claude/settings.json.

Default target: ~/.claude/skills/ogham/ (skills-directory plugin layout,
loads next session as ` + "`ogham@skills-dir`" + ` with no marketplace plumbing).

Hook commands use ` + "`${CLAUDE_PLUGIN_ROOT}/bin/ogham`" + ` in exec form (command
+ args) -- the Anthropic-prescribed pattern for plugin-scoped hooks.
This is intentionally different from ` + "`hooks install`" + ` which writes
an absolute path via os.Executable(); plugin install dirs change on
update so a baked absolute path goes stale.

Gateway-aware: same logic as ` + "`hooks install`" + ` (#10). When no api_key
is configured PostToolUse is omitted from hooks.json -- post-tool's
smart filtering is gateway-only and a hook that can't succeed produces
per-call transcript noise.

Flags:
  --scope user|project       Target ~/.claude/ vs ./.claude/ (default: user).
  --output PATH              Override the target directory entirely.
  --migrate-from-settings    Also remove ` + "`hooks install`" + `-owned entries
                             from ~/.claude/settings.json (verb-shape
                             regex; leaves Python ogham-mcp lines).
  --with-mcp                 Also emit .mcp.json registering ogham as
                             a plugin-scope MCP server. Default: refused
                             (plugin-scope MCP gives every subagent
                             access to ogham_* tools, violates the
                             superpowers-memory bridge's "subagents
                             structurally never touch the store" rule).
  --dry-run                  Print the scaffold plan as JSON; write nothing.
  --force                    Overwrite existing plugin.json / hooks.json
                             instead of refusing.
  --skip-binary-copy         Skip copying the running binary to bin/ogham
                             (use when bin/ogham is already populated --
                             e.g. CI-built scaffolds for distribution).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := claudePluginTargetDir(pluginClaudeCodeScope, pluginClaudeCodeOutput)
		if err != nil {
			return err
		}

		// #10 composition: read api_key the same way hooks install does
		// (config file via DefaultPath, then OGHAM_API_KEY env). Empty
		// key -> PostToolUse omitted from the scaffold.
		cfg, _ := config.Load(config.DefaultPath())
		apiKey := ""
		if cfg != nil {
			apiKey = cfg.APIKey
		}

		plan := buildPluginScaffold(dir, Version, apiKey, pluginClaudeCodeWithMcp)

		if pluginClaudeCodeDryRun {
			return emitJSON(plan)
		}

		// Write each file in the plan.
		for rel, content := range plan.Files {
			contentMap, ok := content.(map[string]any)
			if !ok {
				return fmt.Errorf("internal: plan file %q has non-map content", rel)
			}
			if err := writeJSONFile(filepath.Join(dir, rel), contentMap, pluginClaudeCodeForce); err != nil {
				return err
			}
		}

		// Copy bin/ogham unless caller opted out (CI / distribution
		// workflows where the binary is pre-staged).
		if !pluginClaudeCodeSkipBinaryCpy {
			if err := copyOghamBinary(dir); err != nil {
				return err
			}
		}

		// --migrate-from-settings: strip Go-owned entries from
		// ~/.claude/settings.json so the same hooks don't fire both via
		// plugin path and settings path. Uses the same verb-shape regex
		// pruner as `hooks uninstall` (#7 / v0.7.4).
		removed := 0
		if pluginClaudeCodeMigrate {
			n, err := migrateClaudeSettings()
			if err != nil {
				return fmt.Errorf("migrate-from-settings: %w", err)
			}
			removed = n
		}

		fmt.Printf("Wrote Claude Code plugin scaffold to %s\n", dir)
		fmt.Printf("  Files: %d (plugin.json, hooks.json", len(plan.Files))
		if pluginClaudeCodeWithMcp {
			fmt.Print(", .mcp.json")
		}
		fmt.Println(")")
		if !pluginClaudeCodeSkipBinaryCpy {
			fmt.Printf("  Binary: %s\n", filepath.Join(dir, "bin", "ogham"))
		}
		if apiKey == "" {
			fmt.Println("  Skipped PostToolUse: gateway api_key not configured.")
			fmt.Println("    Run `ogham auth login --api-key KEY`, then re-run with --force to refresh hooks.json.")
		} else {
			fmt.Printf("  PostToolUse matcher: %s\n", defaultPostToolMatcher)
		}
		if pluginClaudeCodeMigrate {
			fmt.Printf("  Migrated: %d entr%s stripped from settings.json\n",
				removed, plural(removed, "y", "ies"))
		}
		fmt.Println("\nNext: open Claude Code in a new session. Plugin loads as `ogham@skills-dir`.")
		return nil
	},
}

// migrateClaudeSettings reads ~/.claude/settings.json, strips Go-owned
// ogham hook entries (via the same verb-shape regex used by
// `hooks uninstall`), and writes the file back. Returns the number of
// entries removed. Idempotent: a settings file with no Go-owned entries
// returns 0 and is left untouched.
func migrateClaudeSettings() (int, error) {
	settings, err := readClaudeSettings()
	if err != nil || settings == nil {
		return 0, nil // no settings to migrate from
	}
	removed := pruneOghamGoHooks(settings)
	if removed == 0 {
		return 0, nil
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return 0, err
	}
	// settings.json is user-scope; no need for world-readability. The
	// existing perm on the file is preserved by WriteFile when the file
	// already exists; this mode only takes effect on fresh creates.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return 0, err
	}
	return removed, nil
}

func init() {
	pluginClaudeCodeCmd.Flags().StringVar(&pluginClaudeCodeScope, "scope", "user", "Install scope: user (~/.claude/) or project (./.claude/)")
	pluginClaudeCodeCmd.Flags().StringVar(&pluginClaudeCodeOutput, "output", "", "Override target directory (skips --scope)")
	pluginClaudeCodeCmd.Flags().BoolVar(&pluginClaudeCodeWithMcp, "with-mcp", false, "Also emit .mcp.json (refused by default: plugin-scope MCP grants every subagent ogham_* access)")
	pluginClaudeCodeCmd.Flags().BoolVar(&pluginClaudeCodeMigrate, "migrate-from-settings", false, "Strip `hooks install`-owned entries from ~/.claude/settings.json")
	pluginClaudeCodeCmd.Flags().BoolVar(&pluginClaudeCodeDryRun, "dry-run", false, "Print the scaffold plan as JSON; write nothing")
	pluginClaudeCodeCmd.Flags().BoolVar(&pluginClaudeCodeForce, "force", false, "Overwrite existing plugin.json / hooks.json / .mcp.json")
	pluginClaudeCodeCmd.Flags().BoolVar(&pluginClaudeCodeSkipBinaryCpy, "skip-binary-copy", false, "Skip copying the running binary to bin/ogham (use when pre-staged)")
	pluginCmd.AddCommand(pluginClaudeCodeCmd)
}
