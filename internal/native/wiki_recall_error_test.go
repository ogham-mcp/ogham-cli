package native

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Connect-failure unit tests. The pgx.Connect call needs a real address
// to fail against -- using a non-routable port (9999 on localhost is
// usually closed) gives a fast, deterministic refusal. These tests
// don't talk to a database; they only verify the error wrapping that
// turns "could not connect" into a CLI-friendly message.

const badPostgresDSN = "postgres://nobody@127.0.0.1:1/nodb?connect_timeout=1"

func TestQueryTopicSummary_PostgresConnectError(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: badPostgresDSN}}
	_, err := QueryTopicSummary(context.Background(), cfg, "p", "t", LevelBody)
	if err == nil {
		t.Error("expected connect error")
	}
	if !strings.Contains(err.Error(), "query_topic_summary") {
		t.Errorf("error should be tagged with verb name; got %v", err)
	}
}

func TestWalkKnowledge_PostgresConnectError(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: badPostgresDSN}}
	_, err := WalkKnowledge(context.Background(), cfg, "11111111-1111-1111-1111-111111111111",
		WalkKnowledgeOptions{Depth: 1})
	if err == nil {
		t.Error("expected connect error")
	}
	if !strings.Contains(err.Error(), "walk_knowledge") {
		t.Errorf("error should be tagged; got %v", err)
	}
}

func TestLintWiki_PostgresConnectError(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: badPostgresDSN}}
	_, err := LintWiki(context.Background(), cfg, "p", LintWikiOptions{})
	if err == nil {
		t.Error("expected connect error")
	}
	if !strings.Contains(err.Error(), "lint_wiki") {
		t.Errorf("error should be tagged; got %v", err)
	}
}

func TestAdvanceLifecycle_PostgresConnectError(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: badPostgresDSN}}
	_, err := AdvanceLifecycle(context.Background(), cfg, "p", AdvanceLifecycleOptions{})
	if err == nil {
		t.Error("expected connect error")
	}
	if !strings.Contains(err.Error(), "advance_lifecycle") {
		t.Errorf("error should be tagged; got %v", err)
	}
}

// Supabase RPC error: 500 from PostgREST surfaces with the verb name.
func TestQueryTopicSummary_SupabaseRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"hint":"intentional"}`))
	}))
	defer server.Close()
	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
	}
	_, err := QueryTopicSummary(context.Background(), cfg, "p", "t", LevelBody)
	if err == nil {
		t.Error("expected RPC error")
	}
}

func TestWalkKnowledge_SupabaseRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
	}
	_, err := WalkKnowledge(context.Background(), cfg, "11111111-1111-1111-1111-111111111111",
		WalkKnowledgeOptions{Depth: 1})
	if err == nil {
		t.Error("expected RPC error")
	}
}

func TestLintWiki_SupabaseRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
	}
	_, err := LintWiki(context.Background(), cfg, "p", LintWikiOptions{})
	if err == nil {
		t.Error("expected RPC error")
	}
}

// Garbled JSON in the RPC response: parse error caught + reported.
func TestQueryTopicSummary_SupabaseGarbledJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer server.Close()
	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
	}
	_, err := QueryTopicSummary(context.Background(), cfg, "p", "t", LevelBody)
	if err == nil {
		t.Error("expected parse error")
	}
}

// LintCategory empty-rows: parseLintCountSample on an empty array
// returns an empty category, not nil.
func TestParseLintCountSample_EmptyArray(t *testing.T) {
	cat := parseLintCountSample([]byte(`[]`), []string{"id"})
	if cat.Count != 0 {
		t.Errorf("Count = %d, want 0", cat.Count)
	}
	if cat.Sample == nil {
		t.Error("Sample should be empty slice, not nil (JSON shape stability)")
	}
}

// Mixed-version probe: Supabase HEAD returning 404 for memory_lifecycle
// (lint case) returns the Skipped/healthy path, not an error.
// Already covered by TestAdvanceLifecycle_SupabaseLifecycleAbsent;
// this test just stresses the same probe as part of a full LintWiki.
func TestLintWiki_SupabaseHandlesMissingTables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodHead && strings.HasSuffix(path, "/topic_summaries"):
			// Pre-028 cluster: table doesn't exist.
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(path, "/rpc/"):
			_, _ = w.Write([]byte(`[]`))
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
	if rep.StaleSummaries.Count != 0 {
		t.Errorf("missing topic_summaries should yield 0 count; got %d", rep.StaleSummaries.Count)
	}
}
