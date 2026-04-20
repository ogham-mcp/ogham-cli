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
pass 0 to disable or a value between 0 and 1 to override.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		noteSidecarFallback("import")

		path := args[0]
		var data []byte
		if path == "-" {
			buf, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			data = buf
		} else {
			buf, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			data = buf
		}

		var payload any
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("parse %s as JSON: %w", path, err)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 600*time.Second)
		defer cancelT()

		client, err := connectSidecar(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		toolArgs := map[string]any{
			"data":             payload,
			"dedup_threshold":  importDedup,
		}
		if importProfile != "" {
			toolArgs["profile"] = importProfile
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

func init() {
	importCmd.Flags().StringVar(&importProfile, "profile", "", "Profile to import into (defaults to active)")
	importCmd.Flags().Float64Var(&importDedup, "dedup", 0.8, "Dedup threshold (0 to disable, 0-1 otherwise)")
	rootCmd.AddCommand(importCmd)
}
