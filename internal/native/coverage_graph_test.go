package native

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Closes the graph + search Supabase gaps that kept internal/native/
// under 90%. Same httptest shape as coverage_supabase_test.go: assert
// the outgoing request body + canned response.

// --- linkUnlinkedSupabase ------------------------------------------------

func TestLinkUnlinkedSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/link_unlinked_memories") {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var args map[string]any
		if err := json.Unmarshal(body, &args); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		// Pin the RPC arg names -- Python backend uses filter_profile +
		// link_threshold; drift here silently breaks the Supabase path.
		if args["filter_profile"] != "work" {
			t.Errorf("filter_profile = %v", args["filter_profile"])
		}
		if args["link_threshold"] != 0.9 {
			t.Errorf("link_threshold = %v", args["link_threshold"])
		}
		// Return an integer -- the function scans as int.
		_, _ = w.Write([]byte("3"))
	})

	result, err := LinkUnlinked(context.Background(), cfg, LinkUnlinkedOptions{
		Profile:   "work",
		Threshold: 0.9,
		MaxLinks:  5,
		BatchSize: 50,
	})
	if err != nil {
		t.Fatalf("LinkUnlinked: %v", err)
	}
	if result.Processed != 3 {
		t.Errorf("Processed = %d, want 3", result.Processed)
	}
	if result.Status != "linked" {
		t.Errorf("Status = %q, want 'linked'", result.Status)
	}
}

// --- createRelationshipSupabase ------------------------------------------

func TestCreateRelationshipSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/memory_relationships") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if prefer := r.Header.Get("Prefer"); !strings.Contains(prefer, "merge-duplicates") {
			t.Errorf("Prefer missing merge-duplicates; got %q", prefer)
		}
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if row["relationship"] != "supports" {
			t.Errorf("relationship = %v", row["relationship"])
		}
		if row["source_id"] != "aaa" || row["target_id"] != "bbb" {
			t.Errorf("ids = %v / %v", row["source_id"], row["target_id"])
		}
		// PostgREST minimal response: empty array with 201.
		w.WriteHeader(201)
		_, _ = w.Write([]byte("[]"))
	})

	err := CreateRelationship(context.Background(), cfg, CreateRelationshipOptions{
		SourceID:     "aaa",
		TargetID:     "bbb",
		Relationship: "supports",
		Strength:     1.0,
	})
	if err != nil {
		t.Fatalf("CreateRelationship: %v", err)
	}
}

// --- exploreKnowledgeSupabase --------------------------------------------
//
// ExploreKnowledge embeds the query client-side via NewEmbedder before
// hitting the RPC. The Supabase test needs an Ollama stub too -- we
// spin up a sibling httptest for the embedder and route the config at
// both endpoints.

func TestExploreKnowledgeSupabase_RoundTrip(t *testing.T) {
	// Ollama embedder stub returns a 512-dim zero vector.
	ollama := newOllamaStubServer(t)

	// Supabase RPC stub -- expects explore_memory_graph invocation.
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/explore_memory_graph") {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var args map[string]any
		if err := json.Unmarshal(body, &args); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if args["query_text"] != "what do I know" {
			t.Errorf("query_text = %v", args["query_text"])
		}
		// traversal_depth + match_count pin the arg naming.
		if args["traversal_depth"] != float64(2) {
			t.Errorf("traversal_depth = %v", args["traversal_depth"])
		}
		if args["match_count"] != float64(5) {
			t.Errorf("match_count = %v", args["match_count"])
		}
		// Return one row to exercise parseGraphMemoriesJSON.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":        "abc-123",
				"content":   "seed match",
				"metadata":  map[string]any{"type": "fact"},
				"tags":      []string{"type:fact"},
				"relevance": 0.87,
				"depth":     0,
			},
		})
	})
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Model = "embeddinggemma"
	cfg.Embedding.Dimension = 512
	t.Setenv("OLLAMA_URL", ollama.URL)

	results, err := ExploreKnowledge(context.Background(), cfg, "what do I know", ExploreOptions{
		Depth:       2,
		MinStrength: 0.5,
		Limit:       5,
		Profile:     "work",
	})
	if err != nil {
		t.Fatalf("ExploreKnowledge: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Relevance != 0.87 {
		t.Errorf("Relevance = %v, want 0.87", results[0].Relevance)
	}
	if results[0].Metadata["type"] != "fact" {
		t.Errorf("Metadata round-trip broken: %+v", results[0].Metadata)
	}
}

// --- findRelatedSupabase -------------------------------------------------

func TestFindRelatedSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/get_related_memories") {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var args map[string]any
		if err := json.Unmarshal(body, &args); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if args["start_id"] != "abc" {
			t.Errorf("start_id = %v", args["start_id"])
		}
		// result_limit is the RPC-side name (not limit or max_results)
		if args["result_limit"] != float64(10) {
			t.Errorf("result_limit = %v", args["result_limit"])
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":            "xyz",
				"content":       "related",
				"tags":          []string{"alpha"},
				"confidence":    0.75,
				"depth":         1,
				"relationship":  "supports",
				"edge_strength": 0.9,
			},
		})
	})

	results, err := FindRelated(context.Background(), cfg, "abc", FindRelatedOptions{
		Depth: 1, MinStrength: 0.5, Limit: 10,
	})
	if err != nil {
		t.Fatalf("FindRelated: %v", err)
	}
	if len(results) != 1 || results[0].Relationship != "supports" {
		t.Errorf("results = %+v", results)
	}
	if results[0].EdgeStrength != 0.9 {
		t.Errorf("EdgeStrength = %v", results[0].EdgeStrength)
	}
}

// --- searchSupabase ------------------------------------------------------

func TestSearchSupabase_HybridSearchRPC(t *testing.T) {
	ollama := newOllamaStubServer(t)
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/hybrid_search_memories") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":        "search-hit",
				"content":   "match",
				"tags":      []string{"type:fact"},
				"metadata":  map[string]any{"note": "x"},
				"relevance": 0.92,
			},
		})
	})
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Model = "embeddinggemma"
	cfg.Embedding.Dimension = 512
	t.Setenv("OLLAMA_URL", ollama.URL)

	results, err := Search(context.Background(), cfg, "test query", SearchOptions{
		Limit: 5, Profile: "work",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != "search-hit" {
		t.Errorf("unexpected results: %+v", results)
	}
}

// --- helper: Ollama stub (used by both explore + search tests) ----------

// newOllamaStubServer returns an httptest.Server that answers every
// POST /api/embed with a 512-dim zero vector. Test-scoped cleanup via
// t.Cleanup so individual tests don't have to defer-Close.
func newOllamaStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vec := make([]float64, 512)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": vec})
	}))
	t.Cleanup(s.Close)
	return s
}
