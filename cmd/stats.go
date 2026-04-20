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
	statsJSONDeprecated bool
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Summarise the active profile: total, top sources, top tags",
	Long: `Native-only command (no sidecar fallback yet) that reports the
headline numbers for the active profile:
  - total non-expired memories
  - top 10 sources
  - top 10 tags
  - untagged count, TTL'd count, expiring-in-7-days count

For Supabase the aggregation is client-side over the first 1000 rows
(PostgREST default limit). Direct Postgres uses GROUP BY for accurate
counts regardless of profile size.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		stats, err := native.GetStats(ctx, cfg)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(stats)
		}
		printStatsHuman(stats)
		return nil
	},
}

func printStatsHuman(s *native.Stats) {
	fmt.Printf("profile:          %s\n", s.Profile)
	fmt.Printf("total memories:   %d  (untagged %d, with TTL %d, expiring<7d %d)\n",
		s.Total, s.Untagged, s.WithTTL, s.Expiring)

	if len(s.Sources) > 0 {
		fmt.Println("\ntop sources:")
		for _, b := range s.Sources {
			fmt.Printf("  %6d  %s\n", b.Count, b.Name)
		}
	}
	if len(s.TopTags) > 0 {
		fmt.Println("\ntop tags:")
		for _, b := range s.TopTags {
			fmt.Printf("  %6d  %s\n", b.Count, b.Name)
		}
	}
}

func init() {
	statsCmd.Flags().BoolVar(&statsJSONDeprecated, "json", false, "")
	_ = statsCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(statsCmd)
}
