package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	importProfile string
	importDedup   float64
)

var importCmd = &cobra.Command{
	Use:   "import <file.json>",
	Short: "Bulk-import memories from an export file via the sidecar",
	Long: `Reads an 'ogham export' JSON file (or stdin if the path is '-') and
calls the Python MCP sidecar's import_memories_tool. Sidecar-only
because bulk import exercises the entity extractor + auto-link path
that is not yet native.

Dedup runs server-side with the configured similarity threshold --
pass 0 to disable or a value between 0 and 1 to override.

Accepts either the raw export payload (a JSON object with a top-level
"memories" array) or the wrapped envelope produced by 'ogham export'
(a JSON object whose "data" field holds the inner JSON as a string).
The wrapper is unwrapped automatically so 'ogham export | ogham import'
round-trips.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		noteSidecarFallback("import")

		path := args[0]
		var raw []byte
		if path == "-" {
			buf, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			raw = buf
		} else {
			buf, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			raw = buf
		}

		payload, err := unwrapImportPayload(raw)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 600*time.Second)
		defer cancelT()

		client, err := connectSidecarWithProfile(ctx, importProfile)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		// import_memories_tool(data: str, dedup_threshold: float). The
		// server takes the profile from its active-profile resolver, which
		// honours OGHAM_PROFILE -- already set by connectSidecarWithProfile
		// when --profile is passed.
		toolArgs := map[string]any{
			"data":            payload,
			"dedup_threshold": importDedup,
		}

		result, err := client.CallTool(ctx, "import_memories_tool", toolArgs)
		if err != nil {
			return fmt.Errorf("import_memories_tool: %w", err)
		}
		unwrapped, err := toolResultJSON(result)
		if err != nil {
			return err
		}
		if useText() {
			if m, ok := unwrapped.(map[string]any); ok {
				imported, _ := m["imported"].(float64)
				skipped, _ := m["skipped"].(float64)
				fmt.Printf("imported %d, skipped %d (dedup)\n", int(imported), int(skipped))
				return nil
			}
			fmt.Println("import complete")
			return nil
		}
		return emitJSON(unwrapped)
	},
}

// unwrapImportPayload returns the JSON string the sidecar's
// import_memories_tool expects.
//
// The sidecar's tool signature is import_memories_tool(data: str, ...) --
// data must be a JSON-encoded string. There are two on-disk shapes:
//
//  1. A bare export object: {"profile": ..., "memories": [...]}.
//  2. The wrapped envelope `ogham export` writes:
//     {"status": "exported", "profile": ..., "format": "json", "data": "<json-string>"}.
//
// Shape 2 happens because export_profile() returns the inner JSON as a
// string field on its own result envelope; the CLI writes the full
// envelope to disk. Importing that file should round-trip, so unwrap it
// when present.
func unwrapImportPayload(raw []byte) (string, error) {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("not valid JSON: %w", err)
	}
	if data, ok := probe["data"].(string); ok && data != "" {
		// Wrapped envelope: the inner JSON is already a string.
		return data, nil
	}
	// Bare export object: re-emit canonical JSON so we always send a
	// well-formed string regardless of the file's whitespace.
	out, err := json.Marshal(probe)
	if err != nil {
		return "", fmt.Errorf("re-encode payload: %w", err)
	}
	return string(out), nil
}

func init() {
	importCmd.Flags().StringVar(&importProfile, "profile", "", "Profile to import into (defaults to active)")
	importCmd.Flags().Float64Var(&importDedup, "dedup", 0.8, "Dedup threshold (0 to disable, 0-1 otherwise)")
	rootCmd.AddCommand(importCmd)
}
