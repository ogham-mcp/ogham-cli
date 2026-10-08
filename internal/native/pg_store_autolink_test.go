//go:build pgcontainer

// Postgres-backed tests for issue #58: the native store must PERSIST its
// auto-links (through the server's own auto_link_memory() function) and
// write an audit `store` event, matching the Python store path. Before the
// fix it computed candidates, returned them as LinkedTo, and wrote nothing.
//
// Every test embeds through a fake OpenAI server, never a live provider, so
// none of them needs Ollama on localhost:11434.

package native

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// unitVec512 returns a 512-dim vector with a single 1.0 at index hot.
// Two different hot indices are orthogonal (cosine 0); the same index is
// identical (cosine 1). That makes the 0.85 threshold unambiguous.
func unitVec512(hot int) []float32 {
	v := make([]float32, 512)
	v[hot] = 1
	return v
}

// seedWithVec inserts a memory with an explicit embedding. insertMemory
// seeds a zero vector, whose cosine is undefined, so it cannot drive a
// similarity test.
func seedWithVec(t *testing.T, cfg *Config, profile, content string, vec []float32) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("seed connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var id string
	err = conn.QueryRow(ctx,
		`INSERT INTO memories (content, embedding, profile) VALUES ($1, $2::vector, $3) RETURNING id::text`,
		content, pgvectorLiteral(vec), profile).Scan(&id)
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	return id
}

// storeCfg is testCfg plus a fake embedder that always returns vec.
func storeCfg(t *testing.T, profile string, vec []float32) *Config {
	t.Helper()
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	server := newFakeOpenAIEmbedServer(t, vec)
	t.Cleanup(server.Close)
	cfg := testCfg(t, profile)
	cfg.Embedding = Embedding{Provider: "openai", APIKey: "sk-test", Dimension: 512, BaseURL: server.URL}
	return cfg
}

// edgesFrom returns target ids of auto `similar` edges whose source is id.
func edgesFrom(t *testing.T, cfg *Config, id string) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("edges connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx,
		`SELECT target_id::text FROM memory_relationships
		  WHERE source_id = $1::uuid AND relationship = 'similar' ORDER BY target_id`, id)
	if err != nil {
		t.Fatalf("edges query: %v", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("edges scan: %v", err)
	}
	return ids
}

// storeAuditCount returns the number of audit `store` events for id.
func storeAuditCount(t *testing.T, cfg *Config, id string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("audit connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE operation = 'store' AND resource_id = $1::uuid`, id).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return n
}

func TestPG_Store_PersistsAutoLinks(t *testing.T) {
	vec := unitVec512(0)
	cfg := storeCfg(t, "work", vec)
	resetMemories(t, cfg)
	neighbour := seedWithVec(t, cfg, "work", "an existing memory about the same thing", vec)

	res, err := Store(context.Background(), cfg, "a new memory about the same thing", StoreOptions{Source: "claude-code"})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	if got := edgesFrom(t, cfg, res.ID); len(got) != 1 || got[0] != neighbour {
		t.Fatalf("persisted edges: want [%s], got %v", neighbour, got)
	}
	// LinkedTo must report what was WRITTEN, not candidates.
	if len(res.LinkedTo) != 1 || res.LinkedTo[0].ID != neighbour {
		t.Errorf("LinkedTo: want [%s], got %+v", neighbour, res.LinkedTo)
	}
	if res.LinkError != "" {
		t.Errorf("LinkError: want empty, got %q", res.LinkError)
	}
	if n := storeAuditCount(t, cfg, res.ID); n != 1 {
		t.Errorf("audit store events: want 1, got %d", n)
	}
}

func TestPG_Store_HookSourceDoesNotLink(t *testing.T) {
	vec := unitVec512(0)
	cfg := storeCfg(t, "work", vec)
	resetMemories(t, cfg)
	seedWithVec(t, cfg, "work", "an existing memory about the same thing", vec)

	res, err := Store(context.Background(), cfg, "a captured tool call about the same thing", StoreOptions{Source: "hook:post-tool"})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Tool-call captures stay out of the knowledge graph, by rule.
	if got := edgesFrom(t, cfg, res.ID); len(got) != 0 {
		t.Fatalf("hook source must not link: got edges %v", got)
	}
	if len(res.LinkedTo) != 0 {
		t.Errorf("LinkedTo: want empty for hook source, got %+v", res.LinkedTo)
	}
	// ...but the write is still audited, like every other store.
	if n := storeAuditCount(t, cfg, res.ID); n != 1 {
		t.Errorf("audit store events: want 1, got %d", n)
	}
}

func TestPG_Store_BelowThresholdDoesNotLink(t *testing.T) {
	cfg := storeCfg(t, "work", unitVec512(0))
	resetMemories(t, cfg)
	seedWithVec(t, cfg, "work", "an unrelated memory", unitVec512(1)) // cosine 0

	res, err := Store(context.Background(), cfg, "something else entirely", StoreOptions{Source: "claude-code"})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := edgesFrom(t, cfg, res.ID); len(got) != 0 {
		t.Fatalf("below threshold must not link: got edges %v", got)
	}
}

func TestPG_Store_LinksOnlyWithinProfile(t *testing.T) {
	vec := unitVec512(0)
	cfg := storeCfg(t, "work", vec)
	resetMemories(t, cfg)
	seedWithVec(t, cfg, "personal", "the same thing, other profile", vec)

	res, err := Store(context.Background(), cfg, "the same thing", StoreOptions{Source: "claude-code"})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := edgesFrom(t, cfg, res.ID); len(got) != 0 {
		t.Fatalf("must not link across profiles: got edges %v", got)
	}
}
