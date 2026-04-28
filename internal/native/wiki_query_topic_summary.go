// Package native: query_topic_summary -- cheap read-only fetch from
// topic_summaries. Mirrors src/ogham/tools/wiki.py::query_topic_summary
// and src/ogham/topic_summaries.py::get_summary_by_topic, plus the v0.13
// `level=` selector for multi-resolution recall.
//
// Calls the wiki_topic_get_by_key RPC (migration 031). No LLM, no
// recompute -- if the cache row is absent the result is `not_cached`.
//
// `level=` mapping:
//
//	body      -> Content (~1000 words, v0.12 default)
//	short     -> TLDRShort (~150-300 tokens)
//	one_line  -> TLDROneLine (~30-50 tokens)
//
// Pre-033 rows have NULL TLDR fields -- the read path falls back to body
// and signals via the response's `level` ("body") + `requested_level`
// (the original ask). This is the same contract Python's tool layer
// follows in v0.13 so callers see one shape across both clients.

package native

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryTopicSummaryStatus enumerates the response shapes.
const (
	StatusOK        = "ok"
	StatusNotCached = "not_cached"
)

// QueryTopicSummaryResult is the MCP-shaped response. Mirrors Python's
// _format_summary_response with the v0.13 `level` / `requested_level`
// pair appended.
//
// When Status == "not_cached" only TopicKey + Profile + Message are
// populated -- the rest are zero values.
type QueryTopicSummaryResult struct {
	Status         string `json:"status"`
	ID             string `json:"id,omitempty"`
	TopicKey       string `json:"topic_key"`
	Profile        string `json:"profile"`
	Version        int    `json:"version,omitempty"`
	StatusField    string `json:"summary_status,omitempty"` // fresh|stale|regenerating
	SourceCount    int    `json:"source_count,omitempty"`
	ModelUsed      string `json:"model_used,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	SourceHash     string `json:"source_hash,omitempty"`
	Level          string `json:"level,omitempty"`           // form actually returned
	RequestedLevel string `json:"requested_level,omitempty"` // what caller asked for (if differs from Level)
	Body           string `json:"body,omitempty"`            // selected text (one_line|short|body)
	Message        string `json:"message,omitempty"`         // populated for not_cached
}

// QueryTopicSummary fetches a cached summary for (profile, topic) and
// projects it down to the requested level. cfg.Profile is used when
// `profile` is empty.
func QueryTopicSummary(ctx context.Context, cfg *Config, profile, topic string, level TLDRLevel) (*QueryTopicSummaryResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native query_topic_summary: nil config")
	}
	if topic == "" {
		return nil, fmt.Errorf("native query_topic_summary: topic is required")
	}
	if profile == "" {
		profile = ActiveProfile(cfg)
	}
	if level == "" {
		level = LevelBody
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	var summary *TopicSummary
	switch backend {
	case "postgres":
		summary, err = queryTopicSummaryPostgres(ctx, cfg, profile, topic)
	case "supabase":
		summary, err = queryTopicSummarySupabase(ctx, cfg, profile, topic)
	default:
		return nil, fmt.Errorf("native query_topic_summary: unknown backend %q", backend)
	}
	if err != nil {
		return nil, err
	}

	if summary == nil {
		return &QueryTopicSummaryResult{
			Status:   StatusNotCached,
			TopicKey: topic,
			Profile:  profile,
			Message:  fmt.Sprintf("No cached summary for topic %q in profile %q. Use compile_wiki to synthesize.", topic, profile),
		}, nil
	}

	body, served := projectLevel(summary, level)
	res := &QueryTopicSummaryResult{
		Status:      StatusOK,
		ID:          summary.ID,
		TopicKey:    summary.TopicKey,
		Profile:     summary.ProfileID,
		Version:     summary.Version,
		StatusField: summary.Status,
		SourceCount: summary.SourceCount,
		ModelUsed:   summary.ModelUsed,
		UpdatedAt:   summary.UpdatedAt.UTC().Format(time.RFC3339Nano),
		SourceHash:  summary.SourceHash,
		Level:       string(served),
		Body:        body,
	}
	if served != level {
		res.RequestedLevel = string(level)
	}
	return res, nil
}

// projectLevel returns (body, level_actually_served). Falls back to body
// when the requested TLDR form is NULL (pre-033 row, or a row produced
// before the TLDR generator ran for this topic). Caller decides how to
// signal the fallback in the response shape.
func projectLevel(s *TopicSummary, requested TLDRLevel) (string, TLDRLevel) {
	switch requested {
	case LevelOneLine:
		if s.TLDROneLine != nil && *s.TLDROneLine != "" {
			return *s.TLDROneLine, LevelOneLine
		}
		// Fall back through short -> body so a partially-populated row
		// still gives the caller something cheaper than the full body
		// when a sibling form is present.
		if s.TLDRShort != nil && *s.TLDRShort != "" {
			return *s.TLDRShort, LevelShort
		}
		return s.Content, LevelBody
	case LevelShort:
		if s.TLDRShort != nil && *s.TLDRShort != "" {
			return *s.TLDRShort, LevelShort
		}
		return s.Content, LevelBody
	case LevelBody:
		return s.Content, LevelBody
	default:
		// Unreachable when ParseTLDRLevel was used; defensive.
		return s.Content, LevelBody
	}
}

func queryTopicSummaryPostgres(ctx context.Context, cfg *Config, profile, topic string) (*TopicSummary, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("query_topic_summary: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// wiki_topic_get_by_key returns SETOF topic_summaries (LIMIT 1). Pull
	// only the columns we expose -- skip embedding (we don't return it).
	row := conn.QueryRow(ctx, `
SELECT id::text, topic_key, profile_id, content,
       tldr_one_line, tldr_short,
       source_count, source_cursor::text, source_hash,
       token_count, importance, model_used,
       version, status, created_at, updated_at, stale_reason
  FROM wiki_topic_get_by_key($1, $2)`, profile, topic)

	var (
		s            TopicSummary
		sourceCursor *string
		sourceHash   []byte
	)
	err = row.Scan(
		&s.ID, &s.TopicKey, &s.ProfileID, &s.Content,
		&s.TLDROneLine, &s.TLDRShort,
		&s.SourceCount, &sourceCursor, &sourceHash,
		&s.TokenCount, &s.Importance, &s.ModelUsed,
		&s.Version, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.StaleReason,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query_topic_summary: scan: %w", err)
	}
	s.SourceCursor = sourceCursor
	if len(sourceHash) > 0 {
		s.SourceHash = hex.EncodeToString(sourceHash)
	}
	return &s, nil
}

func queryTopicSummarySupabase(ctx context.Context, cfg *Config, profile, topic string) (*TopicSummary, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"p_profile":   profile,
		"p_topic_key": topic,
	}
	raw, err := client.callRPC(ctx, "wiki_topic_get_by_key", args)
	if err != nil {
		return nil, err
	}

	// SETOF returns a JSON array; empty -> not cached.
	var rows []supabaseTopicRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("query_topic_summary: parse RPC response: %w (body: %s)", err, truncateForError(raw))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	s := rows[0].toTopicSummary()
	return &s, nil
}

// supabaseTopicRow is the JSON shape PostgREST returns for a row of
// topic_summaries. Bytea is delivered as `\xDEADBEEF`; we trim the prefix
// to mirror Python's _format_summary_response.
type supabaseTopicRow struct {
	ID           string    `json:"id"`
	TopicKey     string    `json:"topic_key"`
	ProfileID    string    `json:"profile_id"`
	Content      string    `json:"content"`
	TLDROneLine  *string   `json:"tldr_one_line"`
	TLDRShort    *string   `json:"tldr_short"`
	SourceCount  int       `json:"source_count"`
	SourceCursor *string   `json:"source_cursor"`
	SourceHash   *string   `json:"source_hash"` // "\xDEADBEEF"
	TokenCount   *int      `json:"token_count"`
	Importance   float64   `json:"importance"`
	ModelUsed    string    `json:"model_used"`
	Version      int       `json:"version"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	StaleReason  *string   `json:"stale_reason"`
}

func (r supabaseTopicRow) toTopicSummary() TopicSummary {
	hash := ""
	if r.SourceHash != nil {
		// PostgREST bytea: "\xDEADBEEF" (the leading backslash + x is the
		// pg-text-format prefix; the rest is hex). Drop the prefix and
		// validate the remainder is parseable.
		raw := *r.SourceHash
		if len(raw) > 2 && raw[0] == '\\' && raw[1] == 'x' {
			raw = raw[2:]
		}
		if _, err := hex.DecodeString(raw); err == nil {
			hash = raw
		}
	}
	return TopicSummary{
		ID:           r.ID,
		TopicKey:     r.TopicKey,
		ProfileID:    r.ProfileID,
		Content:      r.Content,
		TLDROneLine:  r.TLDROneLine,
		TLDRShort:    r.TLDRShort,
		SourceCount:  r.SourceCount,
		SourceCursor: r.SourceCursor,
		SourceHash:   hash,
		TokenCount:   r.TokenCount,
		Importance:   r.Importance,
		ModelUsed:    r.ModelUsed,
		Version:      r.Version,
		Status:       r.Status,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		StaleReason:  r.StaleReason,
	}
}
