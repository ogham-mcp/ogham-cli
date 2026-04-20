package native

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Build-only coverage: confirm that selecting "openai" with all required
// config fields produces the right *openaiEmbedder shape.
func TestNewEmbedder_OpenAIDefaults(t *testing.T) {
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 512,
	}})
	if err != nil {
		t.Fatalf("openai embedder failed to build: %v", err)
	}
	if e.Name() != "openai/text-embedding-3-small" {
		t.Errorf("default model = %q", e.Name())
	}
	if e.Dimension() != 512 {
		t.Errorf("dim = %d", e.Dimension())
	}
	oe := e.(*openaiEmbedder)
	if oe.baseURL != "https://api.openai.com" {
		t.Errorf("default baseURL = %q", oe.baseURL)
	}
}

func TestNewEmbedder_OpenAIMissingKey(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		Dimension: 512,
	}})
	if err == nil {
		t.Error("expected error when OPENAI_API_KEY is missing")
	}
}

// Custom base URL (Azure proxy / LocalAI) is propagated via
// cfg.Embedding.BaseURL. Trailing slash is stripped.
func TestNewEmbedder_OpenAIBaseURLOverride(t *testing.T) {
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 512,
		BaseURL:   "https://azure.example.com/v1/deployments/my/openai/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	oe := e.(*openaiEmbedder)
	if oe.baseURL != "https://azure.example.com/v1/deployments/my/openai" {
		t.Errorf("baseURL = %q (trailing slash should be stripped)", oe.baseURL)
	}
}

// Round-trip: spin up an httptest.Server that speaks the OpenAI wire
// shape, point the embedder at it, check the request bytes the server
// receives AND that the response vector is returned faithfully.
func TestOpenAIEmbed_RoundTrip(t *testing.T) {
	wantVec := make([]float32, 512)
	for i := range wantVec {
		wantVec[i] = float32(i) * 0.002
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s, want /v1/embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header = %q", got)
		}

		body, _ := io.ReadAll(r.Body)
		var req openaiEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req.Model != "text-embedding-3-small" {
			t.Errorf("model = %q", req.Model)
		}
		if req.Input != "hello world" {
			t.Errorf("input = %q", req.Input)
		}
		if req.Dimensions != 512 {
			t.Errorf("dimensions = %d", req.Dimensions)
		}
		resp := openaiEmbedResponse{
			Data: []openaiEmbeddingItem{{Embedding: wantVec, Index: 0}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 512,
		BaseURL:   server.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 512 {
		t.Fatalf("returned len = %d", len(got))
	}
	// Spot-check a few entries rather than iterating the whole vector.
	for _, idx := range []int{0, 100, 511} {
		if got[idx] != wantVec[idx] {
			t.Errorf("vec[%d] = %f, want %f", idx, got[idx], wantVec[idx])
		}
	}
}

// Non-200 JSON error envelope is surfaced typed.
func TestOpenAIEmbed_ErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{
			Error: &openaiAPIError{
				Message: "Incorrect API key provided",
				Type:    "invalid_request_error",
				Code:    "invalid_api_key",
			},
		})
	}))
	defer server.Close()

	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-bogus",
		Dimension: 512,
		BaseURL:   server.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.Embed(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"invalid_request_error", "Incorrect API key", "invalid_api_key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

// Non-200 with non-JSON body: fall back to raw-truncated body in error.
func TestOpenAIEmbed_NonJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer server.Close()

	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider: "openai", APIKey: "x", Dimension: 512, BaseURL: server.URL,
	}})
	_, err := e.Embed(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 502")
	}
	// When parse succeeds (it will: the body won't JSON-parse so we
	// hit the http-%d branch), the truncated body should appear.
	if !strings.Contains(err.Error(), "openai embed:") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// Empty data array is a silent server bug -- surface it loudly.
func TestOpenAIEmbed_EmptyDataArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{Data: nil})
	}))
	defer server.Close()

	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider: "openai", APIKey: "x", Dimension: 512, BaseURL: server.URL,
	}})
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "empty data") {
		t.Errorf("expected empty-data error, got %v", err)
	}
}

// Server returns a vector with the wrong dim -> caller must know.
func TestOpenAIEmbed_DimMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vec := make([]float32, 1536) // ada-002-style fixed dim
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{
			Data: []openaiEmbeddingItem{{Embedding: vec}},
		})
	}))
	defer server.Close()

	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider: "openai", APIKey: "x", Dimension: 512, BaseURL: server.URL,
	}})
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "dim=512") {
		t.Errorf("expected dim-mismatch error, got %v", err)
	}
}

// Empty input is rejected client-side -- no wasted API call.
func TestOpenAIEmbed_EmptyInputRejected(t *testing.T) {
	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider: "openai", APIKey: "x", Dimension: 512,
	}})
	_, err := e.Embed(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "empty text") {
		t.Errorf("expected empty-text error, got %v", err)
	}
}
