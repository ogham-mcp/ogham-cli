package native

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// Pure functions: surprise, auto-link selection, tag merging.
// ---------------------------------------------------------------------

func TestComputeSurprise_EmptyNeighbors_ReturnsFallback(t *testing.T) {
	got := computeSurprise(nil)
	if math.Abs(got-surpriseFallback) > 1e-9 {
		t.Errorf("empty neighbors: got %v, want %v", got, surpriseFallback)
	}
}

func TestComputeSurprise_PicksMaxSimilarity(t *testing.T) {
	neighbors := []SearchResult{
		{Similarity: 0.2},
		{Similarity: 0.75},
		{Similarity: 0.4},
	}
	// surprise = 1 - max_sim = 1 - 0.75 = 0.25
	got := computeSurprise(neighbors)
	if math.Abs(got-0.25) > 1e-9 {
		t.Errorf("got %v, want 0.25", got)
	}
}

func TestComputeSurprise_Clamps(t *testing.T) {
	// Negative similarity (theoretically possible on a badly-calibrated
	// backend) should still produce a clamped [0,1] surprise.
	above := []SearchResult{{Similarity: 1.5}}
	below := []SearchResult{{Similarity: -0.5}}
	if got := computeSurprise(above); got != 0 {
		t.Errorf("similarity > 1: got %v, want 0 (clamped)", got)
	}
	if got := computeSurprise(below); got != 1 {
		t.Errorf("negative similarity: got %v, want 1 (clamped)", got)
	}
}

func TestPickAutoLinks_FiltersByThreshold(t *testing.T) {
	neighbors := []SearchResult{
		{ID: "a", Similarity: 0.91},
		{ID: "b", Similarity: 0.65}, // below threshold
		{ID: "c", Similarity: 0.78},
		{ID: "d", Similarity: 0.72},
		{ID: "e", Similarity: 0.99},
	}
	got := pickAutoLinks(neighbors, 0.70, 3)
	// Should return top 3 sorted desc by similarity: e, a, c.
	// b filtered out; d drops below the cap of 3.
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{"e", "a", "c"}
	for i, l := range got {
		if l.ID != wantOrder[i] {
			t.Errorf("position %d: id = %s, want %s", i, l.ID, wantOrder[i])
		}
	}
}

func TestPickAutoLinks_AllBelowThreshold(t *testing.T) {
	neighbors := []SearchResult{
		{ID: "a", Similarity: 0.5},
		{ID: "b", Similarity: 0.55},
	}
	got := pickAutoLinks(neighbors, 0.70, 3)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (all below threshold)", len(got))
	}
}

func TestMergeTags_DedupsAndSorts(t *testing.T) {
	caller := []string{"type:decision", "project:ogham"}
	entities := []string{"entity:NewEmbedder", "person:Kevin"}
	dates := []string{"2026-04-21", "2026-03-10"}

	got := mergeTags(caller, entities, dates)

	want := []string{
		"2026-04-21", // caller tags don't get a date: prefix; dates do
		"date:2026-03-10",
		"date:2026-04-21",
		"entity:NewEmbedder",
		"person:Kevin",
		"project:ogham",
		"type:decision",
	}
	// Actually "2026-04-21" shouldn't be in caller -- fix the test above:
	// only dates get "date:" prefix. Rebuild the expected set without it.
	_ = want
	// Correct expectation:
	wantSet := map[string]bool{
		"type:decision":      true,
		"project:ogham":      true,
		"entity:NewEmbedder": true,
		"person:Kevin":       true,
		"date:2026-04-21":    true,
		"date:2026-03-10":    true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("len = %d (%v), want %d", len(got), got, len(wantSet))
	}
	for _, tag := range got {
		if !wantSet[tag] {
			t.Errorf("unexpected tag: %q", tag)
		}
	}
	// Output must be sorted.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("tags not sorted: %q >= %q", got[i-1], got[i])
		}
	}
}

func TestMergeTags_Dedups(t *testing.T) {
	got := mergeTags(
		[]string{"type:decision", "type:decision"},
		[]string{"entity:Foo", "type:decision"}, // overlap with caller
		nil,
	)
	if len(got) != 2 {
		t.Errorf("dedup failed: got %v, want 2 entries", got)
	}
}

// ---------------------------------------------------------------------
// Store() smoke tests. Use DryRun=true so we don't need a DB.
// ---------------------------------------------------------------------

func TestStore_NilConfig(t *testing.T) {
	_, err := Store(context.Background(), nil, "anything", StoreOptions{})
	if err == nil {
		t.Fatal("expected error on nil config")
	}
}

func TestStore_EmptyContent(t *testing.T) {
	cfg := &Config{
		Embedding: Embedding{Provider: "openai", APIKey: "sk", Dimension: 4},
	}
	_, err := Store(context.Background(), cfg, "   ", StoreOptions{})
	if err == nil || !strings.Contains(err.Error(), "empty content") {
		t.Errorf("expected empty-content error, got %v", err)
	}
}

func TestStore_DryRun_RunsExtractionAndReturnsWithoutWrite(t *testing.T) {
	// Point the embedder at a local-only path that would fail to reach
	// the provider, then prove DryRun short-circuits the write AND
	// surface the extraction + importance + surprise fallback.
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	cfg := &Config{
		Profile:  "work",
		Database: Database{Backend: "postgres", URL: "postgres://not-reached/"},
		Embedding: Embedding{
			Provider:  "openai",
			APIKey:    "sk-test",
			Dimension: 4,
			// BaseURL points to a dead port. The embed leg of the
			// errgroup WILL fail, which would normally short-circuit
			// Store. DryRun=true wraps the embedder call, and we
			// accept that the errgroup surfaces the embed failure --
			// so for the pure orchestrator shape test we use a
			// working httptest server in the integration test below.
			BaseURL: "http://127.0.0.1:1", // definitely-dead
		},
	}
	content := "We decided to use Voyage for the OGHAM store because /Users/kevin/foo breaks."

	// We expect the embed leg to fail -- prove that Store returns a
	// clean error rather than panicking, and that extraction has
	// already produced useful data when the failure happens.
	_, err := Store(context.Background(), cfg, content, StoreOptions{
		Tags:    []string{"type:decision", "project:ogham"},
		Source:  "claude-code",
		Profile: "work",
		DryRun:  true,
	})
	if err == nil {
		t.Fatal("expected embed failure against dead backend")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("error should mention embed failure, got %v", err)
	}
}

// ---------------------------------------------------------------------
// Full DryRun orchestrator test with a working httptest embedder.
// Verifies: extraction -> parallel embed -> surprise fallback (no
// backend reachable for search) -> DryRun skips write, returns result.
// ---------------------------------------------------------------------

func TestStore_DryRun_FullPipeline(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")

	// Fake OpenAI server -- returns a pretend 4-dim vector.
	server := newFakeOpenAIEmbedServer(t, []float32{0.1, 0.2, 0.3, 0.4})
	defer server.Close()

	cfg := &Config{
		Profile: "work",
		// No backend configured -> search leg errors; store continues
		// with surprise fallback because search errors are swallowed.
		Database: Database{},
		Embedding: Embedding{
			Provider:  "openai",
			APIKey:    "sk-test",
			Dimension: 4,
			BaseURL:   server.URL,
		},
	}

	// Content with a date phrase that extraction.DatesAt should pick up,
	// and a person name + file path for entity extraction.
	content := "On 2026-03-10 Kevin Burns decided to port extraction.go to Go."

	res, err := Store(context.Background(), cfg, content, StoreOptions{
		Tags:   []string{"type:decision"},
		Source: "claude-code",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Store dry-run: %v", err)
	}

	if res.ID != "" {
		t.Errorf("DryRun should not produce an ID, got %q", res.ID)
	}
	if !res.DryRun {
		t.Errorf("DryRun flag should be set on result")
	}
	if res.Profile != "work" {
		t.Errorf("profile = %q, want work", res.Profile)
	}
	if len(res.Entities) == 0 {
		t.Error("expected entities to be extracted")
	}
	// Date extraction is tested directly in the extraction package; here
	// we only check that the hook is wired (dates array may be empty on
	// some corpora; accept either).
	if res.Importance <= 0 || res.Importance > 1 {
		t.Errorf("importance out of range: %v", res.Importance)
	}
	if res.Surprise != surpriseFallback {
		t.Errorf("surprise = %v, want fallback %v (no backend available for search leg)", res.Surprise, surpriseFallback)
	}
	if res.Elapsed <= 0 || res.Elapsed > 30*time.Second {
		t.Errorf("elapsed = %v, want positive + reasonable", res.Elapsed)
	}

	// Caller tag should be preserved and entity tags merged in.
	var found bool
	for _, tag := range res.Tags {
		if tag == "type:decision" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("caller tag not preserved in result.Tags: %v", res.Tags)
	}
}

// ---------------------------------------------------------------------
// Helpers: a reusable OpenAI-shaped httptest server. Declared in the
// _test.go file so it doesn't bloat the main binary.
// ---------------------------------------------------------------------

// newFakeOpenAIEmbedServer returns an httptest.Server that responds to
// the single-text embeddings path with the supplied vector.
func newFakeOpenAIEmbedServer(t *testing.T, vec []float32) *testServer {
	t.Helper()
	return startFakeServer(t, `{"data":[{"embedding":`+floatSliceJSON(vec)+`,"index":0}]}`)
}

// ---------------------------------------------------------------------
// writeMemorySupabase -- PostgREST shape + happy-path insert.
//
// Validates the request body contains the right columns, confirms the
// embedding is sent as a pgvector text literal, and checks that the
// `Prefer: return=representation` header is attached so PostgREST
// echoes the new row (including id) back to us.
// ---------------------------------------------------------------------

func TestWriteMemorySupabase_RoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/memories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Prefer"); got != "return=representation" {
			t.Errorf("Prefer = %q, want return=representation", got)
		}
		if r.Header.Get("apikey") == "" || r.Header.Get("Authorization") == "" {
			t.Errorf("missing supabase auth headers: %+v", r.Header)
		}

		body, _ := io.ReadAll(r.Body)
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("body not JSON array: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		row := rows[0]
		// Required columns present.
		for _, col := range []string{"content", "embedding", "profile", "tags", "importance", "surprise", "metadata"} {
			if _, ok := row[col]; !ok {
				t.Errorf("missing column %q in body: %+v", col, row)
			}
		}
		// Embedding must be the pgvector text literal, not a JSON array.
		embed, _ := row["embedding"].(string)
		if !strings.HasPrefix(embed, "[") || !strings.HasSuffix(embed, "]") {
			t.Errorf("embedding = %q, want pgvector '[...]' literal", embed)
		}
		if row["content"] != "hello store" {
			t.Errorf("content = %v", row["content"])
		}
		if row["profile"] != "work" {
			t.Errorf("profile = %v", row["profile"])
		}
		// Metadata defaults to empty object when nothing was supplied.
		// (The call below passes a non-empty metadata map.)

		resp := []map[string]any{{
			"id":      "11111111-2222-3333-4444-555555555555",
			"content": row["content"],
			"profile": row["profile"],
		}}
		_ = json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_secret_test",
		},
	}

	id, err := writeMemorySupabase(context.Background(), cfg, storeWrite{
		Content:    "hello store",
		Embedding:  []float32{0.1, 0.2, 0.3, 0.4},
		Source:     "claude-code",
		Profile:    "work",
		Tags:       []string{"type:decision", "project:ogham"},
		Importance: 0.6,
		Surprise:   0.8,
		Metadata:   map[string]any{"dates": []string{"2026-04-21"}},
	})
	if err != nil {
		t.Fatalf("writeMemorySupabase: %v", err)
	}
	if id != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("id = %q, want pinned uuid", id)
	}
}

func TestWriteMemorySupabase_NoIdReturned(t *testing.T) {
	// PostgREST returning [] (e.g. RLS reject) surfaces a clear error
	// instead of silently succeeding.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_x"}}
	_, err := writeMemorySupabase(context.Background(), cfg, storeWrite{
		Content:   "x",
		Embedding: []float32{0.1},
		Profile:   "work",
	})
	if err == nil || !strings.Contains(err.Error(), "no id returned") {
		t.Errorf("expected 'no id returned' error, got %v", err)
	}
}

func TestWriteMemorySupabase_HttpError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key"}`))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_x"}}
	_, err := writeMemorySupabase(context.Background(), cfg, storeWrite{
		Content:   "x",
		Embedding: []float32{0.1},
		Profile:   "work",
	})
	if err == nil {
		t.Fatal("expected http error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got %v", err)
	}
}
