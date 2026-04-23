package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// statsSupabaseHandler is a shared httptest handler that serves the mixed
// HEAD-count + GET-rows traffic statsSupabase now produces. The caller
// supplies the four scalar counts (total / untagged / withTTL / decay),
// the page of rows used for source+tag aggregation and the active-id
// set, and the relationship edges for the Connected% numerator.
//
// The handler keeps statsSupabase's shape honest: every route that
// returns a count must be HEAD, every route that returns rows must be
// GET, and HEAD paths must echo a Content-Range header driven by the
// Prefer: count=exact request.
type statsFixture struct {
	total          int64
	untagged       int64
	emptyTags      int64
	withTTL        int64
	expiring       int64
	decay          int64
	rows           []map[string]any
	relationships  []map[string]string
	expectProfile  string
	relationshipsT *testing.T // fail if relationships are fetched when unexpected
	skipRels       bool
}

func newStatsFixtureHandler(t *testing.T, fx statsFixture) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memories"):
			q := r.URL.Query()
			if r.Method == http.MethodHead {
				// Which scalar count? Disambiguate by filter.
				var n int64
				switch {
				case q.Get("tags") == "is.null":
					n = fx.untagged
				case q.Get("tags") == "eq.{}":
					n = fx.emptyTags
				case q.Get("confidence") != "":
					n = fx.decay
				case q.Get("expires_at") != "" && len(q["expires_at"]) > 1:
					// WithTTL + expiring filter adds a second expires_at
					// (lt.<timestamp>). Length > 1 => expiring.
					n = fx.expiring
				case q.Get("expires_at") == "not.is.null":
					n = fx.withTTL
				default:
					n = fx.total
				}
				w.Header().Set("Content-Range", fmt.Sprintf("0-0/%d", n))
				if n == 0 {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				} else {
					w.WriteHeader(http.StatusPartialContent)
				}
				return
			}
			// GET rows: paginated by Range. First call returns all the
			// fixture rows; subsequent calls return []. Managed Supabase
			// would emit 206 on the first page and a short read terminates.
			rng := r.Header.Get("Range")
			if rng == "" || strings.HasPrefix(rng, "0-") {
				_ = json.NewEncoder(w).Encode(fx.rows)
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memory_relationships"):
			// connectedCountSupabase pages in 1000-row chunks with ?offset.
			// skipRels tests pass no edges -- the test still calls into
			// connectedCountSupabase but the intersect comes out at 0.
			if r.URL.Query().Get("offset") == "0" || r.URL.Query().Get("offset") == "" {
				if fx.skipRels {
					_ = json.NewEncoder(w).Encode([]map[string]string{})
				} else {
					_ = json.NewEncoder(w).Encode(fx.relationships)
				}
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]string{})
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestStatsSupabase_ClientSideAggregation(t *testing.T) {
	fx := statsFixture{
		total:     4,
		untagged:  1, // tags=IS NULL
		emptyTags: 1, // tags={}
		rows: []map[string]any{
			{"id": "a", "source": "claude-code", "tags": []string{"type:decision", "project:ogham"}},
			{"id": "b", "source": "claude-code", "tags": []string{"project:ogham"}},
			{"id": "c", "source": "hook:post-tool", "tags": []string{}},
			{"id": "d", "source": nil, "tags": nil},
		},
		skipRels: true, // aggregation-only test, no edges needed
	}
	server := httptest.NewServer(newStatsFixtureHandler(t, fx))
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
	// Untagged splits across two HEAD calls (IS NULL + eq.{}) and sums
	// to 2.
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
// confidence entirely (nil -- should not count toward decay, so the
// fixture reports decay=1).
func TestStatsSupabase_ConnectedAndDecay(t *testing.T) {
	fx := statsFixture{
		total: 4,
		decay: 1,
		rows: []map[string]any{
			{"id": "a", "source": "claude-code", "tags": []string{"x"}},
			{"id": "b", "source": "claude-code", "tags": []string{"x"}},
			{"id": "c", "source": "cli", "tags": []string{"y"}},
			{"id": "d", "source": "cli", "tags": []string{"y"}},
		},
		relationships: []map[string]string{
			{"source_id": "a", "target_id": "b"},
		},
	}
	server := httptest.NewServer(newStatsFixtureHandler(t, fx))
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
	// decay=1 reported via HEAD -- the PostgREST `confidence=lt.0.25`
	// filter excludes nil confidence rows at the server, matching the
	// Postgres path's behaviour.
	if stats.DecayCount != 1 {
		t.Errorf("decay count = %d, want 1", stats.DecayCount)
	}
}

func TestStatsSupabase_ConnectedPctZeroOnEmptyProfile(t *testing.T) {
	fx := statsFixture{
		total:    0,
		rows:     []map[string]any{},
		skipRels: true,
	}
	server := httptest.NewServer(newStatsFixtureHandler(t, fx))
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

// TestStatsSupabase_TotalFromHeadCount is the specific regression for the
// "1,000 memories" dashboard bug: Supabase managed caps each GET at 1000
// rows regardless of explicit ?limit=, so statsSupabase must derive Total
// from HEAD + count=exact, not len(response_array). The fixture serves
// 1000 rows but advertises total=1034 on the HEAD -- a correct client
// reports 1034, a broken one falls back to 1000.
func TestStatsSupabase_TotalFromHeadCount(t *testing.T) {
	// Synthesise 1000 rows -- the per-request cap the bug rides on.
	rows := make([]map[string]any, 1000)
	for i := range rows {
		rows[i] = map[string]any{
			"id":     fmt.Sprintf("id-%d", i),
			"source": "claude-code",
			"tags":   []string{"project:ogham"},
		}
	}
	fx := statsFixture{
		total:    1034,
		rows:     rows,
		skipRels: true, // keep the test focused on Total
	}
	server := httptest.NewServer(newStatsFixtureHandler(t, fx))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	stats, err := GetStats(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1034 {
		t.Errorf("Total = %d, want 1034 (regression: count must come from Content-Range, not len(rows))", stats.Total)
	}
}

// TestStoreCountsByDaySupabase_Paginates drops enough rows across the
// managed-Supabase 1000-row cap that the handler MUST page via Range to
// cover them all. A one-shot GET would only see the first page and
// report <1500 in the buckets. The fixture records page starts it saw.
func TestStoreCountsByDaySupabase_Paginates(t *testing.T) {
	day0 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	total := 1500 // crosses the 1000-row cap
	rows := make([]map[string]any, total)
	for i := range rows {
		rows[i] = map[string]any{"created_at": day0.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
	}
	var rangesSeen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/v1/memories") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		rng := r.Header.Get("Range")
		rangesSeen = append(rangesSeen, rng)
		// Parse "start-end" and return the slice.
		var start, end int
		if _, err := fmt.Sscanf(rng, "%d-%d", &start, &end); err != nil {
			t.Errorf("bad Range header %q: %v", rng, err)
		}
		if start >= len(rows) {
			w.Header().Set("Content-Range", fmt.Sprintf("*/%d", len(rows)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		stop := end + 1
		if stop > len(rows) {
			stop = len(rows)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("%d-%d/%d", start, stop-1, len(rows)))
		if stop-start == len(rows) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}
		_ = json.NewEncoder(w).Encode(rows[start:stop])
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Profile:  "work",
	}
	got, err := StoreCountsByDay(context.Background(), cfg, 30)
	if err != nil {
		t.Fatalf("StoreCountsByDay: %v", err)
	}
	var seen int64
	for _, d := range got {
		seen += d.Count
	}
	if seen != int64(total) {
		t.Errorf("bucket total = %d, want %d (pagination missed rows)", seen, total)
	}
	if len(rangesSeen) < 2 {
		t.Errorf("expected pagination (>=2 range requests), got %d: %v", len(rangesSeen), rangesSeen)
	}
}
