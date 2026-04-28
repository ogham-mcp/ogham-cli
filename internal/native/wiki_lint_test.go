package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLintWiki_NilCfg(t *testing.T) {
	if _, err := LintWiki(context.Background(), nil, "p", LintWikiOptions{}); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestLintWiki_UnknownBackend(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "rocks"}}
	if _, err := LintWiki(context.Background(), cfg, "p", LintWikiOptions{}); err == nil {
		t.Error("expected error on unknown backend")
	}
}

func TestLintWikiOptions_SetIncludeDrift(t *testing.T) {
	var o LintWikiOptions
	if o.includeDriftSet {
		t.Error("zero value should leave includeDriftSet false")
	}
	o.SetIncludeDrift(false)
	if !o.includeDriftSet || o.IncludeDrift {
		t.Errorf("after SetIncludeDrift(false): IncludeDrift=%v set=%v", o.IncludeDrift, o.includeDriftSet)
	}
	o.SetIncludeDrift(true)
	if !o.IncludeDrift {
		t.Error("SetIncludeDrift(true) didn't take")
	}
}

// Routes the lint flow to a fake PostgREST and confirms each category's
// RPC is invoked, count + sample shape are filled in, and IssueCount /
// Healthy are computed correctly.
func TestLintWiki_SupabaseFullReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/rpc/wiki_lint_contradictions"):
			_, _ = w.Write([]byte(`[
				{"source_id":"a","target_id":"b","strength":0.8,"created_at":"2026-04-01T00:00:00Z","total_count":2},
				{"source_id":"c","target_id":"d","strength":0.7,"created_at":"2026-04-02T00:00:00Z","total_count":2}
			]`))
		case strings.HasSuffix(path, "/rpc/wiki_lint_orphans"):
			_, _ = w.Write([]byte(`[
				{"id":"x","content":"alone","tags":["t"],"created_at":"2026-04-01T00:00:00Z","total_count":1}
			]`))
		case strings.HasSuffix(path, "/rpc/wiki_lint_stale_lifecycle"):
			_, _ = w.Write([]byte(`[]`)) // 0 stale lifecycle
		case strings.HasSuffix(path, "/topic_summaries") && r.Method == http.MethodHead:
			// count=exact for stale_summaries
			w.Header().Set("Content-Range", "0-0/3")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(path, "/topic_summaries") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[
				{"id":"s1","topic_key":"foo","version":2,"stale_reason":"sweep","updated_at":"2026-04-10T00:00:00Z"}
			]`))
		case strings.HasSuffix(path, "/rpc/wiki_topic_list_fresh_for_drift"):
			// Deliberately empty -- separate test covers drift detection.
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}

	rep, err := LintWiki(context.Background(), cfg, "default", LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.Profile != "default" {
		t.Errorf("Profile = %q", rep.Profile)
	}
	if rep.Contradictions.Count != 2 || len(rep.Contradictions.Sample) != 2 {
		t.Errorf("contradictions: %+v", rep.Contradictions)
	}
	if rep.Orphans.Count != 1 {
		t.Errorf("orphans: %+v", rep.Orphans)
	}
	if rep.StaleLifecycle.Count != 0 {
		t.Errorf("stale_lifecycle: %+v", rep.StaleLifecycle)
	}
	if rep.StaleLifecycle.OlderThanDays != LintDefaultStableDays {
		t.Errorf("stale_lifecycle older_than_days = %d, want %d", rep.StaleLifecycle.OlderThanDays, LintDefaultStableDays)
	}
	if rep.StaleSummaries.Count != 3 {
		t.Errorf("stale_summaries: %+v", rep.StaleSummaries)
	}
	if rep.SummaryDrift.Count != 0 || rep.SummaryDrift.Skipped {
		t.Errorf("summary_drift: %+v", rep.SummaryDrift)
	}
	wantIssues := 2 + 1 + 0 + 3 + 0
	if rep.IssueCount != wantIssues {
		t.Errorf("IssueCount = %d, want %d", rep.IssueCount, wantIssues)
	}
	if rep.Healthy {
		t.Error("Healthy should be false when issues exist")
	}
}

// Drift skipping: includeDrift=false leaves SummaryDrift.Skipped=true
// and never hits the per-topic loop.
func TestLintWiki_SkipDrift(t *testing.T) {
	driftCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/rpc/wiki_topic_list_fresh_for_drift"):
			driftCalled = true
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(path, "/topic_summaries") && r.Method == http.MethodHead:
			w.Header().Set("Content-Range", "0-0/0")
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}
	opts := LintWikiOptions{}
	opts.SetIncludeDrift(false)
	rep, err := LintWiki(context.Background(), cfg, "default", opts)
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if !rep.SummaryDrift.Skipped {
		t.Error("SummaryDrift.Skipped should be true when caller opts out")
	}
	if driftCalled {
		t.Error("drift RPC should NOT be called when includeDrift=false")
	}
	if !rep.Healthy {
		t.Errorf("Healthy expected when no issues; got rep=%+v", rep)
	}
}

// Drift detection: stored hash mismatches recomputed hash -> drift entry.
func TestLintWiki_DriftDetected(t *testing.T) {
	// hash for ["m1","m2"] (sorted, newline-joined)
	want := sha256.Sum256([]byte("m1\nm2"))
	wantHex := hex.EncodeToString(want[:])

	// stored hash is deliberately different -- triggers drift.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/rpc/wiki_topic_list_fresh_for_drift"):
			_, _ = w.Write([]byte(`[
				{"id":"s1","topic_key":"foo","source_hash":"\\xdeadbeef"}
			]`))
		case strings.HasSuffix(path, "/rpc/wiki_recompute_get_source_ids"):
			_, _ = w.Write([]byte(`[{"id":"m1"},{"id":"m2"}]`))
		case strings.HasSuffix(path, "/topic_summaries") && r.Method == http.MethodHead:
			w.Header().Set("Content-Range", "0-0/0")
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}
	rep, err := LintWiki(context.Background(), cfg, "default", LintWikiOptions{})
	if err != nil {
		t.Fatalf("LintWiki: %v", err)
	}
	if rep.SummaryDrift.Count != 1 {
		t.Fatalf("Drift count = %d, want 1", rep.SummaryDrift.Count)
	}
	got := rep.SummaryDrift.Sample[0]
	if got["topic_key"] != "foo" {
		t.Errorf("topic_key = %v, want foo", got["topic_key"])
	}
	if got["current_source_count"] != 2 {
		t.Errorf("current_source_count = %v, want 2", got["current_source_count"])
	}
	// Sanity: the recomputed hash should round-trip correctly through
	// computeSourceHash (proves we'd detect a NON-drifted row).
	rec := computeSourceHash([]string{"m2", "m1"}) // unsorted input
	if hex.EncodeToString(rec) != wantHex {
		t.Errorf("computeSourceHash sort-instability: got %s want %s", hex.EncodeToString(rec), wantHex)
	}
}

func TestComputeSourceHash_EmptyDeterministic(t *testing.T) {
	a := computeSourceHash(nil)
	b := computeSourceHash([]string{})
	if !bytesEqual(a, b) {
		t.Error("nil vs empty slice should hash identically")
	}
}

func TestDecodePostgRESTBytea(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{`\xdeadbeef`, true, "deadbeef"},
		{`deadbeef`, true, "deadbeef"}, // unprefixed hex still parses
		{`not-hex`, false, ""},
		{``, true, ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := decodePostgRESTBytea(c.in)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
			if ok && hex.EncodeToString(got) != c.want {
				t.Errorf("got %s, want %s", hex.EncodeToString(got), c.want)
			}
		})
	}
}
