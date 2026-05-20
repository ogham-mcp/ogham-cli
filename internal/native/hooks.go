package native

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// HookOptions controls the hook entry points (SessionStart, Recall,
// Inscribe). All fields are optional; zero values mean "use the
// default that mirrors the Python ogham.hooks implementation".
//
// Profile follows the standard precedence: explicit Profile here >
// cfg.Profile > "default".
//
// DryRun is honoured by Inscribe -- when true, the drain memory is
// composed and returned but NOT written to the store. SessionStart
// and Recall ignore DryRun (they're read-only).
type HookOptions struct {
	Profile string
	DryRun  bool
}

// SessionStart returns markdown context to inject at session start.
//
// Mirrors Python ogham.hooks.session_start: hybrid_search using the
// project name as the query, format the top results as a markdown
// bullet list under a "## Session Context" heading. Returns empty
// string when no results so the caller can treat that as "no
// context" without an error.
//
// Best-effort by design: a search failure returns ("", nil) rather
// than propagating the error. Claude Code's SessionStart hook should
// never block the editor on an Ogham outage.
func SessionStart(ctx context.Context, cfg *Config, cwd string, opts HookOptions) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("native hooks: nil config")
	}
	project := projectFromCwd(cwd)
	query := fmt.Sprintf("project context for %s", project)

	results, err := Search(ctx, cfg, query, SearchOptions{
		Limit:   8,
		Profile: opts.Profile,
	})
	if err != nil {
		// Best-effort: silently return no context. The hook is allowed
		// to fail; Claude Code keeps going either way.
		return "", nil
	}
	if len(results) == 0 {
		return "", nil
	}
	return formatRecallMarkdown(results, project, "Session Context", "loaded for", 200), nil
}

// Recall returns markdown context to restore after compaction.
//
// Mirrors Python ogham.hooks.post_compact: hybrid_search using a
// "recent work and decisions" query, limit 10, longer content
// truncation (300 vs 200) than SessionStart since post-compact
// users often need more detail to rebuild context. Same best-effort
// pattern as SessionStart -- never propagate search failures.
func Recall(ctx context.Context, cfg *Config, cwd string, opts HookOptions) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("native hooks: nil config")
	}
	project := projectFromCwd(cwd)
	query := fmt.Sprintf("recent work and decisions for %s", project)

	results, err := Search(ctx, cfg, query, SearchOptions{
		Limit:   10,
		Profile: opts.Profile,
	})
	if err != nil {
		return "", nil
	}
	if len(results) == 0 {
		return "", nil
	}
	return formatRecallMarkdown(results, project, "Restored Context", "restored for", 300), nil
}

// Inscribe drains session context to Ogham before compaction.
//
// Mirrors Python ogham.hooks.pre_compact: build a short plaintext
// summary identifying the session and store it with tags marking
// it as a compaction drain. Returns the content that was stored
// (or that would have been stored when DryRun is true) so the
// caller can echo it for verification.
//
// Inscribe is best-effort: a store failure returns the content + a
// wrapped error, so the caller can log without breaking the
// PreCompact hook flow that triggered it.
func Inscribe(ctx context.Context, cfg *Config, sessionID, cwd string, opts HookOptions) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("native hooks: nil config")
	}
	if sessionID == "" {
		sessionID = "unknown"
	}
	project := projectFromCwd(cwd)
	timestamp := time.Now().UTC().Format(time.RFC3339)

	content := fmt.Sprintf(
		"Session drain before compaction.\nProject: %s\nDirectory: %s\nSession: %s\nTime: %s",
		project, cwd, sessionID, timestamp,
	)

	if opts.DryRun {
		return content, nil
	}

	_, err := Store(ctx, cfg, content, StoreOptions{
		Profile: opts.Profile,
		Source:  "hook:pre-compact",
		Tags: []string{
			"type:session",
			fmt.Sprintf("session:%s", sessionID),
			"compaction:drain",
		},
	})
	if err != nil {
		// Match Python's best-effort behaviour: return the content the
		// caller intended to store, plus a wrapped error so callers
		// who care (tests, CI) can see what failed.
		return content, fmt.Errorf("inscribe: store failed: %w", err)
	}
	return content, nil
}

// projectFromCwd extracts a short project name from a working
// directory path. "." or "" fall back to "current project" so the
// markdown query reads sensibly.
func projectFromCwd(cwd string) string {
	if cwd == "" || cwd == "." {
		return "current project"
	}
	base := filepath.Base(cwd)
	if base == "." || base == "/" || base == "" {
		return "current project"
	}
	return base
}

// formatRecallMarkdown renders search results as the markdown shape
// Claude Code hooks emit to stdout. The Python session_start uses
// "Session Context" / 200-char truncation; post_compact uses
// "Restored Context" / 300-char truncation. Both share this format.
func formatRecallMarkdown(
	results []SearchResult,
	project, heading, verb string,
	truncate int,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", heading)
	for _, r := range results {
		content := r.Content
		if truncate > 0 && len(content) > truncate {
			content = content[:truncate]
		}
		tags := typeTagsFromResult(r)
		if len(tags) > 0 {
			fmt.Fprintf(&b, "- %s (%s)\n", content, strings.Join(tags, ", "))
		} else {
			fmt.Fprintf(&b, "- %s\n", content)
		}
	}
	fmt.Fprintf(&b, "\n*%d memories %s %s*\n", len(results), verb, project)
	return b.String()
}

// typeTagsFromResult returns the subset of tags worth surfacing in
// the hook output bullet. Mirrors Python ogham.hooks._type_tags --
// filters to tags starting with "type:" or "source:" so the bullet
// stays compact without dumping the full tag list.
func typeTagsFromResult(r SearchResult) []string {
	var out []string
	for _, t := range r.Tags {
		if strings.HasPrefix(t, "type:") || strings.HasPrefix(t, "source:") {
			out = append(out, t)
		}
	}
	return out
}
