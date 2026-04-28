// Package native shared types for the v0.13 wiki-recall verbs.
//
// The four ports (query_topic_summary, walk_knowledge, lint_wiki read-only,
// advance_lifecycle) are SQL-only verbs absorbed natively for sub-100ms
// cold-start recall on Lambda-style deployments. The synthesis verbs
// (compile_wiki, recompute_topic_summary, contradict_memory,
// reinforce_memory) stay Python-only because they call LLMs or run
// scheduled background work.
//
// Migration 033 (shipped on dev/public main pre-v0.13) added the
// `tldr_one_line` and `tldr_short` columns plus the 12-param
// `wiki_topic_upsert` signature. TopicSummary mirrors the post-033 row
// shape; pre-033 rows return with NULL TLDR fields and the read path
// transparently falls back to body.

package native

import (
	"fmt"
	"time"
)

// TLDRLevel selects the resolution of a topic summary read.
//
//   - LevelOneLine -- ~30-50 tokens, single sentence
//   - LevelShort   -- ~150-300 tokens, one paragraph
//   - LevelBody    -- ~1000 words, full markdown body (the v0.12 default)
//
// Mirrors the Python LevelType = Literal["one_line", "short", "body"]
// type alias used by `query_topic_summary(level=...)`.
type TLDRLevel string

const (
	LevelOneLine TLDRLevel = "one_line"
	LevelShort   TLDRLevel = "short"
	LevelBody    TLDRLevel = "body"
)

// ParseTLDRLevel coerces a user-supplied string to a TLDRLevel. Empty
// input maps to LevelBody to preserve the v0.12 caller contract --
// callers that don't pass `level` get the full body, same as before.
// Unknown values are an error rather than a silent fallback so a typo
// in a CLI flag doesn't quietly downgrade what got returned.
func ParseTLDRLevel(s string) (TLDRLevel, error) {
	switch s {
	case "", string(LevelBody):
		return LevelBody, nil
	case string(LevelShort):
		return LevelShort, nil
	case string(LevelOneLine):
		return LevelOneLine, nil
	default:
		return "", fmt.Errorf("invalid level %q: want one_line, short, or body", s)
	}
}

// TopicSummary is the post-033 topic_summaries row shape.
//
// TLDROneLine and TLDRShort are *string because they are NULLABLE on
// pre-033 rows and the read path needs to distinguish "explicitly empty"
// (string pointer to "") from "not populated yet" (nil).
//
// SourceHash is rendered to hex in the JSON tag because raw bytea is
// awkward over JSON; the Python `_format_summary_response` does the same.
type TopicSummary struct {
	ID           string    `json:"id"`
	TopicKey     string    `json:"topic_key"`
	ProfileID    string    `json:"profile"`
	Content      string    `json:"content"`
	TLDROneLine  *string   `json:"tldr_one_line,omitempty"`
	TLDRShort    *string   `json:"tldr_short,omitempty"`
	SourceCount  int       `json:"source_count"`
	SourceCursor *string   `json:"source_cursor,omitempty"`
	SourceHash   string    `json:"source_hash,omitempty"` // hex-encoded
	TokenCount   *int      `json:"token_count,omitempty"`
	Importance   float64   `json:"importance"`
	ModelUsed    string    `json:"model_used"`
	Version      int       `json:"version"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	StaleReason  *string   `json:"stale_reason,omitempty"`
}
