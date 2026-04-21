package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCheck_NilConfig(t *testing.T) {
	if _, err := HealthCheck(context.Background(), nil, HealthOptions{}); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestHealthCheck_NoBackend(t *testing.T) {
	cfg := &Config{Embedding: Embedding{Provider: "gemini", APIKey: "k"}}
	if _, err := HealthCheck(context.Background(), cfg, HealthOptions{}); err == nil {
		t.Error("expected backend resolution error")
	}
}

func TestHealthCheck_SupabaseHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/memories" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("apikey") == "" {
			t.Error("missing apikey header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_test",
		},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm_test"},
	}

	report, err := HealthCheck(context.Background(), cfg, HealthOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Errorf("expected OK report, got %+v", report)
	}
	if report.Backend != "supabase" {
		t.Errorf("backend = %q", report.Backend)
	}
	if len(report.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(report.Checks))
	}
}

func TestHealthCheck_SupabaseDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_test",
		},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm_test"},
	}

	report, err := HealthCheck(context.Background(), cfg, HealthOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Error("expected NOT-OK report")
	}
	// Find the backend check.
	var backend *CheckResult
	for i := range report.Checks {
		if strings.HasPrefix(report.Checks[i].Name, "backend:") {
			backend = &report.Checks[i]
		}
	}
	if backend == nil || backend.OK {
		t.Errorf("expected failing backend check, got %+v", backend)
	}
	if !strings.Contains(backend.Error, "503") {
		t.Errorf("error should surface status code: %q", backend.Error)
	}
}

func TestHealthCheck_EmbedderConfigValidationOnly(t *testing.T) {
	// Missing API key should fail the embedder probe without any HTTP call.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Embedding: Embedding{Provider: "gemini"}, // no APIKey
	}

	report, _ := HealthCheck(context.Background(), cfg, HealthOptions{})
	if report.OK {
		t.Error("expected NOT-OK due to missing embedder API key")
	}
	var emb *CheckResult
	for i := range report.Checks {
		if strings.HasPrefix(report.Checks[i].Name, "embedder:") {
			emb = &report.Checks[i]
		}
	}
	if emb == nil || emb.OK {
		t.Errorf("expected failing embedder probe, got %+v", emb)
	}
}

func TestHealthCheck_JSONShape(t *testing.T) {
	// Ensure the full Report marshals to valid JSON with the expected fields.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm_test"},
	}
	report, _ := HealthCheck(context.Background(), cfg, HealthOptions{})

	blob, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ok", "duration_ns", "backend", "checks"} {
		if _, ok := back[k]; !ok {
			t.Errorf("missing field %q in JSON report", k)
		}
	}
}

func TestSortChecksByName(t *testing.T) {
	checks := []CheckResult{
		{Name: "embedder:gemini"},
		{Name: "backend:supabase"},
	}
	sortChecksByName(checks)
	if checks[0].Name != "backend:supabase" || checks[1].Name != "embedder:gemini" {
		t.Errorf("sort failed: %+v", checks)
	}
}
