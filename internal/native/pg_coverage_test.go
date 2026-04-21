//go:build pgcontainer

// Postgres-backed coverage for the pgx functions that sit at 0% under
// the default hermetic run. Uses the shared testcontainer from
// pg_testcontainer_test.go.
//
// Target: close task #141 to 90% on internal/native/. Each test hits
// a single *Postgres function via its public entry point and asserts
// the observable result, not the SQL shape.

package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// --- Delete + Cleanup ----------------------------------------------------

func TestPG_Delete_Roundtrip(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	id := insertMemory(t, cfg, "work", "to be deleted", []string{"keep", "me"})

	result, err := Delete(context.Background(), cfg, id, "work")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if result.ID != id || result.Profile != "work" {
		t.Errorf("Delete result = %+v", result)
	}

	// Second delete of the same id must surface "no memory with id".
	_, err = Delete(context.Background(), cfg, id, "work")
	if err == nil {
		t.Error("second Delete should error when row already gone")
	}
}

func TestPG_Delete_RefusesCrossProfile(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	id := insertMemory(t, cfg, "personal", "private", nil)

	// id exists but in a different profile -- Delete must not find it.
	_, err := Delete(context.Background(), cfg, id, "work")
	if err == nil {
		t.Error("Delete should refuse to cross profile boundary")
	}
}

func TestPG_Cleanup_NoExpired_ReturnsZero(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	insertMemory(t, cfg, "work", "fresh row", nil)

	result, err := Cleanup(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if result.Deleted != 0 {
		t.Errorf("fresh row should not be cleaned up; got deleted=%d", result.Deleted)
	}
}

// --- List ----------------------------------------------------------------

func TestPG_List_RecentFirst(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	// Insert two rows; List returns most-recent first by default.
	insertMemory(t, cfg, "work", "older", []string{"tag-a"})
	idNewer := insertMemory(t, cfg, "work", "newer", []string{"tag-b"})

	results, err := List(context.Background(), cfg, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 rows, got %d", len(results))
	}
	if results[0].ID != idNewer {
		t.Errorf("newest row should be first; got %+v", results[0])
	}
}

func TestPG_List_TagFilter(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	insertMemory(t, cfg, "work", "tagged-alpha", []string{"alpha"})
	insertMemory(t, cfg, "work", "tagged-beta", []string{"beta"})

	results, err := List(context.Background(), cfg, ListOptions{
		Limit: 10,
		Tags:  []string{"alpha"},
	})
	if err != nil {
		t.Fatalf("List tag filter: %v", err)
	}
	if len(results) != 1 || results[0].Content != "tagged-alpha" {
		t.Errorf("tag filter failed; got %+v", results)
	}
}

// --- Profiles ------------------------------------------------------------

func TestPG_ListProfiles_CountsPerProfile(t *testing.T) {
	cfg := testCfg(t, "default")
	resetMemories(t, cfg)
	insertMemory(t, cfg, "work", "one", nil)
	insertMemory(t, cfg, "work", "two", nil)
	insertMemory(t, cfg, "personal", "solo", nil)

	profiles, err := ListProfiles(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}

	counts := map[string]int64{}
	for _, p := range profiles {
		counts[p.Profile] = p.Count
	}
	if counts["work"] != 2 {
		t.Errorf("work count: want 2, got %d", counts["work"])
	}
	if counts["personal"] != 1 {
		t.Errorf("personal count: want 1, got %d", counts["personal"])
	}
}

func TestPG_ProfileTTL_UpsertAndClear(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)

	// Initial set.
	result, err := SetProfileTTL(context.Background(), cfg, "work", 30)
	if err != nil {
		t.Fatalf("SetProfileTTL set: %v", err)
	}
	if result.TTLDays == nil || *result.TTLDays != 30 {
		t.Errorf("after set: TTLDays = %+v", result.TTLDays)
	}

	// Read back.
	got, err := GetProfileTTL(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("GetProfileTTL: %v", err)
	}
	if got.TTLDays == nil || *got.TTLDays != 30 {
		t.Errorf("after read: TTLDays = %+v", got.TTLDays)
	}

	// Clear via negative sentinel.
	result, err = SetProfileTTL(context.Background(), cfg, "work", -1)
	if err != nil {
		t.Fatalf("SetProfileTTL clear: %v", err)
	}
	if result.TTLDays != nil {
		t.Errorf("after clear: TTLDays should be nil; got %v", *result.TTLDays)
	}
}

// --- Stats ---------------------------------------------------------------

func TestPG_GetStats_AggregatesCorrectly(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	insertMemory(t, cfg, "work", "one", []string{"type:decision"})
	insertMemory(t, cfg, "work", "two", []string{"type:decision"})
	insertMemory(t, cfg, "work", "three", []string{"type:fact"})
	// Other-profile row must NOT show up in work stats.
	insertMemory(t, cfg, "personal", "other-profile", nil)

	stats, err := GetStats(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Profile != "work" {
		t.Errorf("stats.Profile = %q, want 'work'", stats.Profile)
	}
	if stats.Total != 3 {
		t.Errorf("stats.Total = %d, want 3 (personal row must not leak)", stats.Total)
	}
	// type:decision should be the top tag -- 2 occurrences beats
	// type:fact's 1.
	if len(stats.TopTags) == 0 || stats.TopTags[0].Name != "type:decision" {
		t.Errorf("top tag = %+v, want type:decision first", stats.TopTags)
	}
}

// --- UpdateConfidence ----------------------------------------------------

func TestPG_UpdateConfidence_ReinforceLiftsConfidence(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	id := insertMemory(t, cfg, "work", "subject to reinforcement", nil)

	// Seed starts at the schema default (confidence=0.5 unless the
	// row's insert overrides). The update_confidence SQL function
	// applies a weighted blend; a signal of 0.85 should land ABOVE the
	// starting value regardless of exact formula.
	result, err := UpdateConfidence(context.Background(), cfg, id, 0.85, "work")
	if err != nil {
		t.Fatalf("UpdateConfidence reinforce: %v", err)
	}
	if result.Confidence <= 0.5 {
		t.Errorf("reinforce should lift confidence above 0.5; got %v", result.Confidence)
	}
}

func TestPG_UpdateConfidence_ContradictDropsConfidence(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	id := insertMemory(t, cfg, "work", "subject to contradiction", nil)

	// A contradict signal of 0.15 should pull confidence BELOW the
	// 0.5 starting point.
	result, err := UpdateConfidence(context.Background(), cfg, id, 0.15, "work")
	if err != nil {
		t.Fatalf("UpdateConfidence contradict: %v", err)
	}
	if result.Confidence >= 0.5 {
		t.Errorf("contradict should drop confidence below 0.5; got %v", result.Confidence)
	}
}

// --- Update (re-embed skipped; tags + metadata only) --------------------

func TestPG_Update_TagsOnly(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	id := insertMemory(t, cfg, "work", "original content", []string{"old-tag"})

	result, err := Update(context.Background(), cfg, id, UpdateOptions{
		Tags:    []string{"new-tag", "another"},
		Profile: "work",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.ID != id {
		t.Errorf("Update returned wrong id: %q", result.ID)
	}
	if len(result.FieldsUpdated) == 0 {
		t.Error("FieldsUpdated should list 'tags'")
	}
	if result.ReEmbedded {
		t.Error("No content change -> must NOT re-embed")
	}
}

// --- Audit ---------------------------------------------------------------

func TestPG_Audit_ReturnsRecentEvents(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	// Audit log rows are emitted by triggers or explicit inserts --
	// for a self-contained test we poke one in directly.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("audit seed connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `
INSERT INTO audit_log (profile, operation, resource_id, metadata)
VALUES ('work', 'delete', gen_random_uuid(), '{"note": "test"}')`)
	if err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	events, err := Audit(context.Background(), cfg, "work", "", 10)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(events) == 0 {
		t.Error("Audit returned no events after seed")
	}
}

// --- Health --------------------------------------------------------------

func TestPG_HealthCheck_LivePostgres(t *testing.T) {
	cfg := testCfg(t, "work")
	// Configure a minimal Ollama stub so the embedder probe has
	// something to validate against. We're testing the backend probe,
	// not the embedder -- embedder validation is unit-tested elsewhere.
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Model = "embeddinggemma"
	cfg.Embedding.Dimension = 512

	report, err := HealthCheck(context.Background(), cfg, HealthOptions{})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if report.Backend != "postgres" {
		t.Errorf("report.Backend = %q, want 'postgres'", report.Backend)
	}
	// Find the backend probe specifically and assert it succeeded.
	// Other probes (embedder) may fail in the sandbox and that's fine.
	var backendCheckOK bool
	for _, c := range report.Checks {
		if c.Name == "backend:postgres" {
			backendCheckOK = c.OK
			break
		}
	}
	if !backendCheckOK {
		t.Errorf("live pg backend probe should pass; got checks=%+v", report.Checks)
	}
}

// --- Store orchestrator round-trip --------------------------------------
//
// Exercises the full Store -> writeMemoryPostgres path end-to-end,
// including extraction + surprise + auto-link + INSERT. Uses an
// httptest server masquerading as Ollama so we don't need a real
// embedder on the host.

func TestPG_Store_EmptyContentRejected(t *testing.T) {
	cfg := testCfg(t, "work")
	_, err := Store(context.Background(), cfg, "", StoreOptions{DryRun: true})
	if err == nil {
		t.Error("empty content must error")
	}
}

func TestPG_Store_FullRoundTripThroughWriteMemoryPostgres(t *testing.T) {
	// Ollama stub: return a deterministic 512-dim vector for any embed
	// request. Covers both the initial embed AND the surprise-search
	// probe (which re-queries for the same content).
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a 512-dim zero vector -- fine since we're testing the
		// INSERT plumbing, not similarity semantics.
		vec := make([]float64, 512)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": vec,
		})
	}))
	defer ollama.Close()

	cfg := testCfg(t, "work")
	resetMemories(t, cfg)
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Model = "embeddinggemma"
	cfg.Embedding.Dimension = 512
	// OllamaURL is how the embedder config looks up the host. See
	// embedding.go ollamaEmbedder.endpoint().
	t.Setenv("OLLAMA_URL", ollama.URL)

	result, err := Store(context.Background(), cfg,
		"Decision: use postgres for the test harness.", StoreOptions{
			Source:  "pg-coverage-test",
			Tags:    []string{"type:decision"},
			Profile: "work",
		})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if result.ID == "" {
		t.Error("Store should return a non-empty id after real INSERT")
	}
	if result.Profile != "work" {
		t.Errorf("result.Profile = %q", result.Profile)
	}

	// Follow-up: the row should now be readable via List.
	listed, err := List(context.Background(), cfg, ListOptions{Limit: 5})
	if err != nil {
		t.Fatalf("List after Store: %v", err)
	}
	found := false
	for _, m := range listed {
		if m.ID == result.ID {
			found = true
			if !strings.Contains(m.Content, "postgres") {
				t.Errorf("content round-trip failed: %q", m.Content)
			}
			break
		}
	}
	if !found {
		t.Errorf("stored id %q not found in List result", result.ID)
	}
}

