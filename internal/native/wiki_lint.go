// Package native: lint_wiki -- READ-ONLY health report.
//
// Mirrors src/ogham/wiki_lint.py::lint_report and the wiki_lint_*
// functions from migration 031. SELECT-only -- the Python writeback
// path (sweeping stale, recompute scheduling) stays Python-owned. This
// is the recall fast path: a Lambda thin client can ask "is the wiki
// layer healthy?" in O(few-queries) without touching an LLM.
//
// Categories surfaced:
//
//   - contradictions    pairs joined by relationship='contradicts'
//   - orphans           memories with no edges (5-min grace window)
//   - stale_lifecycle   memories stuck in stage='stable' past N days
//   - stale_summaries   topic_summaries in status='stale'
//   - summary_drift     fresh summaries whose stored source_hash no
//                       longer matches current sources (skipped if
//                       includeDrift=false; the per-topic re-hash loop
//                       can be slow on huge profiles)

package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// LintDefaults mirror Python's wiki_lint module-level defaults.
const (
	LintDefaultSampleSize         = 10
	LintDefaultStableDays         = 90
	LintDefaultOrphanGraceMinutes = 5
)

// LintCategory is the count + sample shape every category returns. Kept
// generic so the JSON output mirrors the Python report structure.
type LintCategory struct {
	Count  int              `json:"count"`
	Sample []map[string]any `json:"sample"`

	// OlderThanDays is non-zero only for stale_lifecycle (Python
	// includes this in its response so callers can see the threshold
	// they queried at).
	OlderThanDays int `json:"older_than_days,omitempty"`

	// Skipped is set on summary_drift when includeDrift=false so the
	// caller can tell "0 drift" from "we didn't check".
	Skipped bool `json:"skipped,omitempty"`
}

// LintReport is the aggregate response from LintWiki.
type LintReport struct {
	Profile        string       `json:"profile"`
	Healthy        bool         `json:"healthy"`
	IssueCount     int          `json:"issue_count"`
	Contradictions LintCategory `json:"contradictions"`
	Orphans        LintCategory `json:"orphans"`
	StaleLifecycle LintCategory `json:"stale_lifecycle"`
	StaleSummaries LintCategory `json:"stale_summaries"`
	SummaryDrift   LintCategory `json:"summary_drift"`
}

// LintWikiOptions mirrors the Python lint_report keyword args.
type LintWikiOptions struct {
	SampleSize      int  // default 10
	StableDays      int  // default 90
	IncludeDrift    bool // default true (caller passes opts; LintWiki sets if zero-value)
	includeDriftSet bool // internal: true when caller explicitly opted in/out
}

// SetIncludeDrift makes the includeDrift flag explicit. Calling this
// distinguishes "I want drift skipped" from "I left the field at the
// zero value (false) by accident." The default when unset is true.
func (o *LintWikiOptions) SetIncludeDrift(v bool) {
	o.IncludeDrift = v
	o.includeDriftSet = true
}

// LintWiki computes the read-only lint report. Profile defaults to the
// active profile when empty.
func LintWiki(ctx context.Context, cfg *Config, profile string, opts LintWikiOptions) (*LintReport, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native lint_wiki: nil config")
	}
	if profile == "" {
		profile = ActiveProfile(cfg)
	}
	sampleSize := opts.SampleSize
	if sampleSize <= 0 {
		sampleSize = LintDefaultSampleSize
	}
	stableDays := opts.StableDays
	if stableDays <= 0 {
		stableDays = LintDefaultStableDays
	}
	includeDrift := opts.IncludeDrift
	if !opts.includeDriftSet {
		includeDrift = true
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	report := &LintReport{Profile: profile}

	switch backend {
	case "postgres":
		err = lintWikiPostgres(ctx, cfg, profile, sampleSize, stableDays, includeDrift, report)
	case "supabase":
		err = lintWikiSupabase(ctx, cfg, profile, sampleSize, stableDays, includeDrift, report)
	default:
		return nil, fmt.Errorf("native lint_wiki: unknown backend %q", backend)
	}
	if err != nil {
		return nil, err
	}

	report.IssueCount = report.Contradictions.Count +
		report.Orphans.Count +
		report.StaleLifecycle.Count +
		report.StaleSummaries.Count +
		report.SummaryDrift.Count
	report.Healthy = report.IssueCount == 0
	return report, nil
}

// lintWikiPostgres runs each category against the migration-031 RPCs
// over a single connection. We reuse the connection because the lint
// fan-out is hot for dashboards calling at interactive cadence.
func lintWikiPostgres(ctx context.Context, cfg *Config, profile string, sampleSize, stableDays int, includeDrift bool, out *LintReport) error {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("lint_wiki: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// 1. contradictions
	cat, err := lintContradictionsPostgres(ctx, conn, profile, sampleSize)
	if err != nil {
		return err
	}
	out.Contradictions = cat

	// 2. orphans
	cat, err = lintOrphansPostgres(ctx, conn, profile, sampleSize, LintDefaultOrphanGraceMinutes)
	if err != nil {
		return err
	}
	out.Orphans = cat

	// 3. stale_lifecycle
	cat, err = lintStaleLifecyclePostgres(ctx, conn, profile, stableDays, sampleSize)
	if err != nil {
		return err
	}
	cat.OlderThanDays = stableDays
	out.StaleLifecycle = cat

	// 4. stale_summaries (direct query against topic_summaries; mirrors
	//    Python's find_stale_summaries which calls list_stale).
	cat, err = lintStaleSummariesPostgres(ctx, conn, profile, sampleSize)
	if err != nil {
		return err
	}
	out.StaleSummaries = cat

	// 5. summary_drift -- optional, can be slow on huge profiles.
	if !includeDrift {
		out.SummaryDrift = LintCategory{Skipped: true, Sample: []map[string]any{}}
		return nil
	}
	cat, err = lintSummaryDriftPostgres(ctx, conn, profile, sampleSize)
	if err != nil {
		return err
	}
	out.SummaryDrift = cat
	return nil
}

func lintContradictionsPostgres(ctx context.Context, conn *pgx.Conn, profile string, sampleSize int) (LintCategory, error) {
	rows, err := conn.Query(ctx, `
SELECT source_id, target_id, strength, created_at, total_count
  FROM wiki_lint_contradictions($1, $2)`, profile, sampleSize)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki contradictions: %w", err)
	}
	defer rows.Close()

	cat := LintCategory{Sample: []map[string]any{}}
	for rows.Next() {
		var (
			sourceID, targetID string
			strength           float64
			createdAt          time.Time
			total              int64
		)
		if err := rows.Scan(&sourceID, &targetID, &strength, &createdAt, &total); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki contradictions scan: %w", err)
		}
		cat.Count = int(total)
		cat.Sample = append(cat.Sample, map[string]any{
			"source_id":  sourceID,
			"target_id":  targetID,
			"strength":   strength,
			"created_at": createdAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return cat, rows.Err()
}

func lintOrphansPostgres(ctx context.Context, conn *pgx.Conn, profile string, sampleSize, graceMinutes int) (LintCategory, error) {
	rows, err := conn.Query(ctx, `
SELECT id, content, tags, created_at, total_count
  FROM wiki_lint_orphans($1, $2, $3)`, profile, sampleSize, graceMinutes)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki orphans: %w", err)
	}
	defer rows.Close()

	cat := LintCategory{Sample: []map[string]any{}}
	for rows.Next() {
		var (
			id        string
			content   string
			tags      []string
			createdAt time.Time
			total     int64
		)
		if err := rows.Scan(&id, &content, &tags, &createdAt, &total); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki orphans scan: %w", err)
		}
		cat.Count = int(total)
		cat.Sample = append(cat.Sample, map[string]any{
			"id":         id,
			"content":    content,
			"tags":       tags,
			"created_at": createdAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return cat, rows.Err()
}

func lintStaleLifecyclePostgres(ctx context.Context, conn *pgx.Conn, profile string, olderThanDays, sampleSize int) (LintCategory, error) {
	rows, err := conn.Query(ctx, `
SELECT id, stage, stage_entered_at, content, total_count
  FROM wiki_lint_stale_lifecycle($1, $2, $3)`, profile, olderThanDays, sampleSize)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki stale_lifecycle: %w", err)
	}
	defer rows.Close()

	cat := LintCategory{Sample: []map[string]any{}}
	for rows.Next() {
		var (
			id, stage, content string
			stageEntered       time.Time
			total              int64
		)
		if err := rows.Scan(&id, &stage, &stageEntered, &content, &total); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki stale_lifecycle scan: %w", err)
		}
		cat.Count = int(total)
		cat.Sample = append(cat.Sample, map[string]any{
			"id":               id,
			"stage":            stage,
			"stage_entered_at": stageEntered.UTC().Format(time.RFC3339Nano),
			"content":          content,
		})
	}
	return cat, rows.Err()
}

// lintStaleSummariesPostgres mirrors Python's find_stale_summaries:
// status='stale' rows from topic_summaries for the profile. Direct
// table read because the wiki_topic_list_stale RPC returns SETOF
// topic_summaries (full rows) -- we project the same fields the Python
// wrapper exposes in its sample.
func lintStaleSummariesPostgres(ctx context.Context, conn *pgx.Conn, profile string, sampleSize int) (LintCategory, error) {
	// First the count.
	var total int64
	if err := conn.QueryRow(ctx, `
SELECT count(*) FROM topic_summaries
 WHERE profile_id = $1 AND status = 'stale'`, profile).Scan(&total); err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries count: %w", err)
	}

	cat := LintCategory{Count: int(total), Sample: []map[string]any{}}
	if total == 0 {
		return cat, nil
	}

	// Then the sample.
	rows, err := conn.Query(ctx, `
SELECT id, topic_key, version, stale_reason, updated_at
  FROM topic_summaries
 WHERE profile_id = $1 AND status = 'stale'
 ORDER BY updated_at DESC
 LIMIT $2`, profile, sampleSize)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries sample: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, topicKey string
			version      int
			staleReason  *string
			updatedAt    time.Time
		)
		if err := rows.Scan(&id, &topicKey, &version, &staleReason, &updatedAt); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries scan: %w", err)
		}
		entry := map[string]any{
			"id":         id,
			"topic_key":  topicKey,
			"version":    version,
			"updated_at": updatedAt.UTC().Format(time.RFC3339Nano),
		}
		if staleReason != nil {
			entry["stale_reason"] = *staleReason
		}
		cat.Sample = append(cat.Sample, entry)
	}
	return cat, rows.Err()
}

// lintSummaryDriftPostgres mirrors Python's find_summary_drift: walks
// fresh summaries, recomputes the source_hash over their current
// matching memories, and compares to the stored hash. This is the
// expensive category (per-topic loop); we keep it strictly read-only.
//
// The Python implementation calls wiki_topic_list_fresh_for_drift then
// wiki_recompute_get_source_ids per topic. We replicate the same flow
// with two prepared queries so a 1k-topic profile doesn't pay
// per-iteration RPC overhead.
func lintSummaryDriftPostgres(ctx context.Context, conn *pgx.Conn, profile string, sampleSize int) (LintCategory, error) {
	rows, err := conn.Query(ctx, `
SELECT id, topic_key, source_hash
  FROM wiki_topic_list_fresh_for_drift($1)`, profile)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki summary_drift list: %w", err)
	}
	defer rows.Close()

	type freshRow struct {
		id       string
		topicKey string
		stored   []byte
	}
	var fresh []freshRow
	for rows.Next() {
		var fr freshRow
		if err := rows.Scan(&fr.id, &fr.topicKey, &fr.stored); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki summary_drift scan: %w", err)
		}
		fresh = append(fresh, fr)
	}
	if err := rows.Err(); err != nil {
		return LintCategory{}, err
	}

	drifted := []map[string]any{}
	for _, fr := range fresh {
		idRows, err := conn.Query(ctx, `
SELECT id FROM wiki_recompute_get_source_ids($1, $2)`, profile, fr.topicKey)
		if err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki summary_drift source-ids: %w", err)
		}
		var currentIDs []string
		for idRows.Next() {
			var idStr string
			if err := idRows.Scan(&idStr); err != nil {
				idRows.Close()
				return LintCategory{}, fmt.Errorf("lint_wiki summary_drift id scan: %w", err)
			}
			currentIDs = append(currentIDs, idStr)
		}
		_ = idRows.Err()
		idRows.Close()

		current := computeSourceHash(currentIDs)
		if !bytesEqual(current, fr.stored) {
			drifted = append(drifted, map[string]any{
				"id":                   fr.id,
				"topic_key":            fr.topicKey,
				"current_source_count": len(currentIDs),
			})
		}
	}

	cat := LintCategory{
		Count:  len(drifted),
		Sample: drifted,
	}
	if len(cat.Sample) > sampleSize {
		cat.Sample = cat.Sample[:sampleSize]
	}
	if cat.Sample == nil {
		cat.Sample = []map[string]any{}
	}
	return cat, nil
}

// computeSourceHash mirrors topic_summaries.compute_source_hash --
// SHA-256 over sorted memory IDs joined by newline. The sort makes the
// hash insertion-order independent so an UPDATE that reorders the
// child rows doesn't wrongly trigger drift.
func computeSourceHash(ids []string) []byte {
	if len(ids) == 0 {
		// Match Python: hash of empty string. SHA-256 of "" is well-defined.
		h := sha256.Sum256([]byte(""))
		return h[:]
	}
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	joined := ""
	for i, id := range sorted {
		if i > 0 {
			joined += "\n"
		}
		joined += id
	}
	h := sha256.Sum256([]byte(joined))
	return h[:]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lintWikiSupabase runs the same lint flow against the PostgREST RPCs.
// Each helper returns rows as JSON; we project them into the same
// LintCategory shape so the report is backend-agnostic.
func lintWikiSupabase(ctx context.Context, cfg *Config, profile string, sampleSize, stableDays int, includeDrift bool, out *LintReport) error {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return err
	}

	// 1. contradictions
	raw, err := client.callRPC(ctx, "wiki_lint_contradictions", map[string]any{
		"p_profile":     profile,
		"p_sample_size": sampleSize,
	})
	if err != nil {
		return err
	}
	out.Contradictions = parseLintCountSample(raw, []string{"source_id", "target_id", "strength", "created_at"})

	// 2. orphans
	raw, err = client.callRPC(ctx, "wiki_lint_orphans", map[string]any{
		"p_profile":       profile,
		"p_sample_size":   sampleSize,
		"p_grace_minutes": LintDefaultOrphanGraceMinutes,
	})
	if err != nil {
		return err
	}
	out.Orphans = parseLintCountSample(raw, []string{"id", "content", "tags", "created_at"})

	// 3. stale_lifecycle
	raw, err = client.callRPC(ctx, "wiki_lint_stale_lifecycle", map[string]any{
		"p_profile":         profile,
		"p_older_than_days": stableDays,
		"p_sample_size":     sampleSize,
	})
	if err != nil {
		return err
	}
	out.StaleLifecycle = parseLintCountSample(raw, []string{"id", "stage", "stage_entered_at", "content"})
	out.StaleLifecycle.OlderThanDays = stableDays

	// 4. stale_summaries (direct table read; PostgREST count=exact for total).
	cat, err := lintStaleSummariesSupabase(ctx, client, profile, sampleSize)
	if err != nil {
		return err
	}
	out.StaleSummaries = cat

	// 5. summary_drift -- optional.
	if !includeDrift {
		out.SummaryDrift = LintCategory{Skipped: true, Sample: []map[string]any{}}
		return nil
	}
	cat, err = lintSummaryDriftSupabase(ctx, client, profile, sampleSize)
	if err != nil {
		return err
	}
	out.SummaryDrift = cat
	return nil
}

// parseLintCountSample shapes the table-returning RPC response into
// LintCategory. Strips total_count off each row, lifts it to .Count,
// keeps the listed projection in .Sample.
func parseLintCountSample(raw []byte, keep []string) LintCategory {
	cat := LintCategory{Sample: []map[string]any{}}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return cat
	}
	if v, ok := rows[0]["total_count"]; ok {
		switch n := v.(type) {
		case float64:
			cat.Count = int(n)
		case int:
			cat.Count = n
		case int64:
			cat.Count = int(n)
		}
	}
	for _, r := range rows {
		out := make(map[string]any, len(keep))
		for _, k := range keep {
			if v, ok := r[k]; ok {
				out[k] = v
			}
		}
		cat.Sample = append(cat.Sample, out)
	}
	return cat
}

func lintStaleSummariesSupabase(ctx context.Context, client *supabaseClient, profile string, sampleSize int) (LintCategory, error) {
	// Count via PostgREST count=exact.
	urlVals := url.Values{}
	urlVals.Set("profile_id", "eq."+profile)
	urlVals.Set("status", "eq.stale")
	total, err := client.headCountExact(ctx, "topic_summaries", urlVals)
	if err != nil {
		// Mixed-version safe -- pre-028 DBs have no topic_summaries;
		// treat as no stale summaries rather than returning a hard error.
		if isRelationNotFound(err) {
			return LintCategory{Sample: []map[string]any{}}, nil
		}
		return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries count: %w", err)
	}
	cat := LintCategory{Count: int(total), Sample: []map[string]any{}}
	if total == 0 {
		return cat, nil
	}

	// Sample.
	urlVals.Set("select", "id,topic_key,version,stale_reason,updated_at")
	urlVals.Set("order", "updated_at.desc")
	urlVals.Set("limit", fmt.Sprintf("%d", sampleSize))
	endpoint := client.baseURL + "/topic_summaries?" + urlVals.Encode()
	raw, err := client.getJSON(ctx, endpoint)
	if err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries sample: %w", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki stale_summaries parse: %w (body: %s)", err, truncateForError(raw))
	}
	for _, r := range rows {
		// Strip nil stale_reason for cleanliness; mirror Python which
		// uses `r.get("stale_reason")` (preserves nil) -- but the JSON
		// already has it, so just pass through.
		cat.Sample = append(cat.Sample, r)
	}
	return cat, nil
}

func lintSummaryDriftSupabase(ctx context.Context, client *supabaseClient, profile string, sampleSize int) (LintCategory, error) {
	raw, err := client.callRPC(ctx, "wiki_topic_list_fresh_for_drift", map[string]any{
		"p_profile": profile,
	})
	if err != nil {
		return LintCategory{}, err
	}
	type freshRow struct {
		ID         string `json:"id"`
		TopicKey   string `json:"topic_key"`
		SourceHash string `json:"source_hash"` // "\xDEADBEEF"
	}
	var fresh []freshRow
	if err := json.Unmarshal(raw, &fresh); err != nil {
		return LintCategory{}, fmt.Errorf("lint_wiki summary_drift list: %w (body: %s)", err, truncateForError(raw))
	}

	drifted := []map[string]any{}
	for _, fr := range fresh {
		raw, err := client.callRPC(ctx, "wiki_recompute_get_source_ids", map[string]any{
			"p_profile":   profile,
			"p_topic_key": fr.TopicKey,
		})
		if err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki summary_drift source-ids: %w", err)
		}
		var idRows []map[string]any
		if err := json.Unmarshal(raw, &idRows); err != nil {
			return LintCategory{}, fmt.Errorf("lint_wiki summary_drift parse: %w", err)
		}
		ids := make([]string, 0, len(idRows))
		for _, r := range idRows {
			if v, ok := r["id"].(string); ok {
				ids = append(ids, v)
			}
		}

		current := computeSourceHash(ids)
		stored, ok := decodePostgRESTBytea(fr.SourceHash)
		if !ok || !bytesEqual(current, stored) {
			drifted = append(drifted, map[string]any{
				"id":                   fr.ID,
				"topic_key":            fr.TopicKey,
				"current_source_count": len(ids),
			})
		}
	}

	cat := LintCategory{
		Count:  len(drifted),
		Sample: drifted,
	}
	if len(cat.Sample) > sampleSize {
		cat.Sample = cat.Sample[:sampleSize]
	}
	if cat.Sample == nil {
		cat.Sample = []map[string]any{}
	}
	return cat, nil
}

// decodePostgRESTBytea parses "\xDEADBEEF" into raw bytes.
func decodePostgRESTBytea(s string) ([]byte, bool) {
	if len(s) > 2 && s[0] == '\\' && s[1] == 'x' {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}
