package cmd

import (
	"context"
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
UX) -- pass --format markdown for the human-readable variant, or
--format okf for an Open Knowledge Format v0.1 bundle directory.

The written file is the raw export payload, not the MCP envelope, so
'ogham export -o backup.json && ogham import backup.json' round-trips
cleanly.

For --format okf, the sidecar writes a bundle DIRECTORY to its current
working directory and returns the path. We print that path. The -o
flag is honoured for the path string (useful for scripting), but the
bundle itself lives where the sidecar put it. Use 'ogham import <path>'
to round-trip.

--profile is honoured by spawning the sidecar with OGHAM_PROFILE set
for the duration of this command; the user's active profile sentinel
file is untouched.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Sidecar-only; notify the user unless they opted out.
		noteSidecarFallback("export")

		if exportFormat != "json" && exportFormat != "markdown" && exportFormat != "okf" {
			return fmt.Errorf("--format must be 'json', 'markdown', or 'okf', got %q", exportFormat)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 120*time.Second)
		defer cancelT()

		client, err := connectSidecarWithProfile(ctx, exportProfile)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		result, err := client.CallTool(ctx, "export_profile", map[string]any{
			"format": exportFormat,
		})
		if err != nil {
			return fmt.Errorf("export_profile: %w", err)
		}
		payload, err := toolResultJSON(result)
		if err != nil {
			return err
		}

		body, err := unwrapExportPayload(payload)
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

		_, err = fmt.Fprintln(out, body)
		return err
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportProfile, "profile", "", "Profile to export (defaults to active)")
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "Output format: json, markdown, or okf (bundle directory)")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Write to this file instead of stdout")
	rootCmd.AddCommand(exportCmd)
}
