// Package cmd: `ogham triple` -- store a typed (subject, predicate, object)
// edge in the entity graph, with write-time supersession. Native-first
// (direct Postgres via pgx); falls back to the Python sidecar's
// store_triple MCP tool with --sidecar.

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

var (
	tripleProfile  string
	tripleFactID   string
	tripleMetadata string
)

var tripleCmd = &cobra.Command{
	Use:   "triple <subject> <predicate> <object>",
	Short: "Store a typed (subject, predicate, object) edge in the entity graph",
	Long: `Store a typed edge in the entity graph, with write-time supersession:
any current edge for the same (subject, predicate, object, profile) has its
valid_to stamped and superseded_by set to the new edge's id.

subject/object may be a canonical entity name or an alias. predicate must
be one of the v1 vocabulary (DEPENDS_ON, DEPENDED_ON_BY, OWNS, OWNED_BY,
ASSIGNED_TO, HAS_ASSIGNEE, DECIDED, MENTIONS, BLOCKS, BLOCKED_BY, PART_OF,
CONTAINS, SUPPORTS, CONTRADICTS, EVOLVED_INTO, RELATED_TO).

Native-only against the postgres backend today -- use --sidecar to route
through the Python MCP server's store_triple tool instead.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		if useSidecar() {
			return runTripleSidecar(ctx, args)
		}
		return runTripleNative(ctx, args)
	},
}

// runTripleNative runs StoreTriple against the configured postgres
// backend directly. Emits StoreTripleResult as JSON (default) or a terse
// human summary with --text.
func runTripleNative(ctx context.Context, args []string) error {
	cfg, err := loadNativeConfig()
	if err != nil {
		return err
	}

	var metadata map[string]any
	if tripleMetadata != "" {
		if err := json.Unmarshal([]byte(tripleMetadata), &metadata); err != nil {
			return fmt.Errorf("--metadata: invalid JSON: %w", err)
		}
	}

	profile := tripleProfile
	if profile == "" {
		profile = native.ActiveProfile(cfg)
	}

	res, err := native.StoreTriple(ctx, cfg, native.StoreTripleOptions{
		Subject:        args[0],
		Predicate:      args[1],
		Object:         args[2],
		Profile:        profile,
		SourceMemoryID: tripleFactID,
		Metadata:       metadata,
	})
	if err != nil {
		return fmt.Errorf("native triple: %w", err)
	}

	if !useText() {
		return emitJSON(res)
	}
	fmt.Printf("edge_id=%d\n", res.EdgeID)
	return nil
}

// runTripleSidecar is the --sidecar path. Passes the same argument shape
// the Python store_triple MCP tool expects; let Python respond if it
// lacks the verb (that IS the fallback -- we don't fabricate a contract).
func runTripleSidecar(ctx context.Context, args []string) error {
	noteSidecarFallback("triple")

	client, err := connectSidecar(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	toolArgs := map[string]any{
		"subject":   args[0],
		"predicate": args[1],
		"object_":   args[2],
	}
	if tripleProfile != "" {
		toolArgs["profile"] = tripleProfile
	}
	if tripleFactID != "" {
		toolArgs["source_memory_id"] = tripleFactID
	}
	if tripleMetadata != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(tripleMetadata), &metadata); err != nil {
			return fmt.Errorf("--metadata: invalid JSON: %w", err)
		}
		toolArgs["metadata"] = metadata
	}

	result, err := client.CallTool(ctx, "store_triple", toolArgs)
	if err != nil {
		return fmt.Errorf("store_triple: %w", err)
	}

	payload, err := toolResultJSON(result)
	if err != nil {
		return err
	}
	if !useText() {
		return emitJSON(payload)
	}

	if m, ok := payload.(map[string]any); ok {
		if id, ok := m["edge_id"]; ok {
			fmt.Printf("edge_id=%v\n", id)
			return nil
		}
	}
	fmt.Println("Stored.")
	return nil
}

func init() {
	tripleCmd.Flags().StringVar(&tripleProfile, "profile", "", "Ogham profile (default: active profile)")
	tripleCmd.Flags().StringVar(&tripleFactID, "fact-id", "", "UUID of the memory that produced this claim")
	tripleCmd.Flags().StringVar(&tripleMetadata, "metadata", "", "JSON dict, free-form (default: {})")
	rootCmd.AddCommand(tripleCmd)
}
