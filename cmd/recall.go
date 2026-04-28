// Package cmd: `ogham recall ...` subcommands.
//
// These wrap the v0.13 native recall verbs (Track 3 of the v0.13 release).
// All four are SQL-only fast paths -- sub-100ms cold-start friendly for
// Lambda thin clients. The synthesis verbs (compile_wiki, recompute,
// reinforce, contradict) stay on the Python sidecar; this CLI doesn't
// expose them through `ogham recall`.

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

var recallCmd = &cobra.Command{
	Use:   "recall",
	Short: "Read-only recall verbs (sub-100ms cold-start)",
	Long: `Recall verbs absorbed natively for the Lambda fast path:

  ogham recall topic-summary <topic>     Cached topic summary (level= picks
                                          one_line / short / body)
  ogham recall walk-knowledge <id>        Direction-aware graph walk
  ogham recall lint-wiki                  Read-only health report

These verbs are SQL-only -- no LLM round-trip, no embedding generation.
Use the Python sidecar (--sidecar) for compile_wiki, reinforce_memory,
contradict_memory, and other synthesis verbs.`,
	Args: cobra.NoArgs,
}

// -----------------------------------------------------------------------
// recall topic-summary

var (
	recallTSProfile string
	recallTSLevel   string
)

var recallTopicSummaryCmd = &cobra.Command{
	Use:   "topic-summary <topic>",
	Short: "Fetch a cached topic summary at the requested resolution",
	Long: `Read the cached summary from topic_summaries for the active profile.

The --level flag chooses the resolution (token cost decreases as the level
gets shorter):

  --level=body       full markdown body (~1000 words; v0.12 default)
  --level=short      one paragraph (~150-300 tokens)
  --level=one_line   single sentence (~30-50 tokens)

Pre-033 rows that lack TLDR forms transparently fall back to body and
indicate the fallback via requested_level in the JSON response.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 30*time.Second)
		defer cancelT()

		level, err := native.ParseTLDRLevel(recallTSLevel)
		if err != nil {
			return err
		}

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profile := recallTSProfile
		if profile == "" {
			profile = cfg.Profile
		}

		res, err := native.QueryTopicSummary(ctx, cfg, profile, args[0], level)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(res)
		}

		// Human-readable form.
		if res.Status == native.StatusNotCached {
			fmt.Printf("topic %q not cached in profile %q\n", res.TopicKey, res.Profile)
			fmt.Println(res.Message)
			return nil
		}
		fmt.Printf("# %s (profile=%s, level=%s, version=%d)\n",
			res.TopicKey, res.Profile, res.Level, res.Version)
		if res.RequestedLevel != "" {
			fmt.Printf("# fallback: requested %s, served %s (TLDR not generated yet)\n",
				res.RequestedLevel, res.Level)
		}
		fmt.Println()
		fmt.Println(res.Body)
		return nil
	},
}

// -----------------------------------------------------------------------
// recall walk-knowledge

var (
	recallWalkDepth       int
	recallWalkDirection   string
	recallWalkMinStrength float64
	recallWalkLimit       int
	recallWalkTypes       string
)

var recallWalkKnowledgeCmd = &cobra.Command{
	Use:   "walk-knowledge <memory-id>",
	Short: "Direction-aware graph walk over memory_relationships",
	Long: `Walk the memory_relationships graph from a starting memory id.

  --depth          edges to traverse (0..5; default 1)
  --direction      outgoing | incoming | both (default both)
  --min-strength   filter edges by strength (0.0..1.0)
  --types          comma-separated relationship_type values to include
                   (e.g. depends_on,cites)
  --limit          cap on returned nodes (default 50)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 30*time.Second)
		defer cancelT()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		opts := native.WalkKnowledgeOptions{
			Depth:             recallWalkDepth,
			Direction:         recallWalkDirection,
			MinStrength:       recallWalkMinStrength,
			Limit:             recallWalkLimit,
			RelationshipTypes: splitCSV(recallWalkTypes),
		}
		res, err := native.WalkKnowledge(ctx, cfg, args[0], opts)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(res)
		}
		fmt.Printf("walk from %s (depth=%d direction=%s) -- %d nodes\n",
			res.StartID, res.Depth, res.Direction, res.NodeCount)
		for i, n := range res.Nodes {
			rel := n.Relationship
			if rel == "" {
				rel = "(?)"
			}
			fmt.Printf("%2d. depth=%d %s edge=%.2f via=%s\n   %s\n",
				i+1, n.Depth, rel, n.EdgeStrength, n.DirectionUsed,
				truncate(n.Content, 100))
		}
		return nil
	},
}

// -----------------------------------------------------------------------
// recall lint-wiki

var (
	recallLintProfile    string
	recallLintStableDays int
	recallLintSampleSize int
	recallLintSkipDrift  bool
)

var recallLintWikiCmd = &cobra.Command{
	Use:   "lint-wiki",
	Short: "Read-only health report across the wiki layer",
	Long: `Aggregate report covering contradictions, orphans, stale lifecycle
rows, stale topic summaries, and source-hash drift on fresh summaries.

Read-only -- this never writes back. Use the Python sidecar to recompute
stale summaries or sweep them.`,
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
		profile := recallLintProfile
		if profile == "" {
			profile = cfg.Profile
		}
		opts := native.LintWikiOptions{
			SampleSize: recallLintSampleSize,
			StableDays: recallLintStableDays,
		}
		// SetIncludeDrift ensures we propagate "skip" intent unambiguously.
		opts.SetIncludeDrift(!recallLintSkipDrift)

		rep, err := native.LintWiki(ctx, cfg, profile, opts)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(rep)
		}
		status := "HEALTHY"
		if !rep.Healthy {
			status = fmt.Sprintf("UNHEALTHY (%d issues)", rep.IssueCount)
		}
		fmt.Printf("wiki lint: profile=%s -- %s\n", rep.Profile, status)
		fmt.Printf("  contradictions:   %d\n", rep.Contradictions.Count)
		fmt.Printf("  orphans:          %d\n", rep.Orphans.Count)
		fmt.Printf("  stale_lifecycle:  %d (older than %d days)\n",
			rep.StaleLifecycle.Count, rep.StaleLifecycle.OlderThanDays)
		fmt.Printf("  stale_summaries:  %d\n", rep.StaleSummaries.Count)
		if rep.SummaryDrift.Skipped {
			fmt.Printf("  summary_drift:    skipped\n")
		} else {
			fmt.Printf("  summary_drift:    %d\n", rep.SummaryDrift.Count)
		}
		return nil
	},
}

func init() {
	// recall topic-summary
	recallTopicSummaryCmd.Flags().StringVar(&recallTSProfile, "profile", "",
		"Profile to read (defaults to active)")
	recallTopicSummaryCmd.Flags().StringVar(&recallTSLevel, "level", "body",
		"Resolution: one_line | short | body (default body)")
	recallCmd.AddCommand(recallTopicSummaryCmd)

	// recall walk-knowledge
	recallWalkKnowledgeCmd.Flags().IntVar(&recallWalkDepth, "depth", 1,
		"Edges to traverse (0..5)")
	recallWalkKnowledgeCmd.Flags().StringVar(&recallWalkDirection, "direction", "both",
		"outgoing | incoming | both")
	recallWalkKnowledgeCmd.Flags().Float64Var(&recallWalkMinStrength, "min-strength", 0.0,
		"Filter edges below this strength")
	recallWalkKnowledgeCmd.Flags().IntVar(&recallWalkLimit, "limit", 50,
		"Cap on returned nodes")
	recallWalkKnowledgeCmd.Flags().StringVar(&recallWalkTypes, "types", "",
		"Comma-separated relationship_type values")
	recallCmd.AddCommand(recallWalkKnowledgeCmd)

	// recall lint-wiki
	recallLintWikiCmd.Flags().StringVar(&recallLintProfile, "profile", "",
		"Profile to lint (defaults to active)")
	recallLintWikiCmd.Flags().IntVar(&recallLintStableDays, "stable-days", 90,
		"Lifecycle 'stuck in stable' threshold")
	recallLintWikiCmd.Flags().IntVar(&recallLintSampleSize, "sample-size", 10,
		"Per-category preview rows")
	recallLintWikiCmd.Flags().BoolVar(&recallLintSkipDrift, "skip-drift", false,
		"Skip the per-topic source_hash drift check (slow on huge profiles)")
	recallCmd.AddCommand(recallLintWikiCmd)

	rootCmd.AddCommand(recallCmd)
}
