package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ogham-mcp/ogham-cli/internal/gateway"
)

func TestBuildToolHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{
			"echoed_tool": body["tool"],
			"status":      "ok",
		})
	}))
	defer server.Close()

	client := gateway.New(server.URL, "test-key", "test/1.0")
	handler := BuildToolHandler(client, "test_tool")

	if handler == nil {
		t.Fatal("handler is nil")
	}
}

func TestManifestHash(t *testing.T) {
	tools1 := []map[string]any{{"name": "a"}, {"name": "b"}}
	tools2 := []map[string]any{{"name": "a"}, {"name": "b"}}
	tools3 := []map[string]any{{"name": "a"}, {"name": "c"}}

	h1 := ManifestHash(tools1)
	h2 := ManifestHash(tools2)
	h3 := ManifestHash(tools3)

	if h1 != h2 {
		t.Error("identical manifests should produce same hash")
	}
	if h1 == h3 {
		t.Error("different manifests should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(h1))
	}
}
