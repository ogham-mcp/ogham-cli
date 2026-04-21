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
	cleanupProfile        string
	cleanupDryRun         bool
	cleanupJSONDeprecated bool
	cleanupYes            bool
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove expired memories from a profile",
	Long: `Deletes every memory whose expires_at is in the past for the given
profile via the cleanup_expired_memories(target_profile) RPC.

Run with --dry-run to count first without deleting. Mirrors Python's
'ogham cleanup' command with the same confirmation-on-stdin safety gate.`,
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
		profile := cleanupProfile
		if profile == "" {
			profile = cfg.Profile
		}

		if cleanupDryRun {
			n, err := native.CountExpired(ctx, cfg, profile)
			if err != nil {
				return err
			}
			if !useText() {
				return emitJSON(map[string]any{"profile": profile, "expired": n, "dry_run": true})
			}
			fmt.Printf("%d expired memories in profile %q (dry run; nothing deleted)\n", n, profile)
			return nil
		}

		if !cleanupYes {
			n, _ := native.CountExpired(ctx, cfg, profile)
			if n == 0 {
				fmt.Printf("no expired memories in profile %q\n", profile)
				return nil
			}
			fmt.Printf("Delete %d expired memories from profile %q? [y/N] ", n, profile)
			var answer string
			_, _ = fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" && answer != "yes" {
				fmt.Println("aborted")
				return nil
			}
		}

		res, err := native.Cleanup(ctx, cfg, profile)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(res)
		}
		fmt.Printf("deleted %d expired memories from profile %q\n", res.Deleted, res.Profile)
		return nil
	},
}

func init() {
	cleanupCmd.Flags().StringVar(&cleanupProfile, "profile", "", "Profile to clean (defaults to active)")
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "Count expired memories without deleting")
	cleanupCmd.Flags().BoolVarP(&cleanupYes, "yes", "y", false, "Skip confirmation prompt")
	cleanupCmd.Flags().BoolVar(&cleanupJSONDeprecated, "json", false, "")
	_ = cleanupCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(cleanupCmd)
}
