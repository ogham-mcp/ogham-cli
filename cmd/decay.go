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
	decayProfile        string
	decayBatchSize      int
	decayDryRun         bool
	decayJSONDeprecated bool
)

var decayCmd = &cobra.Command{
	Use:   "decay",
	Short: "Apply Hebbian decay to stale memories",
	Long: `Calls apply_hebbian_decay(target_profile, batch_size) to reduce the
confidence of memories that haven't been accessed recently. Matches
Python's 'ogham decay' command; same SQL RPC underneath.

Use --dry-run to see how many memories would be affected without
changing anything.`,
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
		profile := decayProfile
		if profile == "" {
			profile = cfg.Profile
		}

		res, err := native.Decay(ctx, cfg, profile, decayBatchSize, decayDryRun)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(res)
		}
		if res.DryRun {
			fmt.Printf("%d memories eligible for decay in profile %q (dry run)\n", res.Eligible, res.Profile)
			return nil
		}
		fmt.Printf("decayed %d memories in profile %q\n", res.Decayed, res.Profile)
		return nil
	},
}

func init() {
	decayCmd.Flags().StringVar(&decayProfile, "profile", "", "Profile to decay (defaults to active)")
	decayCmd.Flags().IntVar(&decayBatchSize, "batch-size", 1000, "Max memories to decay per run")
	decayCmd.Flags().BoolVar(&decayDryRun, "dry-run", false, "Count eligible memories without decaying")
	decayCmd.Flags().BoolVar(&decayJSONDeprecated, "json", false, "")
	_ = decayCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(decayCmd)
}
