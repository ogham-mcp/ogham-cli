package native

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestSupabase_PipelineCounts_HappyPath stands up a fake PostgREST and
// verifies the probe + three HEAD requests flow. Each HEAD returns a
// Content-Range header that the function must parse into the stage count.
func TestSupabase_PipelineCounts_HappyPath(t *testing.T) {
	var probeCount, freshCount, stableCount, editingCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		// Every HEAD call must carry Prefer: count=exact.
		if got := r.Header.Get("Prefer"); got != "count=exact" {
			t.Errorf("Prefer header = %q, want count=exact", got)
		}
		// auth headers present
		if r.Header.Get("apikey") == "" || r.Header.Get("Authorization") == "" {
			t.Errorf("auth headers missing: apikey=%q auth=%q",
				r.Header.Get("apikey"), r.Header.Get("Authorization"))
		}

		q := r.URL.Query()
		stage := strings.TrimPrefix(q.Get("stage"), "eq.")
		switch {
		case !strings.HasPrefix(r.URL.Path, "/rest/v1/memory_lifecycle"):
			t.Errorf("unexpected path: %s", r.URL.Path)
		case stage == "":
			// Probe hit -- no stage filter, no profile filter expected.
			probeCount++
			w.Header().Set("Content-Range", "0-0/9999")
			w.WriteHeader(http.StatusPartialContent)
		case stage == "fresh":
			freshCount++
			if got := q.Get("profile"); got != "eq.work" {
				t.Errorf("fresh profile filter = %q", got)
			}
			w.Header().Set("Content-Range", "0-0/6633")
			w.WriteHeader(http.StatusPartialContent)
		case stage == "stable":
			stableCount++
			w.Header().Set("Content-Range", "0-0/12")
			w.WriteHeader(http.StatusPartialContent)
		case stage == "editing":
			editingCount++
			// Empty bucket -- PostgREST returns 416 Range Not Satisfiable
			// with Content-Range: */0 for a zero-row count.
			w.Header().Set("Content-Range", "*/0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			t.Errorf("unexpected stage filter: %q", stage)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_test"},
		Profile:  "work",
	}
	counts, err := PipelineCounts(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("PipelineCounts: %v", err)
	}
	if counts["fresh"] != 6633 {
		t.Errorf("fresh = %d, want 6633", counts["fresh"])
	}
	if counts["stable"] != 12 {
		t.Errorf("stable = %d, want 12", counts["stable"])
	}
	if counts["editing"] != 0 {
		t.Errorf("editing = %d, want 0", counts["editing"])
	}
	if probeCount != 1 {
		t.Errorf("probe hit %d times, want 1", probeCount)
	}
	if freshCount != 1 || stableCount != 1 || editingCount != 1 {
		t.Errorf("stage hit counts: fresh=%d stable=%d editing=%d", freshCount, stableCount, editingCount)
	}
}

// TestSupabase_PipelineCounts_PreMigrationFallback simulates a pre-026
// DB where /rest/v1/memory_lifecycle returns 404. The function must fall
// back to counting /rest/v1/memories and bucket the total under 'fresh'.
func TestSupabase_PipelineCounts_PreMigrationFallback(t *testing.T) {
	var memoriesHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memory_lifecycle"):
			// PostgREST 404 for missing relation. No Content-Range header.
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/rest/v1/memories"):
			memoriesHits++
			if got := r.URL.Query().Get("profile"); got != "eq.work" {
				t.Errorf("fallback profile filter = %q", got)
			}
			w.Header().Set("Content-Range", "0-0/42")
			w.WriteHeader(http.StatusPartialContent)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_test"},
		Profile:  "work",
	}
	counts, err := PipelineCounts(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("PipelineCounts with pre-migration DB: %v", err)
	}
	if counts["fresh"] != 42 || counts["stable"] != 0 || counts["editing"] != 0 {
		t.Errorf("fallback counts wrong: %+v", counts)
	}
	if memoriesHits != 1 {
		t.Errorf("fallback /memories hit %d times, want 1", memoriesHits)
	}
}

// TestSupabase_PipelineCounts_EmptyProfile exercises the */0 Content-Range
// shape that PostgREST emits for empty result sets. All three stages must
// read as zero without error.
func TestSupabase_PipelineCounts_EmptyProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Probe returns a real count so we stay on the happy path.
		if r.URL.Query().Get("stage") == "" {
			w.Header().Set("Content-Range", "0-0/1")
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		// Stage-filtered counts all empty.
		w.Header().Set("Content-Range", "*/0")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_test"},
		Profile:  "empty",
	}
	counts, err := PipelineCounts(context.Background(), cfg, "empty")
	if err != nil {
		t.Fatalf("PipelineCounts: %v", err)
	}
	for _, stage := range []string{"fresh", "stable", "editing"} {
		if counts[stage] != 0 {
			t.Errorf("%s = %d, want 0", stage, counts[stage])
		}
	}
}

// TestSupabase_PipelineCounts_MissingContentRange surfaces a clean error
// when PostgREST returns 200 but no Content-Range header (e.g. a
// misconfigured proxy strips it).
func TestSupabase_PipelineCounts_MissingContentRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intentionally no Content-Range.
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_secret_test"},
		Profile:  "work",
	}
	_, err := PipelineCounts(context.Background(), cfg, "work")
	if err == nil {
		t.Fatal("expected error on missing Content-Range, got nil")
	}
	if !strings.Contains(err.Error(), "Content-Range") {
		t.Errorf("error should mention Content-Range, got: %v", err)
	}
}

func TestIsRelationNotFound(t *testing.T) {
	cases := []struct {
		in   error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("relation \"memory_lifecycle\" does not exist"), true},
		{fmt.Errorf("supabase HEAD foo: http 404: relation not found"), true},
		{fmt.Errorf("PGRST code 42P01: undefined_table"), true},
		{fmt.Errorf("connection refused"), false},
		{fmt.Errorf("http 500"), false},
	}
	for _, tc := range cases {
		if got := isRelationNotFound(tc.in); got != tc.want {
			t.Errorf("isRelationNotFound(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSupabaseLive_PipelineCounts is a smoke test against a real Supabase
// project. Skipped unless SUPABASE_URL + SUPABASE_KEY are set in the env.
// Not guarded by a build tag -- follows the same "skip if env missing"
// pattern used by embedding_*_test.go for provider credentials.
func TestSupabaseLive_PipelineCounts(t *testing.T) {
	url := os.Getenv("SUPABASE_URL")
	key := os.Getenv("SUPABASE_KEY")
	if url == "" || key == "" {
		t.Skip("SUPABASE_URL / SUPABASE_KEY not set; skipping live test")
	}
	profile := os.Getenv("OGHAM_LIVE_PROFILE")
	if profile == "" {
		profile = "work"
	}

	cfg := &Config{
		Database: Database{SupabaseURL: url, SupabaseKey: key},
		Profile:  profile,
	}
	counts, err := PipelineCounts(context.Background(), cfg, profile)
	if err != nil {
		t.Fatalf("live PipelineCounts(%s): %v", profile, err)
	}
	t.Logf("live pipeline counts for profile=%s: %+v", profile, counts)
	for _, stage := range []string{"fresh", "stable", "editing"} {
		if _, ok := counts[stage]; !ok {
			t.Errorf("missing stage bucket: %s", stage)
		}
	}
	total := counts["fresh"] + counts["stable"] + counts["editing"]
	if total == 0 {
		t.Errorf("all three buckets zero for profile=%s -- expected >0 for OpenBrain", profile)
	}
}
