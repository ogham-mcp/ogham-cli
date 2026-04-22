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

func TestResolveBackend_Explicit(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: "x"}}
	got, err := cfg.ResolveBackend()
	if err != nil || got != "postgres" {
		t.Errorf("got (%q, %v)", got, err)
	}
}

func TestResolveBackend_AutoSupabase(t *testing.T) {
	cfg := &Config{Database: Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "k"}}
	got, _ := cfg.ResolveBackend()
	if got != "supabase" {
		t.Errorf("got %q, want supabase", got)
	}
}

func TestResolveBackend_AutoPostgres(t *testing.T) {
	cfg := &Config{Database: Database{URL: "postgres://x"}}
	got, _ := cfg.ResolveBackend()
	if got != "postgres" {
		t.Errorf("got %q, want postgres", got)
	}
}

func TestResolveBackend_SupabaseWithoutKeyFallsThrough(t *testing.T) {
	// URL alone isn't enough -- Supabase needs a key too. If the user set
	// the URL but forgot the key, we'd rather error than silently try
	// un-authenticated calls.
	cfg := &Config{Database: Database{SupabaseURL: "https://x.supabase.co", URL: "postgres://y"}}
	got, _ := cfg.ResolveBackend()
	if got != "postgres" {
		t.Errorf("got %q, want postgres fallback when Supabase key missing", got)
	}
}

func TestResolveBackend_NothingConfigured(t *testing.T) {
	cfg := &Config{}
	_, err := cfg.ResolveBackend()
	if err == nil {
		t.Error("expected error when nothing is configured")
	}
}

func TestNewSupabaseClient_RequiresURLAndKey(t *testing.T) {
	if _, err := newSupabaseClient(&Config{}); err == nil {
		t.Error("expected error with no URL")
	}
	if _, err := newSupabaseClient(&Config{
		Database: Database{SupabaseURL: "https://x.supabase.co"},
	}); err == nil {
		t.Error("expected error with no key")
	}
}

func TestSearchSupabase_RoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/models/gemini-embedding-2-preview:embedContent":
			// Embedder hit. Unit vector so the L2 normalization we do
			// client-side for sub-3072 dims is a no-op and downstream
			// checks see the exact value we send.
			unit := float32(1.0 / math.Sqrt(512.0))
			vec := make([]float32, 512)
			for i := range vec {
				vec[i] = unit
			}
			_ = json.NewEncoder(w).Encode(geminiEmbedResponse{
				Embedding: &geminiEmbeddingPayload{Values: vec},
			})
		case "/rest/v1/rpc/hybrid_search_memories":
			// Validate auth headers + JSON body shape.
			if r.Header.Get("apikey") == "" || r.Header.Get("Authorization") == "" {
				t.Errorf("missing supabase auth headers: %+v", r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("body not JSON: %v", err)
			}
			for _, k := range []string{"query_text", "query_embedding", "match_count", "filter_profile"} {
				if _, ok := got[k]; !ok {
					t.Errorf("RPC arg missing: %s", k)
				}
			}
			if got["query_text"] != "hello" {
				t.Errorf("query_text = %v, want hello", got["query_text"])
			}
			rows := []map[string]any{
				{
					"id": "11111111-1111-1111-1111-111111111111", "content": "match one",
					"profile": "work", "tags": []string{"a"}, "source": "claude-code",
					"similarity": 0.8, "keyword_rank": 0.0, "relevance": 0.03,
					"created_at": "2026-04-19T19:32:05Z",
				},
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_secret_test",
		},
		Embedding: Embedding{
			Provider: "gemini", APIKey: "gm_test", Dimension: 512,
			Model: "gemini-embedding-2-preview",
		},
		Profile: "work",
	}

	// Inject the test server URL into the embedder. Since NewEmbedder
	// hard-codes the production URL, build one by hand.
	emb := &geminiEmbedder{
		apiKey:  cfg.Embedding.APIKey,
		model:   cfg.Embedding.Model,
		dim:     cfg.Embedding.Dimension,
		http:    server.Client(),
		baseURL: server.URL,
	}
	vec, err := emb.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	if len(vec) != 512 {
		t.Fatalf("embedding dim = %d", len(vec))
	}

	// Exercise the Supabase RPC path directly by calling the helper that
	// does not re-create the embedder -- we just proved embedder works.
	client := &supabaseClient{
		baseURL: server.URL + "/rest/v1",
		apiKey:  cfg.Database.SupabaseKey,
		http:    server.Client(),
	}
	raw, err := client.callRPC(context.Background(), "hybrid_search_memories", map[string]any{
		"query_text":      "hello",
		"query_embedding": pgvectorLiteral(vec),
		"match_count":     10,
		"filter_profile":  "work",
	})
	if err != nil {
		t.Fatalf("callRPC: %v", err)
	}
	var rows []SearchResult
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 || rows[0].Content != "match one" {
		t.Errorf("unexpected rows: %+v", rows)
	}
}

func TestListSupabase_RoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/v1/memories") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("profile") != "eq.work" {
			t.Errorf("profile filter = %q", q.Get("profile"))
		}
		if q.Get("order") != "created_at.desc" {
			t.Errorf("order = %q", q.Get("order"))
		}
		if !strings.Contains(q.Get("or"), "expires_at") {
			t.Errorf("missing expires_at filter: %q", q.Get("or"))
		}
		rows := []map[string]any{
			{
				"id": "22222222-2222-2222-2222-222222222222", "content": "recent",
				"tags": []string{"type:note"}, "source": "claude-code",
				"created_at": "2026-04-19T22:00:00Z",
			},
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_secret_test",
		},
		Profile: "work",
	}

	got, err := listSupabase(context.Background(), cfg, ListOptions{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "recent" {
		t.Errorf("unexpected rows: %+v", got)
	}
}

func TestSupabaseClient_ErrorSurfacing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid API key"}`))
	}))
	defer server.Close()

	client := &supabaseClient{
		baseURL: server.URL + "/rest/v1",
		apiKey:  "bad",
		http:    server.Client(),
	}
	_, err := client.callRPC(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error missing status: %v", err)
	}
}

// TestListSupabase_BeforeCursor asserts that ListOptions.Before lands in
// the PostgREST query as a `created_at=lt.<iso>` filter.
func TestListSupabase_BeforeCursor(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	cutoff := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	_, err := listSupabase(context.Background(), cfg, ListOptions{Limit: 10, Before: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	// The PostgREST predicate is URL-encoded; checking for the core
	// substring is enough -- order of keys in url.Values.Encode is
	// unspecified but contents are stable.
	if !strings.Contains(captured, "created_at=lt.") {
		t.Errorf("Before missing from query: %s", captured)
	}
}

// TestStoreCountsByDaySupabase_ClientAggregation posts 4 rows spread
// across 2 days and asserts the aggregator groups them correctly.
func TestStoreCountsByDaySupabase_ClientAggregation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dayA := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
		rows := []map[string]any{
			{"created_at": dayA.Format(time.RFC3339)},
			{"created_at": dayA.Add(time.Hour).Format(time.RFC3339)},
			{"created_at": dayA.Add(30 * time.Hour).Format(time.RFC3339)}, // next UTC day
			{"created_at": dayA.Add(31 * time.Hour).Format(time.RFC3339)},
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	got, err := StoreCountsByDay(context.Background(), cfg, 365)
	if err != nil {
		t.Fatalf("StoreCountsByDay: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %d (%+v)", len(got), got)
	}
	var total int64
	for _, d := range got {
		total += d.Count
	}
	if total != 4 {
		t.Errorf("total: want 4 got %d", total)
	}
}

// TestAuditEntriesSupabase_FilterAndCursor asserts that the operation
// + before filters are passed through to PostgREST correctly.
func TestAuditEntriesSupabase_FilterAndCursor(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.RawQuery
		rows := []map[string]any{
			{"event_time": "2026-04-01T12:00:00Z", "profile": "work", "operation": "delete"},
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	cutoff := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	got, err := AuditEntries(context.Background(), cfg, "work", "delete", cutoff, 20)
	if err != nil {
		t.Fatalf("AuditEntries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if !strings.Contains(captured, "operation=eq.delete") {
		t.Errorf("operation filter missing: %s", captured)
	}
	if !strings.Contains(captured, "event_time=lt.") {
		t.Errorf("before cursor missing: %s", captured)
	}
}
