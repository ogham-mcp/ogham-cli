package cmd

import (
	"context"
	"encoding/json"
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

// inscribe is the explicit commit primitive that replaces the legacy
// PreCompact hook (#11). The hook wrote a metadata-only stub on every
// compaction event (session_id / cwd / timestamp -- no transcript
// content), which dilutes recall at scale. The verb takes prepared,
// caller-distilled content and stores it; the caller (orchestrator,
// skill, scribe, future plugin) owns the signal gating and any LLM
// distillation. ogham-cli stays a fast, reliable commit target.
//
// Composes with the superpowers-memory bridge spec §4.3: signal-gated
// capture -> staged JSONL buffer -> distilled flush, where the flush
// step targets exactly this verb.
var (
	inscribeFile           string
	inscribeStdin          bool
	inscribeTranscriptPath string
	inscribeProfile        string
	inscribeTags           string
	inscribeSummary        string
	inscribeSource         string
	inscribeDryRun         bool
)

var inscribeCmd = &cobra.Command{
	Use:   "inscribe [content]",
	Short: "Commit prepared content as a memory (explicit verb; replaces the legacy PreCompact hook)",
	Long: `Inscribe prepared content as a memory in the active profile. This
is the durable commit primitive that pre-distilled-content pipelines
(orchestrators, skills, transcript scribes, future plugins) target.

Content sources (mutually exclusive):
  - Positional args (joined by spaces) -- e.g. ` + "`ogham inscribe \"prepared note\"`" + `
  - --file PATH                       -- read content from a file
  - --stdin                           -- read content from stdin explicitly
  - --transcript-path PATH            -- read a Claude Code PreCompact JSONL
                                         transcript and inscribe the concatenated
                                         user+assistant turns RAW (no LLM
                                         distillation; the caller decides
                                         whether to distill before inscribing)

When no source is given and stdin is a pipe, content is read from stdin
(matches the ` + "`ogham store`" + ` convenience).

Why a verb, not a hook (#11): the legacy PreCompact hook wrote a
metadata-only stub (session_id / cwd / timestamp) on every compaction.
That dilutes recall at scale because every search has to sift through
dozens of metadata stubs that say nothing about what actually happened.
ogham-cli should not grow LLM distillation inside the sub-100ms Go
binary (the claude-mem comparison in the v0.8 release notes spells out
why). The verb separates ` + "`commit`" + ` from ` + "`distill`" + `: the caller distills,
ogham inscribes.

Flags:
  --file PATH             Read content from a file
  --stdin                 Read content from stdin explicitly
  --transcript-path PATH  Read a Claude Code transcript JSONL, concatenate
                          user+assistant turns raw
  --profile NAME          Profile to inscribe into (default: active profile)
  --tags KEY=VAL[,...]    Comma-separated tags
  --summary "..."         Caller-prepared one-line summary (advisory)
  --source LABEL          Source label (default: "inscribe")
  --dry-run               Show what would be stored, write nothing`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		content, err := resolveInscribeContent(args)
		if err != nil {
			return err
		}
		content = strings.TrimSpace(content)
		if content == "" {
			return fmt.Errorf("inscribe: empty content after resolution; pass --file, --stdin, --transcript-path, or a positional arg")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		cfg, err := loadNativeConfig()
		if err != nil {
			return err
		}

		profile := inscribeProfile
		if profile == "" {
			profile = cfg.Profile
		}

		source := inscribeSource
		if source == "" {
			source = "inscribe"
		}

		tags := splitCSV(inscribeTags)
		// Mark inscribed content so downstream search/maintenance can
		// distinguish from interactive store_memory calls.
		tags = append(tags, "type:inscribed")
		if inscribeSummary != "" {
			tags = append(tags, "has:summary")
		}

		res, err := native.Store(ctx, cfg, content, native.StoreOptions{
			Tags:    tags,
			Source:  source,
			Profile: profile,
			DryRun:  inscribeDryRun,
		})
		if err != nil {
			return fmt.Errorf("inscribe: %w", err)
		}

		if !useText() {
			return emitJSON(res)
		}
		if res.DryRun {
			fmt.Printf("[dry-run inscribe] profile=%s importance=%.3f surprise=%.3f bytes=%d elapsed=%s\n",
				res.Profile, res.Importance, res.Surprise, len(content), res.Elapsed)
		} else {
			fmt.Printf("Inscribed id=%s profile=%s importance=%.3f surprise=%.3f bytes=%d elapsed=%s\n",
				res.ID, res.Profile, res.Importance, res.Surprise, len(content), res.Elapsed)
		}
		if len(res.Tags) > 0 {
			fmt.Printf("  tags: %s\n", strings.Join(res.Tags, ", "))
		}
		return nil
	},
}

// resolveInscribeContent pulls content from whichever source the caller
// configured. Exactly one of the mutually-exclusive sources may be set;
// positional args win when present (matches `ogham store` convention).
func resolveInscribeContent(args []string) (string, error) {
	sourcesSet := 0
	if len(args) > 0 {
		sourcesSet++
	}
	if inscribeFile != "" {
		sourcesSet++
	}
	if inscribeStdin {
		sourcesSet++
	}
	if inscribeTranscriptPath != "" {
		sourcesSet++
	}
	if sourcesSet > 1 {
		return "", fmt.Errorf("inscribe: --file, --stdin, --transcript-path, and positional args are mutually exclusive (pick one)")
	}

	switch {
	case len(args) > 0:
		return strings.Join(args, " "), nil
	case inscribeFile != "":
		data, err := os.ReadFile(inscribeFile) // #nosec G304 -- user-supplied path is the intent of --file
		if err != nil {
			return "", fmt.Errorf("read --file %s: %w", inscribeFile, err)
		}
		return string(data), nil
	case inscribeTranscriptPath != "":
		return readClaudeCodeTranscript(inscribeTranscriptPath)
	case inscribeStdin:
		return readStdinString()
	default:
		// Auto-detect: stdin is a pipe -> read it. Else error.
		info, _ := os.Stdin.Stat()
		if info != nil && (info.Mode()&os.ModeCharDevice) == 0 {
			return readStdinString()
		}
		return "", fmt.Errorf("inscribe: no content source provided (use --file, --stdin, --transcript-path, or pipe to stdin)")
	}
}

func readStdinString() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

// readClaudeCodeTranscript reads a Claude Code PreCompact transcript
// JSONL and concatenates the user/assistant message text into a single
// content blob. No LLM distillation -- callers that want a distilled
// summary should pipe content through their own LLM before calling
// `ogham inscribe --stdin`. This convenience exists for the simplest
// "just inscribe what was said" workflow.
//
// Format follows Claude Code's transcript JSONL (one JSON object per
// line, each with a `type` discriminator and a `message` payload). The
// reader extracts `message.role` + the textual portion of
// `message.content`. Unknown shapes are skipped silently rather than
// failing the whole inscribe -- partial content beats no content.
func readClaudeCodeTranscript(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- user-supplied path is the intent of --transcript-path
	if err != nil {
		return "", fmt.Errorf("read --transcript-path %s: %w", path, err)
	}
	var out strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed lines
		}
		msg, ok := entry["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		text := extractTranscriptText(msg["content"])
		if text == "" {
			continue
		}
		fmt.Fprintf(&out, "## %s\n\n%s\n\n", role, text)
	}
	return out.String(), nil
}

// extractTranscriptText walks Claude Code's `message.content` field,
// which can be either a string (simple message) or a slice of typed
// blocks (`[{"type":"text","text":"..."}, ...]`). Returns the textual
// portion concatenated. Tool calls / tool results / image blocks are
// skipped -- only natural-language content survives.
func extractTranscriptText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if blockType, _ := block["type"].(string); blockType != "text" {
				continue
			}
			if t, ok := block["text"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func init() {
	inscribeCmd.Flags().StringVar(&inscribeFile, "file", "", "Read content from a file")
	inscribeCmd.Flags().BoolVar(&inscribeStdin, "stdin", false, "Read content from stdin explicitly")
	inscribeCmd.Flags().StringVar(&inscribeTranscriptPath, "transcript-path", "", "Read a Claude Code PreCompact transcript JSONL and inscribe user+assistant turns raw")
	inscribeCmd.Flags().StringVar(&inscribeProfile, "profile", "", "Profile to inscribe into (defaults to active)")
	inscribeCmd.Flags().StringVar(&inscribeTags, "tags", "", "Comma-separated tags (e.g. type:decision,project:foo)")
	inscribeCmd.Flags().StringVar(&inscribeSummary, "summary", "", "Caller-prepared one-line summary (advisory; appended as tag flag has:summary)")
	inscribeCmd.Flags().StringVar(&inscribeSource, "source", "", "Source label (default: inscribe)")
	inscribeCmd.Flags().BoolVar(&inscribeDryRun, "dry-run", false, "Show what would be stored, write nothing")
	rootCmd.AddCommand(inscribeCmd)
}
