//go:build live

// Live smoke tests for the v0.13 recall-verb ports. Opted into via
// `go test -tags live -v -run Live ./internal/native/` -- requires a
// reachable scratch Postgres with migrations 026, 028, 031, and 033
// applied (the dev repo's scratch DB at localhost:5433 is configured
// for this in CLAUDE.md).
//
// Each test self-skips when DATABASE_URL is unset so a contributor
// without the scratch DB can still run the rest of the live suite.

package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const liveProfileBase = "ogham_cli_v013_recall_test"

// liveCfg returns a Config wired to the scratch DB or skips the test
// when DATABASE_URL is unset. Caller is responsible for cleaning up
// any rows it creates -- runLiveCleanup is the helper for that.
func liveCfg(t *testing.T) (*Config, string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping live test")
	}
	// Per-test profile suffix keeps parallel runs from stepping on
	// each other -- t.Name() is unique within a single `go test`
	// invocation.
	profile := fmt.Sprintf("%s_%d", liveProfileBase, time.Now().UnixNano())
	cfg := &Config{
		Database:  Database{Backend: "postgres", URL: url},
		Embedding: Embedding{Provider: "gemini", APIKey: "skip"}, // not used by these verbs
		Profile:   profile,
	}
	return cfg, profile
}

// liveCleanup removes everything written by a test profile. Runs as
// t.Cleanup so the scratch DB stays tidy across runs.
func liveCleanup(t *testing.T, cfg *Config, profile string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			t.Logf("cleanup: connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		// memory_relationships ON DELETE CASCADE from memories handles
		// edge cleanup; lifecycle has FK back to memories too. We just
		// need to scrub topic_summaries (no FK from memories) plus the
		// memories themselves. Order matters only when there are
		// FK-without-cascade constraints.
		stmts := []string{
			`DELETE FROM topic_summary_sources WHERE summary_id IN
			   (SELECT id FROM topic_summaries WHERE profile_id = $1)`,
			`DELETE FROM topic_summaries WHERE profile_id = $1`,
			`DELETE FROM memory_lifecycle WHERE profile = $1`,
			`DELETE FROM memories WHERE profile = $1`,
		}
		for _, s := range stmts {
			if _, err := conn.Exec(ctx, s, profile); err != nil {
				t.Logf("cleanup %q: %v", s, err)
			}
		}
	})
}

// pad512 is a placeholder pgvector(512) literal for embedding columns.
// We use a uniform "0,0,0,..." so the HNSW index gets a valid value
// without blowing up on dim mismatch. The recall verbs we're testing
// don't read embeddings -- they walk the relational layer.
var pad512 = func() string {
	parts := make([]byte, 0, 512*2)
	for i := 0; i < 512; i++ {
		if i > 0 {
			parts = append(parts, ',')
		}
		parts = append(parts, '0')
	}
	return "[" + string(parts) + "]"
}()

// seedMemories inserts N memories for the profile, returning their ids.
// All embeddings are the pad512 zero-vector; surprise/importance are
// configurable per-row so the lifecycle gates can be exercised.
type seedRow struct {
	content    string
	tags       []string
	surprise   float64
	importance float64
}

func seedMemories(t *testing.T, cfg *Config, profile string, rows []seedRow) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("seed connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	ids := make([]string, len(rows))
	for i, r := range rows {
		var id string
		err := conn.QueryRow(ctx, `
INSERT INTO memories (profile, content, embedding, tags, source, surprise, importance)
VALUES ($1, $2, $3::vector, $4, 'live-test', $5, $6)
RETURNING id::text`,
			profile, r.content, pad512, r.tags, r.surprise, r.importance,
		).Scan(&id)
		if err != nil {
			t.Fatalf("seed memory %d: %v", i, err)
		}
		ids[i] = id
	}
	return ids
}

func seedRelationship(t *testing.T, cfg *Config, srcID, tgtID, rel string, strength float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("seed rel connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `
INSERT INTO memory_relationships (source_id, target_id, relationship, strength)
VALUES ($1::uuid, $2::uuid, $3::relationship_type, $4)
ON CONFLICT DO NOTHING`,
		srcID, tgtID, rel, strength)
	if err != nil {
		t.Fatalf("seed relationship: %v", err)
	}
}

func seedTopicSummary(t *testing.T, cfg *Config, profile, topicKey, body, oneLine, short string, sourceIDs []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("seed summary connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Compute the source_hash the way Python does: sha256 over sorted
	// IDs joined by newline. Round-tripping this is what
	// lint_wiki/find_summary_drift checks for.
	hash := computeSourceHash(sourceIDs)
	_ = hex.EncodeToString(hash)

	var id string
	err = conn.QueryRow(ctx, `
INSERT INTO topic_summaries (
  profile_id, topic_key, content, embedding,
  source_count, source_hash, model_used,
  tldr_one_line, tldr_short
)
VALUES ($1, $2, $3, $4::vector, $5, $6, 'live-test', $7, $8)
RETURNING id::text`,
		profile, topicKey, body, pad512,
		len(sourceIDs), hash, oneLine, short,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	for _, srcID := range sourceIDs {
		_, err := conn.Exec(ctx, `
INSERT INTO topic_summary_sources (summary_id, memory_id) VALUES ($1::uuid, $2::uuid)
ON CONFLICT DO NOTHING`, id, srcID)
		if err != nil {
			t.Fatalf("seed source link: %v", err)
		}
	}
	return id
}

// ----------------------------------------------------------------------
// query_topic_summary live

func TestLive_QueryTopicSummary_HappyPath(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "first memory", tags: []string{"ogham"}},
		{content: "second memory", tags: []string{"ogham"}},
	})
	seedTopicSummary(t, cfg, profile, "ogham",
		"Body of the ogham summary",
		"One liner",
		"Short paragraph",
		ids,
	)

	for _, level := range []TLDRLevel{LevelOneLine, LevelShort, LevelBody} {
		t.Run(string(level), func(t *testing.T) {
			res, err := QueryTopicSummary(context.Background(), cfg, profile, "ogham", level)
			if err != nil {
				t.Fatalf("QueryTopicSummary(%s): %v", level, err)
			}
			if res.Status != StatusOK {
				t.Errorf("Status = %q, want ok", res.Status)
			}
			if res.Level != string(level) {
				t.Errorf("Level = %q, want %q", res.Level, level)
			}
			if res.RequestedLevel != "" {
				t.Errorf("no fallback expected, got requested_level=%q", res.RequestedLevel)
			}
		})
	}
}

func TestLive_QueryTopicSummary_NotCached(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	res, err := QueryTopicSummary(context.Background(), cfg, profile, "no-such-topic", LevelBody)
	if err != nil {
		t.Fatalf("QueryTopicSummary: %v", err)
	}
	if res.Status != StatusNotCached {
		t.Errorf("Status = %q, want not_cached", res.Status)
	}
}

func TestLive_QueryTopicSummary_FallbackOnNullTLDRs(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "x", tags: []string{"old"}},
	})
	// Insert summary with NULL tldrs (pre-033 shape).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	hash := sha256.Sum256([]byte(ids[0]))
	_, err = conn.Exec(ctx, `
INSERT INTO topic_summaries (profile_id, topic_key, content, embedding,
  source_count, source_hash, model_used)
VALUES ($1, 'old', 'pre-033 body', $2::vector, 1, $3, 'live-test')`,
		profile, pad512, hash[:])
	if err != nil {
		t.Fatalf("seed null-tldr summary: %v", err)
	}

	res, err := QueryTopicSummary(context.Background(), cfg, profile, "old", LevelOneLine)
	if err != nil {
		t.Fatalf("QueryTopicSummary: %v", err)
	}
	if res.Body != "pre-033 body" {
		t.Errorf("Body = %q, want pre-033 body", res.Body)
	}
	if res.Level != string(LevelBody) {
		t.Errorf("served Level = %q, want body", res.Level)
	}
	if res.RequestedLevel != string(LevelOneLine) {
		t.Errorf("RequestedLevel = %q, want one_line", res.RequestedLevel)
	}
}

// ----------------------------------------------------------------------
// walk_knowledge live

func TestLive_WalkKnowledge_DepthOne(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "root"},
		{content: "child A"},
		{content: "child B"},
	})
	// root -> A and root -> B. depth=1 should surface both.
	seedRelationship(t, cfg, ids[0], ids[1], "derived_from", 0.9)
	seedRelationship(t, cfg, ids[0], ids[2], "related", 0.5)

	res, err := WalkKnowledge(context.Background(), cfg, ids[0], WalkKnowledgeOptions{
		Depth: 1, Direction: "outgoing",
	})
	if err != nil {
		t.Fatalf("WalkKnowledge: %v", err)
	}
	if res.NodeCount < 2 {
		t.Errorf("expected >= 2 reachable nodes, got %d", res.NodeCount)
	}
	for _, n := range res.Nodes {
		if n.Depth != 1 {
			t.Errorf("node depth = %d, want 1", n.Depth)
		}
		if n.DirectionUsed != "outgoing" {
			t.Errorf("direction_used = %q, want outgoing", n.DirectionUsed)
		}
	}
}

func TestLive_WalkKnowledge_RelationshipFilter(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "root"},
		{content: "depends"},
		{content: "cites"},
	})
	seedRelationship(t, cfg, ids[0], ids[1], "derived_from", 0.9)
	seedRelationship(t, cfg, ids[0], ids[2], "related", 0.5)

	res, err := WalkKnowledge(context.Background(), cfg, ids[0], WalkKnowledgeOptions{
		Depth:             1,
		Direction:         "outgoing",
		RelationshipTypes: []string{"derived_from"},
	})
	if err != nil {
		t.Fatalf("WalkKnowledge: %v", err)
	}
	if res.NodeCount != 1 {
		t.Errorf("expected 1 node after relationship filter, got %d", res.NodeCount)
	}
	if res.NodeCount == 1 && res.Nodes[0].Relationship != "derived_from" {
		t.Errorf("relationship = %q, want depends_on", res.Nodes[0].Relationship)
	}
}

// ----------------------------------------------------------------------
// lint_wiki live

func TestLive_LintWiki_HealthyEmpty(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if !rep.Healthy {
		t.Errorf("empty profile expected healthy; got %+v", rep)
	}
	if rep.IssueCount != 0 {
		t.Errorf("IssueCount = %d, want 0", rep.IssueCount)
	}
}

// Force a contradiction edge so wiki_lint_contradictions has rows.
func TestLive_LintWiki_DetectsContradictions(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "claim A"},
		{content: "claim B"},
	})
	seedRelationship(t, cfg, ids[0], ids[1], "contradicts", 0.9)

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.Contradictions.Count < 1 {
		t.Errorf("expected >=1 contradictions; got %+v", rep.Contradictions)
	}
}

// Stuck-in-stable lifecycle row past the threshold -> rep.StaleLifecycle.Count >=1.
func TestLive_LintWiki_StaleLifecycle(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{{content: "stuck"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// Backdate 100 days into stable so the default 90-day threshold trips.
	_, err = conn.Exec(ctx, `
INSERT INTO memory_lifecycle (memory_id, profile, stage, stage_entered_at)
VALUES ($1::uuid, $2, 'stable', now() - interval '100 days')
ON CONFLICT (memory_id) DO UPDATE
SET stage = 'stable', stage_entered_at = now() - interval '100 days'`,
		ids[0], profile,
	)
	if err != nil {
		t.Fatalf("seed lifecycle: %v", err)
	}

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.StaleLifecycle.Count < 1 {
		t.Errorf("expected >=1 stale_lifecycle; got %+v", rep.StaleLifecycle)
	}
	if rep.StaleLifecycle.OlderThanDays != 90 {
		t.Errorf("OlderThanDays = %d, want 90", rep.StaleLifecycle.OlderThanDays)
	}
}

// Stale summary in topic_summaries -> rep.StaleSummaries.Count >=1.
func TestLive_LintWiki_StaleSummaries(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{{content: "old fact"}})
	id := seedTopicSummary(t, cfg, profile, "stale-topic", "body", "one", "short", ids)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `UPDATE topic_summaries SET status = 'stale', stale_reason = 'test' WHERE id = $1::uuid`, id)
	if err != nil {
		t.Fatalf("flip stale: %v", err)
	}

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.StaleSummaries.Count < 1 {
		t.Errorf("expected >=1 stale_summaries; got %+v", rep.StaleSummaries)
	}
}

// Summary drift: stored hash mismatches re-computed hash -> drift entry.
func TestLive_LintWiki_SummaryDrift(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	// Two memories tagged "drift-topic"; the summary is seeded with a
	// hash computed against ONLY the first id, so when wiki_lint_drift
	// recomputes it pulls both ids and the hash mismatches.
	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "drift fact A", tags: []string{"drift-topic"}},
		{content: "drift fact B", tags: []string{"drift-topic"}},
	})
	// Seed summary with hash for ids[:1] only.
	seedTopicSummary(t, cfg, profile, "drift-topic", "body", "one", "short", ids[:1])

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.SummaryDrift.Skipped {
		t.Fatal("drift should not be skipped by default")
	}
	if rep.SummaryDrift.Count < 1 {
		t.Errorf("expected >=1 summary_drift; got %+v", rep.SummaryDrift)
	}
}

func TestLive_LintWiki_DetectsOrphans(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	// Seed a memory and force its created_at far enough in the past
	// that the 5-min orphan grace window is cleared. Bypassing the
	// default `now()` requires an explicit UPDATE.
	ids := seedMemories(t, cfg, profile, []seedRow{{content: "orphaned"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `UPDATE memories SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, ids[0])
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rep, err := LintWiki(context.Background(), cfg, profile, LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.Orphans.Count < 1 {
		t.Errorf("expected at least 1 orphan; got %+v", rep.Orphans)
	}
	if rep.Healthy {
		t.Error("Healthy should be false when orphans exist")
	}
}

// ----------------------------------------------------------------------
// advance_lifecycle live

func TestLive_AdvanceLifecycle_FreshToStable(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	// Seed a memory with surprise=0.9 (clears gate). Auto-trigger
	// inserts a 'fresh' lifecycle row -- in some scratch DBs the
	// trigger isn't installed, so insert lifecycle row defensively.
	ids := seedMemories(t, cfg, profile, []seedRow{
		{content: "important", surprise: 0.9, importance: 0.9},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// Ensure lifecycle row exists at stage='fresh' AND past the 1h
	// dwell cutoff. We backdate stage_entered_at by 2h.
	_, err = conn.Exec(ctx, `
INSERT INTO memory_lifecycle (memory_id, profile, stage, stage_entered_at)
VALUES ($1::uuid, $2, 'fresh', now() - interval '2 hours')
ON CONFLICT (memory_id) DO UPDATE
SET stage = 'fresh', stage_entered_at = now() - interval '2 hours'`,
		ids[0], profile,
	)
	if err != nil {
		t.Fatalf("seed lifecycle: %v", err)
	}

	rep, err := AdvanceLifecycle(context.Background(), cfg, profile, AdvanceLifecycleOptions{})
	if err != nil {
		t.Fatalf("AdvanceLifecycle: %v", err)
	}
	if rep.LifecycleAbsent {
		t.Fatal("scratch DB should have memory_lifecycle table")
	}
	if rep.FreshToStable < 1 {
		t.Errorf("FreshToStable = %d, want >=1", rep.FreshToStable)
	}

	// Verify the row is now stable.
	var stage string
	if err := conn.QueryRow(ctx, `SELECT stage FROM memory_lifecycle WHERE memory_id = $1::uuid`, ids[0]).Scan(&stage); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if stage != "stable" {
		t.Errorf("post-advance stage = %q, want stable", stage)
	}
}

func TestLive_AdvanceLifecycle_EditingClose(t *testing.T) {
	cfg, profile := liveCfg(t)
	liveCleanup(t, cfg, profile)

	ids := seedMemories(t, cfg, profile, []seedRow{{content: "in-edit"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// Editing window opened 1h ago -- past the 30min default close.
	_, err = conn.Exec(ctx, `
INSERT INTO memory_lifecycle (memory_id, profile, stage, stage_entered_at)
VALUES ($1::uuid, $2, 'editing', now() - interval '1 hour')
ON CONFLICT (memory_id) DO UPDATE
SET stage = 'editing', stage_entered_at = now() - interval '1 hour'`,
		ids[0], profile,
	)
	if err != nil {
		t.Fatalf("seed editing: %v", err)
	}

	rep, err := AdvanceLifecycle(context.Background(), cfg, profile, AdvanceLifecycleOptions{})
	if err != nil {
		t.Fatalf("AdvanceLifecycle: %v", err)
	}
	if rep.EditingClosed < 1 {
		t.Errorf("EditingClosed = %d, want >=1", rep.EditingClosed)
	}
}
