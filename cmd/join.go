// Package cmd: `ogham join` -- walk the entity graph along a predicate
// path from a start entity, breadth-first. Native-first (direct Postgres
// via pgx); falls back to the Python sidecar's query_join MCP tool with
// --sidecar.

package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

var (
	joinPath      []string
	joinHopLimit  int
	joinDirection string
	joinProfile   string
)

var joinCmd = &cobra.Command{
	Use:   "join <start-entity>",
	Short: "Walk the entity graph along a predicate path from a start entity",
	Long: `Breadth-first traversal of the entity graph, one predicate per hop.

start-entity may be a canonical entity name or an alias. --path is a
comma-separated list of v1 predicates (e.g. DEPENDS_ON,OWNS); --hop-limit
is required and must be >= len(path). The path either resolves fully or
the result is the empty shape {"entities":[],"edges":[],"citations":[]} --
an unresolvable start entity or a dead-end hop is a legitimate empty
result, not an error.

Only current edges (valid_to IS NULL) are read. Cycle detection uses a
visited set, so a loop in the graph terminates the traversal instead of
looping forever.

Native-only against the postgres backend today -- use --sidecar to route
through the Python MCP server's query_join tool instead.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(joinPath) == 0 {
			return fmt.Errorf("--path is required (comma-separated predicate list, e.g. DEPENDS_ON,OWNS)")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		if useSidecar() {
			return runJoinSidecar(ctx, args)
		}
		return runJoinNative(ctx, args)
	},
}

// runJoinNative runs QueryJoin against the configured postgres backend
// directly. Emits QueryJoinResult as JSON (default) or a small tree with
// --text.
func runJoinNative(ctx context.Context, args []string) error {
	cfg, err := loadNativeConfig()
	if err != nil {
		return err
	}

	profile := joinProfile
	if profile == "" {
		profile = native.ActiveProfile(cfg)
	}

	res, err := native.QueryJoin(ctx, cfg, native.QueryJoinOptions{
		StartEntity:   args[0],
		PredicatePath: joinPath,
		HopLimit:      joinHopLimit,
		Direction:     joinDirection,
		Profile:       profile,
	})
	if err != nil {
		return fmt.Errorf("native join: %w", err)
	}

	if !useText() {
		return emitJSON(res)
	}

	fmt.Printf("join from %q -- %d entities, %d edges\n", args[0], len(res.Entities), len(res.Edges))
	for i, e := range res.Entities {
		fmt.Printf("%2d. %s (%s) id=%d\n", i+1, e.CanonicalName, e.EntityType, e.ID)
	}
	for i, e := range res.Edges {
		fmt.Printf("  hop %d: %d -[%s]-> %d\n", i+1, e.SubjectID, e.Predicate, e.ObjectID)
	}
	return nil
}

// runJoinSidecar is the --sidecar path. Passes the same argument shape
// the Python query_join MCP tool expects; let Python respond if it lacks
// the verb (that IS the fallback -- we don't fabricate a contract).
func runJoinSidecar(ctx context.Context, args []string) error {
	noteSidecarFallback("join")

	client, err := connectSidecar(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	toolArgs := map[string]any{
		"start_entity":   args[0],
		"predicate_path": joinPath,
		"hop_limit":      joinHopLimit,
	}
	if joinDirection != "" {
		toolArgs["direction"] = joinDirection
	}
	if joinProfile != "" {
		toolArgs["profile"] = joinProfile
	}

	result, err := client.CallTool(ctx, "query_join", toolArgs)
	if err != nil {
		return fmt.Errorf("query_join: %w", err)
	}

	payload, err := toolResultJSON(result)
	if err != nil {
		return err
	}
	if !useText() {
		return emitJSON(payload)
	}
	fmt.Println("Joined.")
	return nil
}

func init() {
	joinCmd.Flags().StringSliceVar(&joinPath, "path", nil, "Comma-separated predicate path, e.g. DEPENDS_ON,OWNS (required)")
	joinCmd.Flags().IntVar(&joinHopLimit, "hop-limit", 0, "Maximum hops to traverse (required, must be >= 1)")
	joinCmd.Flags().StringVar(&joinDirection, "direction", "outgoing", "outgoing | incoming")
	joinCmd.Flags().StringVar(&joinProfile, "profile", "", "Ogham profile (default: active profile)")
	rootCmd.AddCommand(joinCmd)
}
