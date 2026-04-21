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

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

var (
	storeTags            string
	storeSource          string
	storeProfile         string
	storeNativePreview   bool
	storeNativeDryRun    bool
	// Deprecated no-ops. --native / --json pre-dated v0.5 Day 4. Accepted
	// for backwards-compat but the v0.5 native path is opt-in via the new
	// --native-store-preview flag; the default remains the Python sidecar
	// until Day 6 flips it.
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

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		// v0.5 Day 4 opt-in native store path. Default still goes
		// through the sidecar so existing integrations don't change
		// behaviour until Day 6 promotes this path to the default.
		if storeNativePreview {
			return runNativeStorePreview(ctx, content)
		}

		// Native store isn't the default yet; the sidecar owns the
		// contradiction/compression/supersedes pipeline. Surface that
		// to the user unless they explicitly asked for --legacy or --quiet.
		noteSidecarFallback("store")

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

// runNativeStorePreview runs the v0.5 Day 4 native store orchestrator
// for the given content. Emits the StoreResult as JSON unless --dry-run
// was passed, in which case it renders a short human summary showing
// what the pipeline extracted.
func runNativeStorePreview(ctx context.Context, content string) error {
	cfg, err := loadNativeConfig()
	if err != nil {
		return err
	}

	profile := storeProfile
	if profile == "" {
		profile = cfg.Profile
	}

	res, err := native.Store(ctx, cfg, content, native.StoreOptions{
		Tags:    splitCSV(storeTags),
		Source:  storeSource,
		Profile: profile,
		DryRun:  storeNativeDryRun,
	})
	if err != nil {
		return fmt.Errorf("native store: %w", err)
	}

	if !useText() {
		return emitJSON(res)
	}

	if res.DryRun {
		fmt.Printf("[dry-run] profile=%s importance=%.3f surprise=%.3f elapsed=%s\n",
			res.Profile, res.Importance, res.Surprise, res.Elapsed)
	} else {
		fmt.Printf("Stored id=%s profile=%s importance=%.3f surprise=%.3f elapsed=%s\n",
			res.ID, res.Profile, res.Importance, res.Surprise, res.Elapsed)
	}
	if len(res.Tags) > 0 {
		fmt.Printf("  tags: %s\n", strings.Join(res.Tags, ", "))
	}
	if len(res.LinkedTo) > 0 {
		fmt.Printf("  %d auto-link candidate(s) (writes deferred):\n", len(res.LinkedTo))
		for _, l := range res.LinkedTo {
			fmt.Printf("    %s  sim=%.3f\n", l.ID, l.Similarity)
		}
	}
	return nil
}

// loadNativeConfig resolves the active profile's config for the native
// store path. Kept adjacent to the command so it's easy to swap out
// when Day 6 turns native-store-preview into the default.
func loadNativeConfig() (*native.Config, error) {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func init() {
	storeCmd.Flags().StringVar(&storeTags, "tags", "", "Comma-separated tags (e.g. type:decision,project:foo)")
	storeCmd.Flags().StringVar(&storeSource, "source", "", "Source label (e.g. claude-code)")
	storeCmd.Flags().StringVar(&storeProfile, "profile", "", "Profile to store in (defaults to config)")
	storeCmd.Flags().BoolVar(&storeNativePreview, "native-store-preview", false,
		"[preview] Run the native Go store pipeline (extraction, embed, surprise) "+
			"instead of shelling to the Python sidecar. Day 6 flips this to the default.")
	storeCmd.Flags().BoolVar(&storeNativeDryRun, "dry-run", false,
		"With --native-store-preview, run the pipeline but skip the DB write")
	storeCmd.Flags().BoolVar(&storeJSONDeprecated, "json", false, "")
	storeCmd.Flags().BoolVar(&storeNativeDeprecated, "native", false, "")
	_ = storeCmd.Flags().MarkHidden("json")
	_ = storeCmd.Flags().MarkHidden("native")
	rootCmd.AddCommand(storeCmd)
}
