package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ogham-mcp/ogham-cli/internal/agentzeroimport"
	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/spf13/cobra"
)

var (
	importAreas            string
	importIncludeKnowledge bool
	importDryRun           bool
)

var importAgentZeroCmd = &cobra.Command{
	Use:   "import-agent-zero <path>",
	Short: "Import memories from an Agent Zero FAISS pickle file",
	Long: `Reads an Agent Zero memory directory (or index.pkl file),
extracts memories via a safe Python unpickler, and uploads them
to the Ogham gateway in batches of 100.

Requires python3 in PATH.

Examples:
  ogham import-agent-zero /tmp/agent-zero-memory/default/
  ogham import-agent-zero /tmp/agent-zero-memory/default/index.pkl --dry-run
  ogham import-agent-zero ./memory --areas fragments,solutions`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pklPath := args[0]

		// If path is a directory, append index.pkl
		info, err := os.Stat(pklPath)
		if err != nil {
			return fmt.Errorf("path not found: %w", err)
		}
		if info.IsDir() {
			pklPath = filepath.Join(pklPath, "index.pkl")
		}

		// Check the pkl file exists
		if _, err := os.Stat(pklPath); err != nil {
			return fmt.Errorf("pickle file not found: %s", pklPath)
		}

		// Parse areas flag
		var areas []string
		if importAreas != "" {
			areas = strings.Split(importAreas, ",")
		}

		fmt.Fprintf(os.Stderr, "Parsing %s ...\n", pklPath)

		memories, err := agentzeroimport.ParsePickle(pklPath, areas, importIncludeKnowledge)
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Found %d memories\n", len(memories))

		if len(memories) == 0 {
			fmt.Fprintln(os.Stderr, "Nothing to import.")
			return nil
		}

		// Dry run: preview and exit
		if importDryRun {
			limit := 10
			if len(memories) < limit {
				limit = len(memories)
			}
			fmt.Fprintf(os.Stderr, "\n--- Preview (first %d of %d) ---\n", limit, len(memories))
			for i := 0; i < limit; i++ {
				m := memories[i]
				preview := m.Content
				if len(preview) > 120 {
					preview = preview[:120] + "..."
				}
				fmt.Fprintf(os.Stderr, "[%d] %s\n    tags: %s\n", i+1, preview, strings.Join(m.Tags, ", "))
			}
			fmt.Fprintf(os.Stderr, "\nDry run complete. Use without --dry-run to import.\n")
			return nil
		}

		// Load config for gateway access
		cfg, err := config.Load(config.DefaultPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.APIKey == "" {
			return fmt.Errorf("no API key configured. Run: ogham auth login")
		}

		ua := fmt.Sprintf("ogham-cli/%s", Version)
		client := gateway.New(cfg.GatewayURL, cfg.APIKey, ua)

		// Ctrl+C halts the upload loop mid-batch; the in-flight HTTP
		// call tears down cleanly via the gateway client's ctx-aware
		// retry path.
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Batch and upload
		batchSize := 100
		total := len(memories)
		imported := 0

		for i := 0; i < total; i += batchSize {
			end := i + batchSize
			if end > total {
				end = total
			}
			batch := memories[i:end]

			// Convert to []any for the gateway client
			payload := make([]any, len(batch))
			for j, m := range batch {
				payload[j] = m
			}

			fmt.Fprintf(os.Stderr, "Uploading batch %d-%d of %d ...\n", i+1, end, total)

			result, err := client.BulkImport(ctx, payload)
			if err != nil {
				return fmt.Errorf("batch %d-%d failed: %w", i+1, end, err)
			}

			if count, ok := result["imported"]; ok {
				if c, ok := count.(float64); ok {
					imported += int(c)
				}
			} else {
				imported += len(batch)
			}
		}

		fmt.Fprintf(os.Stderr, "✓ Imported %d memories to Ogham\n", imported)
		return nil
	},
}

func init() {
	importAgentZeroCmd.Flags().StringVar(&importAreas, "areas", "", "Comma-separated areas to import (default: all)")
	importAgentZeroCmd.Flags().BoolVar(&importIncludeKnowledge, "include-knowledge", false, "Include knowledge imports")
	importAgentZeroCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Show what would be imported without uploading")
	rootCmd.AddCommand(importAgentZeroCmd)
}
