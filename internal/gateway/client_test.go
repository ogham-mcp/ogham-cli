package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("path = %q, want /api/v1/health", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	status, err := c.Health()
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if status["status"] != "ok" {
		t.Errorf("status = %v, want ok", status["status"])
	}
}

func TestCallTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/call" {
			t.Errorf("path = %q, want /api/v1/mcp/call", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		if r.Header.Get("User-Agent") != "test/1.0" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["tool"] != "store_memory" {
			t.Errorf("tool = %v, want store_memory", body["tool"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"status": "stored",
			"id":     "abc-123",
		})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.CallTool("store_memory", map[string]any{"content": "test"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result["status"] != "stored" {
		t.Errorf("result = %v", result)
	}
}

func TestFetchTools(t *testing.T) {
	tools := []map[string]any{
		{"name": "store_memory", "description": "Store a memory"},
		{"name": "hybrid_search", "description": "Search memories"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/tools" {
			t.Errorf("path = %q, want /api/v1/mcp/tools", r.URL.Path)
		}
		json.NewEncoder(w).Encode(tools)
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.FetchTools()
	if err != nil {
		t.Fatalf("FetchTools failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d tools, want 2", len(result))
	}
}

func TestAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "my-secret-key" {
			t.Errorf("X-Api-Key = %q, want %q", got, "my-secret-key")
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "my-secret-key", "test/1.0")
	c.Health()
}
