package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	searchLimit   int
	searchTags    string
	searchProfile string
	// Deprecated -- kept as silent no-ops; JSON + native are now default.
	searchJSONDeprecated   bool
	searchNativeDeprecated bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Hybrid search across stored memories",
	Long: `Run a hybrid (vector + keyword) search against the active profile.

Default path (native Go): generates the query embedding via the
configured provider (Gemini / Ollama / OpenAI / Voyage / Mistral) and
runs hybrid_search_memories via pgx directly. Requires DATABASE_URL,
EMBEDDING_PROVIDER, and the provider API key in your .env or
config.toml.
--sidecar path: calls the Python MCP sidecar's hybrid_search tool for
the full retrieval pipeline (intent detection, strided retrieval,
query reformulation, MMR, graph augmentation).`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		query := strings.Join(args, " ")

		if useSidecar() {
			return runSearchSidecar(ctx, query)
		}
		return runSearchNative(ctx, query)
	},
}

func runSearchNative(ctx context.Context, query string) error {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return err
	}
	if searchProfile != "" {
		cfg.Profile = searchProfile
	}
	results, err := native.Search(ctx, cfg, query, native.SearchOptions{
		Limit:   searchLimit,
		Tags:    splitCSV(searchTags),
		Profile: searchProfile,
	})
	if err != nil {
		return err
	}

	if !useText() {
		return emitJSON(results)
	}
	if len(results) == 0 {
		fmt.Println("No results.")
		return nil
	}
	for i, r := range results {
		printNativeSearchResult(i+1, r)
	}
	return nil
}

func runSearchSidecar(ctx context.Context, query string) error {
	client, err := connectSidecar(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	toolArgs := map[string]any{
		"query": query,
		"limit": searchLimit,
	}
	if tags := splitCSV(searchTags); tags != nil {
		toolArgs["tags"] = tags
	}
	if searchProfile != "" {
		toolArgs["profile"] = searchProfile
	}

	result, err := client.CallTool(ctx, "hybrid_search", toolArgs)
	if err != nil {
		return fmt.Errorf("hybrid_search: %w", err)
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
		fmt.Println("No results.")
		return nil
	}
	for i, m := range mems {
		printMemoryMap(i+1, m)
	}
	return nil
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", 10, "Maximum number of results")
	searchCmd.Flags().StringVar(&searchTags, "tags", "", "Filter by comma-separated tags")
	searchCmd.Flags().StringVar(&searchProfile, "profile", "", "Profile to search (defaults to config)")
	searchCmd.Flags().BoolVar(&searchJSONDeprecated, "json", false, "")
	searchCmd.Flags().BoolVar(&searchNativeDeprecated, "native", false, "")
	_ = searchCmd.Flags().MarkHidden("json")
	_ = searchCmd.Flags().MarkHidden("native")
	rootCmd.AddCommand(searchCmd)
}
