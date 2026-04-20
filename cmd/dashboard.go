package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	dashboardPort     int
	dashboardBind     string
	dashboardExtrasEx string
)

// dashboardCmd delegates entirely to the Python Prefab dashboard. This
// is deliberate: rewriting the dashboard in Go would require a Node
// frontend or a Go Prefab port, neither of which we want. The Go CLI
// stays the one-stop entry point by exec'ing the Python dashboard as
// a child process and forwarding stdio + signals.
var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Launch the Prefab dashboard (delegates to Python)",
	Long: `Launches the Ogham Prefab UI dashboard. The dashboard stays in Python
because it uses FastMCP + Prefab components; rewriting in Go would
require a Node frontend we deliberately don't ship.

This subcommand invokes the Python sidecar's dashboard subcommand and
forwards stdio + SIGINT/SIGTERM so Ctrl+C cleanly stops the server.

Configuration is picked up from the same .env files as the other
subcommands -- DATABASE_*, SUPABASE_*, EMBEDDING_PROVIDER etc. The
Python ogham needs the [dashboard] extra; set
OGHAM_SIDECAR_EXTRAS=postgres,gemini,dashboard in your .env or pass
--extras explicitly.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve the subprocess command the same way the sidecar client
		// does (OGHAM_SIDECAR_CMD override, OGHAM_SIDECAR_EXTRAS, or the
		// default uv tool run line), then swap the last two tokens
		// ("ogham serve") for ("ogham dashboard" + args).
		argv := resolveDashboardCommand()

		// Feed TOML-derived env through so Python sees the same config
		// sidecar-mode commands already expose.
		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}

		// Add dashboard-specific args. --profile is explicit because
		// Python's typer CLI hard-codes a "default" default for that
		// option that would otherwise beat any DEFAULT_PROFILE env var.
		if dashboardPort > 0 {
			argv = append(argv, "--port", fmt.Sprintf("%d", dashboardPort))
		}
		if dashboardBind != "" {
			argv = append(argv, "--host", dashboardBind)
		}
		profile := cfg.Profile
		if profile == "" {
			profile = "default"
		}
		argv = append(argv, "--profile", profile)
		argv = append(argv, args...) // forward anything extra the user passed
		env := append(os.Environ(), native.LoadEnvFiles()...)
		env = append(env, cfg.SidecarEnv()...)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		child := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // user-controlled by design
		child.Env = env
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr

		// Propagate SIGINT to the child process group so Ctrl+C reaches
		// Python cleanly; CommandContext already kills the process when
		// ctx is cancelled, but setting Cancel explicitly with SIGINT
		// (rather than the default SIGKILL) gives Python a chance to shut
		// down gracefully.
		child.Cancel = func() error {
			if child.Process != nil {
				return child.Process.Signal(syscall.SIGINT)
			}
			return nil
		}

		fmt.Fprintf(os.Stderr, "[ogham dashboard] launching: %s\n", strings.Join(argv, " "))
		if err := child.Run(); err != nil {
			// Exit errors from the child -- pass through exit code semantics
			// by returning a plain error. cobra will print to stderr.
			return fmt.Errorf("dashboard exited: %w", err)
		}
		return nil
	},
}

// resolveDashboardCommand builds the argv for `ogham dashboard` as a
// subprocess. Mirrors the sidecar resolver logic but swaps the serve
// terminal verb for dashboard.
func resolveDashboardCommand() []string {
	if raw := strings.TrimSpace(os.Getenv("OGHAM_DASHBOARD_CMD")); raw != "" {
		return strings.Fields(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("OGHAM_SIDECAR_CMD")); raw != "" {
		return swapTerminalToDashboard(strings.Fields(raw))
	}

	// Fall back to a uv tool run line with sensible extras for the
	// dashboard (postgres + dashboard required; gemini optional unless
	// the user is on Gemini embeddings).
	extras := strings.TrimSpace(os.Getenv("OGHAM_SIDECAR_EXTRAS"))
	if extras == "" {
		extras = "postgres,dashboard"
	} else if !strings.Contains(extras, "dashboard") {
		extras = extras + ",dashboard"
	}
	return []string{
		"uv", "tool", "run", "--python", "3.13",
		"--from", fmt.Sprintf("ogham-mcp[%s]", extras),
		"ogham", "dashboard",
	}
}

// swapTerminalToDashboard takes ["uv", "tool", "run", ..., "ogham", "serve"]
// and returns it with the final "serve" rewritten to "dashboard". Falls
// back to appending "dashboard" if no "ogham serve" suffix is found.
func swapTerminalToDashboard(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	// Look for the last "serve" and rewrite it.
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] == "serve" {
			out[i] = "dashboard"
			return out
		}
	}
	// Didn't find serve -- user's override command is doing something
	// unusual. Append dashboard and let Python figure it out.
	return append(out, "dashboard")
}

func init() {
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 0, "Port for the dashboard HTTP server (forwarded to Python)")
	dashboardCmd.Flags().StringVar(&dashboardBind, "bind", "", "Bind address (forwarded to Python)")
	rootCmd.AddCommand(dashboardCmd)
}
