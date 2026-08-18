package cmd

import (
	"context"
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

--profile is honoured by spawning the sidecar with OGHAM_PROFILE set
for the duration of this command; the user's active profile sentinel
file is untouched.`,
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
			// #nosec G304 -- path is the import file the user named on the
			// command line; reading it is what import does.
			buf, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			data = buf
		}

		toolArgs, err := buildImportToolArgs(data, importDedup)
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
