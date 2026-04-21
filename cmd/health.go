package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	// Deprecated: JSON is the default now (use --text), native is the
	// default now (use --sidecar to opt out).
	healthJSONDeprecated   bool
	healthNativeDeprecated bool
	healthLiveEmbedder     bool
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check the Ogham stack is reachable and responsive",
	Long: `Default path (native Go): run parallel probes in-process against
the configured backend (Supabase PostgREST or Postgres via pgx) and
the configured embedding provider. Native mode is ~10x faster because
it skips the sidecar startup.
--sidecar path: spawn the Python MCP sidecar and call its health_check
tool. Useful for diagnosing sidecar-only installs.

Embedder probe is config-validation-only by default to avoid a paid
API call on every invocation. Pass --live-embedder to issue a real
round-trip embedding request.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		if useSidecar() {
			return runHealthSidecar(ctx)
		}
		return runHealthNative(ctx)
	},
}

func runHealthNative(ctx context.Context) error {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return err
	}
	report, err := native.HealthCheck(ctx, cfg, native.HealthOptions{
		LiveEmbedder: healthLiveEmbedder,
	})
	if err != nil {
		return err
	}

	if !useText() {
		return emitJSON(report)
	}

	overall := "ok"
	if !report.OK {
		overall = "degraded"
	}
	fmt.Printf("✓ ogham health (%s, backend=%s, elapsed=%s)\n",
		overall, report.Backend, report.Duration.Truncate(time.Millisecond))
	for _, c := range report.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		dur := c.Duration.Truncate(time.Millisecond)
		if c.OK {
			fmt.Printf("  %s %-22s %6s  %s\n", mark, c.Name, dur, c.Detail)
		} else {
			fmt.Printf("  %s %-22s %6s  ERROR: %s\n", mark, c.Name, dur, c.Error)
		}
	}

	if !report.OK {
		return fmt.Errorf("health check reported %d failing probe(s)", countFailed(report.Checks))
	}
	return nil
}

func countFailed(checks []native.CheckResult) int {
	n := 0
	for _, c := range checks {
		if !c.OK {
			n++
		}
	}
	return n
}

func runHealthSidecar(ctx context.Context) error {
	client, err := connectSidecar(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	result, err := client.CallTool(ctx, "health_check", map[string]any{})
	if err != nil {
		return fmt.Errorf("health_check: %w", err)
	}

	payload, err := toolResultJSON(result)
	if err != nil {
		return err
	}

	if !useText() {
		return emitJSON(payload)
	}

	if m, ok := payload.(map[string]any); ok {
		status, _ := m["status"].(string)
		if status == "" {
			status = "ok"
		}
		provider, _ := m["provider"].(string)
		fmt.Printf("✓ sidecar %s", status)
		if provider != "" {
			fmt.Printf(" (provider=%s)", provider)
		}
		fmt.Println()
		return nil
	}
	fmt.Println("✓ sidecar responded")
	return nil
}

func init() {
	healthCmd.Flags().BoolVar(&healthJSONDeprecated, "json", false, "")
	healthCmd.Flags().BoolVar(&healthNativeDeprecated, "native", false, "")
	_ = healthCmd.Flags().MarkHidden("json")
	_ = healthCmd.Flags().MarkHidden("native")
	healthCmd.Flags().BoolVar(&healthLiveEmbedder, "live-embedder", false, "Native: make a real embedding API call (costs one provider token)")
	rootCmd.AddCommand(healthCmd)
}
