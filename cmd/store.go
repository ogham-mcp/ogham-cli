package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	storeTags    string
	storeSource  string
	storeProfile string
	// Deprecated no-ops. store has no native implementation yet (entity
	// extractor port still pending), so every call routes through the
	// sidecar regardless. --legacy is accepted but the sidecar IS the
	// only path available.
	storeJSONDeprecated   bool
	storeNativeDeprecated bool
)

var storeCmd = &cobra.Command{
	Use:   "store [content]",
	Short: "Store a memory in the active profile",
	Long: `Store a memory via the Python MCP sidecar.

Content source:
  - positional args (joined by spaces), or
  - piped stdin if no positional arg is given --
      e.g. 'git diff | ogham store --source git-diff --tags review'
      e.g. 'echo "meeting notes..." | ogham store --tags type:notes'

Native store is not yet implemented. Store requires entity extraction
(Python owns the 18-language extractor) and is expected to be the last
tool absorbed.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve content: positional wins, fall back to stdin when piped.
		var content string
		if len(args) > 0 {
			content = strings.TrimSpace(strings.Join(args, " "))
		} else {
			info, _ := os.Stdin.Stat()
			if info != nil && (info.Mode()&os.ModeCharDevice) != 0 {
				// Stdin is a TTY -- no pipe, user forgot to pass content.
				return fmt.Errorf("no content provided: pass as argument or pipe to stdin\n  ogham store \"your content\"\n  echo \"your content\" | ogham store")
			}
			buf, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			content = strings.TrimSpace(string(buf))
			if content == "" {
				return fmt.Errorf("stdin was empty; nothing to store")
			}
		}

		// Native store isn't implemented yet (blocked on entity-extractor
		// port). We always go through the sidecar. Surface that to the
		// user unless they explicitly asked for --legacy or --quiet.
		noteSidecarFallback("store")

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		client, err := connectSidecar(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		toolArgs := map[string]any{
			"content": content,
		}
		if tags := splitCSV(storeTags); tags != nil {
			toolArgs["tags"] = tags
		}
		if storeSource != "" {
			toolArgs["source"] = storeSource
		}
		if storeProfile != "" {
			toolArgs["profile"] = storeProfile
		}

		result, err := client.CallTool(ctx, "store_memory", toolArgs)
		if err != nil {
			return fmt.Errorf("store_memory: %w", err)
		}

		payload, err := toolResultJSON(result)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(payload)
		}

		if m, ok := payload.(map[string]any); ok {
			id, _ := m["id"].(string)
			status, _ := m["status"].(string)
			switch {
			case id != "":
				fmt.Printf("Stored id=%s\n", id)
			case status != "":
				fmt.Printf("Stored status=%s\n", status)
			default:
				fmt.Println("Stored.")
			}
			return nil
		}
		fmt.Println("Stored.")
		return nil
	},
}

func init() {
	storeCmd.Flags().StringVar(&storeTags, "tags", "", "Comma-separated tags (e.g. type:decision,project:foo)")
	storeCmd.Flags().StringVar(&storeSource, "source", "", "Source label (e.g. claude-code)")
	storeCmd.Flags().StringVar(&storeProfile, "profile", "", "Profile to store in (defaults to config)")
	storeCmd.Flags().BoolVar(&storeJSONDeprecated, "json", false, "")
	storeCmd.Flags().BoolVar(&storeNativeDeprecated, "native", false, "")
	_ = storeCmd.Flags().MarkHidden("json")
	_ = storeCmd.Flags().MarkHidden("native")
	rootCmd.AddCommand(storeCmd)
}
