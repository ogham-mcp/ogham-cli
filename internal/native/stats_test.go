package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTopN(t *testing.T) {
	m := map[string]int64{
		"a": 10, "b": 5, "c": 20, "d": 5, "e": 1,
	}
	got := topN(m, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	// c:20 first, a:10 second, then b or d (tie-break alphabetical: b).
	if got[0].Name != "c" || got[0].Count != 20 {
		t.Errorf("first should be c:20, got %+v", got[0])
	}
	if got[1].Name != "a" || got[1].Count != 10 {
		t.Errorf("second should be a:10, got %+v", got[1])
	}
	if got[2].Name != "b" {
		t.Errorf("tie-break should prefer 'b' alphabetically, got %+v", got[2])
	}
}

func TestTopN_FewerThanN(t *testing.T) {
	m := map[string]int64{"a": 1, "b": 2}
	got := topN(m, 10)
	if len(got) != 2 {
		t.Errorf("expected 2, got %d", len(got))
	}
}

func TestStatsSupabase_ClientSideAggregation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/v1/memories") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		rows := []map[string]any{
			{"source": "claude-code", "tags": []string{"type:decision", "project:ogham"}},
			{"source": "claude-code", "tags": []string{"project:ogham"}},
			{"source": "hook:post-tool", "tags": []string{}},
			{"source": nil, "tags": nil}, // untagged + unknown source
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	stats, err := GetStats(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 4 {
		t.Errorf("total = %d, want 4", stats.Total)
	}
	// Two memories have empty/nil tags.
	if stats.Untagged != 2 {
		t.Errorf("untagged = %d, want 2", stats.Untagged)
	}
	// Top source: claude-code with 2.
	if len(stats.Sources) < 2 || stats.Sources[0].Name != "claude-code" || stats.Sources[0].Count != 2 {
		t.Errorf("top source wrong: %+v", stats.Sources)
	}
	// Top tag: project:ogham with 2.
	if len(stats.TopTags) == 0 || stats.TopTags[0].Name != "project:ogham" || stats.TopTags[0].Count != 2 {
		t.Errorf("top tag wrong: %+v", stats.TopTags)
	}
}

// TestStatsSupabase_ConnectedAndDecay exercises the two new headline
// numbers through the PostgREST transport. Four memories with ids a-d:
// a+b are wired via a single relationship (so ConnectedPct = 50%);
// c has confidence 0.1 (below DecayThreshold); the fourth row omits
// confidence entirely (nil -- should not count toward decay).
func TestStatsSupabase_ConnectedAndDecay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memories"):
			rows := []map[string]any{
				{"id": "a", "source": "claude-code", "tags": []string{"x"}, "confidence": 0.5},
				{"id": "b", "source": "claude-code", "tags": []string{"x"}, "confidence": 0.5},
				{"id": "c", "source": "cli", "tags": []string{"y"}, "confidence": 0.1},
				{"id": "d", "source": "cli", "tags": []string{"y"}}, // nil confidence
			}
			_ = json.NewEncoder(w).Encode(rows)
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memory_relationships"):
			// Respond with a single edge a -> b no matter which column
			// the handler asks for. connectedCountSupabase only reads
			// the requested column out of the row so the other field
			// is harmless noise.
			q := r.URL.Query()
			if q.Get("select") == "source_id" {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"source_id": "a"}})
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"target_id": "b"}})
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	stats, err := GetStats(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 4 {
		t.Errorf("total = %d, want 4", stats.Total)
	}
	// a + b are the touched ids => 2 of 4 connected => 50%.
	if stats.ConnectedPct != 50.0 {
		t.Errorf("connected pct = %v, want 50", stats.ConnectedPct)
	}
	// Only c (0.1) is below DecayThreshold (0.25). d's nil confidence
	// must NOT be counted as decayed -- absence is not decay.
	if stats.DecayCount != 1 {
		t.Errorf("decay count = %d, want 1", stats.DecayCount)
	}
}

func TestStatsSupabase_ConnectedPctZeroOnEmptyProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty profile: zero memories, no relationships endpoint hit.
		if strings.HasPrefix(r.URL.Path, "/rest/v1/memory_relationships") {
			t.Errorf("relationships must not be fetched when profile is empty")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "empty",
	}
	stats, err := GetStats(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 0 {
		t.Errorf("total = %d, want 0", stats.Total)
	}
	if stats.ConnectedPct != 0 {
		t.Errorf("connected pct = %v, want 0 for empty profile", stats.ConnectedPct)
	}
	if stats.DecayCount != 0 {
		t.Errorf("decay count = %d, want 0 for empty profile", stats.DecayCount)
	}
}
