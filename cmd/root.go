package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Global output + backend mode. Persistent flags on the root command,
// readable by any subcommand via the useText / useLegacy helpers.
//
// Defaults:
//   - JSON output  (LLM-friendly, script-friendly; --text for humans)
//   - Native path  (pgx + PostgREST + Gemini, fast; --legacy for Python sidecar)
//
// The Python `ogham` CLI has its own sensible defaults; we match that
// philosophy: a drop-in replacement should not force users to type
// --native --json on every invocation.
var (
	rootTextFlag    bool
	rootLegacyFlag  bool
	rootPythonAlias bool // --python is the alias for --legacy
	rootQuietFlag   bool
)

var rootCmd = &cobra.Command{
	Use:   "ogham",
	Short: "Ogham MCP client -- persistent shared memory for AI agents",
	Long: `A lightweight MCP client for the Ogham memory stack.

Defaults (v0.3.0-rc4+):
  - JSON output for stable scripting / LLM consumption -- use --text for humans
  - Native path (direct Postgres / Supabase / Gemini) for speed -- use --legacy
    (or --python) to route through the Python MCP sidecar instead`,
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

// useLegacy reports whether the user asked to route through the Python
// sidecar (the pre-rc4 default path). Accepts --legacy or --python.
func useLegacy() bool { return rootLegacyFlag || rootPythonAlias }

// useQuiet reports whether the user asked to suppress stderr informational
// notices (currently just the sidecar-fallback message).
func useQuiet() bool { return rootQuietFlag }

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
	rootCmd.PersistentFlags().BoolVar(&rootLegacyFlag, "legacy", false,
		"Route through the Python MCP sidecar instead of native Go")
	rootCmd.PersistentFlags().BoolVar(&rootPythonAlias, "python", false,
		"Alias for --legacy")
	rootCmd.PersistentFlags().BoolVarP(&rootQuietFlag, "quiet", "q", false,
		"Suppress stderr informational notices (sidecar fallback message)")
}
