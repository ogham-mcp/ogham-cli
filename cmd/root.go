package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// Global output + backend mode. Persistent flags on the root command,
// readable by any subcommand via the useText / useSidecar helpers.
//
// Defaults:
//   - JSON output  (LLM-friendly, script-friendly; --text for humans)
//   - Native path  (pgx + PostgREST + Gemini, fast; --sidecar routes through
//     the Python MCP for the full retrieval pipeline -- intent detection,
//     strided retrieval, MMR, graph augmentation, query reformulation)
//
// --legacy is a hidden backward-compat alias for --sidecar. It still works
// but emits a deprecation warning; the alias will be removed in v0.8.
// The old name was misleading -- "legacy" read as "deprecated, will be
// removed" when the Python MCP is actually the canonical brain and the
// Go CLI's enterprise-friendly access door. Renamed 2026-04-22.
//
// The Python `ogham` CLI has its own sensible defaults; we match that
// philosophy: a drop-in replacement should not force users to type
// --native --json on every invocation.
var (
	rootTextFlag    bool
	rootSidecarFlag bool
	rootLegacyFlag  bool // deprecated alias for --sidecar (removed v0.8)
	rootPythonAlias bool // --python is the alias for --sidecar
	rootQuietFlag   bool
)

var rootCmd = &cobra.Command{
	Use:   "ogham",
	Short: "Ogham MCP client -- persistent shared memory for AI agents",
	Long: `A lightweight MCP client for the Ogham memory stack.

Defaults (v0.3.0-rc4+):
  - JSON output for stable scripting / LLM consumption -- use --text for humans
  - Native path (direct Postgres / Supabase / Gemini) for speed -- use
    --sidecar (or --python) to route through the Python MCP sidecar for
    the full retrieval pipeline (intent detection, strided retrieval,
    query reformulation, MMR, graph augmentation)`,
	// PersistentPreRunE fires before every subcommand. Installs the
	// shared slog handler so -v / --quiet apply everywhere, and emits
	// the --legacy deprecation warning when that hidden alias is used.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		initLogging()
		warnLegacyDeprecated()
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return serveCmd.RunE(cmd, args)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

// useText reports whether the user asked for human-readable output.
// JSON is the default because both scripts and LLMs parse it reliably
// without string heuristics.
func useText() bool { return rootTextFlag }

// useSidecar reports whether the user asked to route through the Python
// MCP sidecar for the full-fidelity retrieval pipeline. Accepts
// --sidecar (new canonical name), --legacy (deprecated alias, removed
// in v0.8), or --python (alias kept for users who expect the Python-
// wording).
func useSidecar() bool { return rootSidecarFlag || rootLegacyFlag || rootPythonAlias }

// useLegacy is retained as a thin synonym of useSidecar so existing
// call-sites keep compiling while we migrate. Prefer useSidecar in
// new code. Scheduled for removal alongside the --legacy flag in v0.8.
func useLegacy() bool { return useSidecar() }

// useQuiet reports whether the user asked to suppress stderr informational
// notices (currently just the sidecar-fallback message).
func useQuiet() bool { return rootQuietFlag }

// warnLegacyDeprecated emits a one-line slog.Warn when the user passes
// --legacy. Suppressed when --quiet. Deliberately uses slog (stderr)
// rather than fmt.Fprintln so it respects the shared log level and
// never leaks into stdout JSON that scripts / LLMs are parsing.
func warnLegacyDeprecated() {
	if rootLegacyFlag && !useQuiet() {
		slog.Warn("`--legacy` is deprecated; use `--sidecar`. The alias will be removed in v0.8.")
	}
}

// Execute is the single entry point main.go calls.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&rootTextFlag, "text", false,
		"Human-readable output (default is JSON for LLM / script consumption)")
	rootCmd.PersistentFlags().BoolVar(&rootSidecarFlag, "sidecar", false,
		"Route through the Python MCP sidecar for the full retrieval pipeline (intent detection, strided retrieval, query reformulation, graph augmentation). Default is the fast native Go path.")
	rootCmd.PersistentFlags().BoolVar(&rootLegacyFlag, "legacy", false,
		"Deprecated alias for --sidecar; removed in v0.8.")
	rootCmd.PersistentFlags().BoolVar(&rootPythonAlias, "python", false,
		"Alias for --sidecar")
	// Hide --legacy from --help so new users don't pick it up; the flag
	// still parses (backward compat) and still works, but the persistent
	// PreRunE warns on use.
	_ = rootCmd.PersistentFlags().MarkHidden("legacy")
	rootCmd.PersistentFlags().BoolVarP(&rootQuietFlag, "quiet", "q", false,
		"Suppress stderr informational notices (sidecar fallback message)")
	// -v raises the stderr log level one step: default=warn, -v=info, -vv=debug.
	// --quiet overrides (error-only). Every slog.* call in the tree lands
	// in the same stderr stream once this is set in PersistentPreRunE.
	rootCmd.PersistentFlags().CountVarP(&verboseCount, "verbose", "v",
		"Increase stderr log verbosity (-v = info, -vv = debug)")
}
