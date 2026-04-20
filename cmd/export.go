package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	exportProfile string
	exportFormat  string
	exportOutput  string
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export memories from a profile (JSON or Markdown)",
	Long: `Exports every memory in the active (or specified) profile via the
Python MCP sidecar's export_profile tool. Sidecar-only for now because
the Python path handles pagination and Markdown rendering; a native
port lives behind the same blocker as import (entity extractor).

Output goes to stdout by default; pass --output path/to/file.json to
write directly to disk instead. JSON is the default format (per rc4
UX) -- pass --format markdown for the human-readable variant.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Sidecar-only; notify the user unless they opted out.
		noteSidecarFallback("export")

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 120*time.Second)
		defer cancelT()

		client, err := connectSidecar(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		toolArgs := map[string]any{
			"format": exportFormat,
		}
		if exportProfile != "" {
			toolArgs["profile"] = exportProfile
		}

		result, err := client.CallTool(ctx, "export_profile", toolArgs)
		if err != nil {
			return fmt.Errorf("export_profile: %w", err)
		}
		payload, err := toolResultJSON(result)
		if err != nil {
			return err
		}

		out := os.Stdout
		if exportOutput != "" {
			f, err := os.OpenFile(exportOutput, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return fmt.Errorf("open %s: %w", exportOutput, err)
			}
			defer func() { _ = f.Close() }()
			out = f
		}

		// Markdown comes back as a string; JSON comes back as a structured
		// value. Honour the user's format request regardless of shape.
		if exportFormat == "markdown" {
			if s, ok := payload.(string); ok {
				_, err := fmt.Fprintln(out, s)
				return err
			}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportProfile, "profile", "", "Profile to export (defaults to active)")
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "Output format: json or markdown")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Write to this file instead of stdout")
	rootCmd.AddCommand(exportCmd)
}
