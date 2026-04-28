package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Hermetic Supabase happy-path test. Spins up an httptest.Server that
// stands in for PostgREST, confirms QueryTopicSummary issues the right
// RPC POST, parses the SETOF topic_summaries response, and projects
// the requested level cleanly.
func TestQueryTopicSummary_SupabaseHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/rpc/wiki_topic_get_by_key") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("apikey"); got == "" {
			t.Error("apikey header missing")
		}

		// Decode args, confirm shape.
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Fatalf("decode args: %v", err)
		}
		if args["p_profile"] != "default" || args["p_topic_key"] != "ogham" {
			t.Errorf("unexpected args: %#v", args)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id": "11111111-1111-1111-1111-111111111111",
			"topic_key": "ogham",
			"profile_id": "default",
			"content": "long body of the summary",
			"tldr_one_line": "one-liner",
			"tldr_short": "short paragraph",
			"source_count": 4,
			"source_cursor": null,
			"source_hash": "\\xdeadbeef",
			"token_count": 800,
			"importance": 0.7,
			"model_used": "claude-opus",
			"version": 3,
			"status": "fresh",
			"created_at": "2026-04-25T10:00:00Z",
			"updated_at": "2026-04-26T10:00:00Z",
			"stale_reason": null
		}]`))
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}

	res, err := QueryTopicSummary(context.Background(), cfg, "default", "ogham", LevelShort)
	if err != nil {
		t.Fatalf("QueryTopicSummary: %v", err)
	}
	if res.Status != StatusOK {
		t.Errorf("Status = %q, want %q", res.Status, StatusOK)
	}
	if res.Body != "short paragraph" {
		t.Errorf("Body = %q, want %q", res.Body, "short paragraph")
	}
	if res.Level != string(LevelShort) {
		t.Errorf("Level = %q, want short", res.Level)
	}
	if res.RequestedLevel != "" {
		t.Errorf("RequestedLevel = %q, want empty (Level matched request)", res.RequestedLevel)
	}
	if res.SourceHash != "deadbeef" {
		t.Errorf("SourceHash = %q, want deadbeef (prefix stripped)", res.SourceHash)
	}
}

// Pre-033 row: TLDR fields null, request short -> falls back to body
// AND populates RequestedLevel so the caller knows.
func TestQueryTopicSummary_FallbackToBodyOnPre033Row(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id": "22222222-2222-2222-2222-222222222222",
			"topic_key": "old",
			"profile_id": "default",
			"content": "v0.12-era body only",
			"tldr_one_line": null,
			"tldr_short": null,
			"source_count": 1,
			"source_hash": "\\xcafebabe",
			"importance": 0.5,
			"model_used": "claude-haiku",
			"version": 1,
			"status": "fresh",
			"created_at": "2026-03-01T00:00:00Z",
			"updated_at": "2026-03-01T00:00:00Z"
		}]`))
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}

	res, err := QueryTopicSummary(context.Background(), cfg, "", "old", LevelOneLine)
	if err != nil {
		t.Fatalf("QueryTopicSummary: %v", err)
	}
	if res.Body != "v0.12-era body only" {
		t.Errorf("Body = %q, want body fallback", res.Body)
	}
	if res.Level != string(LevelBody) {
		t.Errorf("Level served = %q, want body", res.Level)
	}
	if res.RequestedLevel != string(LevelOneLine) {
		t.Errorf("RequestedLevel = %q, want one_line (signals fallback)", res.RequestedLevel)
	}
}

// Empty row set => not_cached.
func TestQueryTopicSummary_NotCached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}

	res, err := QueryTopicSummary(context.Background(), cfg, "default", "missing", LevelBody)
	if err != nil {
		t.Fatalf("QueryTopicSummary: %v", err)
	}
	if res.Status != StatusNotCached {
		t.Errorf("Status = %q, want not_cached", res.Status)
	}
	if !strings.Contains(res.Message, "compile_wiki") {
		t.Errorf("Message should hint at compile_wiki; got %q", res.Message)
	}
}

func TestQueryTopicSummary_NilCfg(t *testing.T) {
	if _, err := QueryTopicSummary(context.Background(), nil, "p", "t", LevelBody); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestQueryTopicSummary_EmptyTopic(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: "postgres://x"}}
	if _, err := QueryTopicSummary(context.Background(), cfg, "p", "", LevelBody); err == nil {
		t.Error("expected error on empty topic")
	}
}

func TestQueryTopicSummary_UnknownBackend(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "rocks"}}
	if _, err := QueryTopicSummary(context.Background(), cfg, "p", "t", LevelBody); err == nil {
		t.Error("expected error on unknown backend")
	}
}

func TestSupabaseTopicRow_HashStripsPrefix(t *testing.T) {
	pref := `\xdeadbeef`
	row := supabaseTopicRow{
		ID:         "id",
		TopicKey:   "k",
		ProfileID:  "p",
		Content:    "c",
		SourceHash: &pref,
	}
	got := row.toTopicSummary()
	if got.SourceHash != "deadbeef" {
		t.Errorf("SourceHash = %q, want deadbeef", got.SourceHash)
	}
}

func TestSupabaseTopicRow_HashUnparseableDropped(t *testing.T) {
	bad := `not-hex`
	row := supabaseTopicRow{SourceHash: &bad}
	got := row.toTopicSummary()
	if got.SourceHash != "" {
		t.Errorf("unparseable hash should drop; got %q", got.SourceHash)
	}
}
