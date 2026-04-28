// Package cmd: `ogham lifecycle ...` subcommands.
//
// `lifecycle advance` is the SQL-only stage promoter: fresh -> stable
// for memories that have dwelled past the dual-signal gate, and
// editing-window close for editing rows past the 30-min default.
//
// Mixed-version safe: pre-026 clusters without memory_lifecycle return
// a zeroed report flagged with LifecycleAbsent rather than failing.

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

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle",
	Short: "Memory lifecycle stage management",
	Long: `Inspect and advance the memory_lifecycle pipeline.

  ogham lifecycle advance     Run the fresh->stable + editing-close
                              sweep for the active profile.

The reverse direction (open editing windows on retrieval) and the
hot-path search-triggered advances stay on the Python sidecar -- those
are wired into store/search hooks, not user-invoked.`,
	Args: cobra.NoArgs,
}

var (
	lifecycleAdvanceProfile string
)

var lifecycleAdvanceCmd = &cobra.Command{
	Use:   "advance",
	Short: "Run a fresh->stable + editing-close sweep",
	Long: `Promote memories whose lifecycle has matured past the dual-signal gate.

Two batched UPDATEs:

  1. fresh -> stable: rows past the 1h dwell window AND clearing the
     surprise>=0.3 OR importance>=0.5 gate.
  2. editing -> stable: editing windows past the 30-min cutoff.

Returns counts for each transition. Run on a schedule (cron / systemd
timer / Lambda EventBridge) at whatever cadence fits your write
volume; idempotent on no-op.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 60*time.Second)
		defer cancelT()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profile := lifecycleAdvanceProfile
		if profile == "" {
			profile = cfg.Profile
		}

		rep, err := native.AdvanceLifecycle(ctx, cfg, profile, native.AdvanceLifecycleOptions{})
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(rep)
		}
		if rep.LifecycleAbsent {
			fmt.Printf("lifecycle table absent on profile %q (pre-026 schema) -- nothing to advance\n", rep.Profile)
			return nil
		}
		fmt.Printf("lifecycle advance for profile %q:\n", rep.Profile)
		fmt.Printf("  fresh -> stable:  %d\n", rep.FreshToStable)
		fmt.Printf("  editing closed:   %d\n", rep.EditingClosed)
		return nil
	},
}

func init() {
	lifecycleAdvanceCmd.Flags().StringVar(&lifecycleAdvanceProfile, "profile", "",
		"Profile to advance (defaults to active)")
	lifecycleCmd.AddCommand(lifecycleAdvanceCmd)
	rootCmd.AddCommand(lifecycleCmd)
}
