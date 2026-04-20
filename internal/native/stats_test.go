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
