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
	listLimit   int
	listProfile string
	listSource  string
	listTags    string
	// Deprecated: kept as silent no-ops so pre-rc4 scripts keep working.
	// JSON is now the default (use --text), native is now the default
	// (use --legacy).
	listJSONDeprecated   bool
	listNativeDeprecated bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent memories",
	Long: `Return the most recent memories from the active profile.

Default path: calls the Python MCP sidecar's list_recent tool.
--native path: connects to Postgres directly via pgx -- this is the first
absorbed tool. Requires DATABASE_URL or [database] url in config.toml.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		if useLegacy() {
			return runListSidecar(ctx)
		}
		return runListNative(ctx)
	},
}

func runListNative(ctx context.Context) error {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return err
	}
	if listProfile != "" {
		cfg.Profile = listProfile
	}
	memories, err := native.List(ctx, cfg, native.ListOptions{
		Limit:  listLimit,
		Source: listSource,
		Tags:   splitCSV(listTags),
	})
	if err != nil {
		return err
	}

	if !useText() {
		return emitJSON(memories)
	}
	if len(memories) == 0 {
		fmt.Println("No memories.")
		return nil
	}
	for i, m := range memories {
		printNativeMemory(i+1, m)
	}
	return nil
}

func runListSidecar(ctx context.Context) error {
	client, err := connectSidecar(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	toolArgs := map[string]any{"limit": listLimit}
	if listProfile != "" {
		toolArgs["profile"] = listProfile
	}
	if listSource != "" {
		toolArgs["source"] = listSource
	}
	if tags := splitCSV(listTags); tags != nil {
		toolArgs["tags"] = tags
	}

	result, err := client.CallTool(ctx, "list_recent", toolArgs)
	if err != nil {
		return fmt.Errorf("list_recent: %w", err)
	}

	payload, err := toolResultJSON(result)
	if err != nil {
		return err
	}
	if !useText() {
		return emitJSON(payload)
	}

	mems := extractMemories(payload)
	if len(mems) == 0 {
		fmt.Println("No memories.")
		return nil
	}
	for i, m := range mems {
		printMemoryMap(i+1, m)
	}
	return nil
}

func init() {
	listCmd.Flags().IntVar(&listLimit, "limit", 20, "Maximum number of memories to list")
	listCmd.Flags().StringVar(&listProfile, "profile", "", "Profile to list from (defaults to config)")
	listCmd.Flags().StringVar(&listSource, "source", "", "Filter by source label (e.g. claude-code, hook:post-tool)")
	listCmd.Flags().StringVar(&listTags, "tags", "", "Filter by any of these comma-separated tags")
	// Deprecated aliases kept silent so pre-rc4 scripts don't error.
	listCmd.Flags().BoolVar(&listJSONDeprecated, "json", false, "")
	listCmd.Flags().BoolVar(&listNativeDeprecated, "native", false, "")
	_ = listCmd.Flags().MarkHidden("json")
	_ = listCmd.Flags().MarkHidden("native")
	rootCmd.AddCommand(listCmd)
}
