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

func TestNewEmbedder_MistralDefaults(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "mistral",
		APIKey:    "mis-test",
		Dimension: 1024,
	}})
	if err != nil {
		t.Fatalf("mistral embedder failed to build: %v", err)
	}
	if e.Name() != "mistral/mistral-embed" {
		t.Errorf("default model name = %q", e.Name())
	}
	if e.Dimension() != 1024 {
		t.Errorf("dim = %d, want 1024", e.Dimension())
	}
	me := e.(*mistralEmbedder)
	if me.baseURL != "https://api.mistral.ai" {
		t.Errorf("default baseURL = %q", me.baseURL)
	}
}

func TestNewEmbedder_MistralMissingKey(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "mistral",
		Dimension: 1024,
	}})
	if err == nil || !strings.Contains(err.Error(), "MISTRAL_API_KEY") {
		t.Errorf("expected MISTRAL_API_KEY error, got %v", err)
	}
}

// mistral-embed is a fixed-1024 model. Any other dim must fail at
// construction rather than at every call, so misconfigurations surface
// once and loudly instead of silently burning tokens on doomed requests.
func TestNewEmbedder_MistralRejectsBadDim(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "mistral",
		APIKey:    "mis-test",
		Dimension: 512,
	}})
	if err == nil || !strings.Contains(err.Error(), "1024") {
		t.Errorf("expected dim-mismatch error for mistral-embed@512, got %v", err)
	}
}

func TestNewEmbedder_MistralCustomModelAllowsSmallerDim(t *testing.T) {
	// A non-default Mistral model (stand-in) should not be guarded
	// against sub-1024 dims -- only mistral-embed has the fixed-dim
	// contract that warrants rejecting at construction.
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "mistral",
		APIKey:    "mis-test",
		Dimension: 512,
		Model:     "hypothetical-mistral-embed-mrl",
	}})
	if err != nil {
		t.Fatalf("unexpected error for non-default Mistral model: %v", err)
	}
	if e.Dimension() != 512 {
		t.Errorf("dim = %d, want 512", e.Dimension())
	}
}

// Round-trip against a fake server: confirms output_dimension is NOT
// sent for the default mistral-embed model (API returns 400 if it is).
func TestMistralEmbed_RoundTrip(t *testing.T) {
	wantVec := make([]float32, 1024)
	for i := range wantVec {
		wantVec[i] = 0.001 * float32(i+1)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s, want /v1/embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mis-test" {
			t.Errorf("auth = %q", got)
		}

		body, _ := io.ReadAll(r.Body)
		var req mistralEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req.Model != "mistral-embed" {
			t.Errorf("model = %q", req.Model)
		}
		if len(req.Input) != 1 || req.Input[0] != "hello mistral" {
			t.Errorf("input = %v, want [\"hello mistral\"]", req.Input)
		}
		// output_dimension must NOT be sent for mistral-embed.
		if req.OutputDimension != 0 {
			t.Errorf("output_dimension = %d for mistral-embed; must be omitted (API rejects)", req.OutputDimension)
		}
		_ = json.NewEncoder(w).Encode(mistralEmbedResponse{
			Data: []mistralEmbeddingItem{{Embedding: wantVec, Index: 0}},
		})
	}))
	defer server.Close()

	e := &mistralEmbedder{
		apiKey: "mis-test", model: "mistral-embed", dim: 1024,
		baseURL: server.URL, http: server.Client(),
	}
	got, err := e.Embed(context.Background(), "hello mistral")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 1024 {
		t.Fatalf("dim = %d, want 1024", len(got))
	}
}

// A non-default Mistral model must get output_dimension in the request
// -- the guard is specifically for mistral-embed, not for the provider
// as a whole.
func TestMistralEmbed_CustomModelSendsDimension(t *testing.T) {
	var gotReq mistralEmbedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		vec := make([]float32, 512)
		_ = json.NewEncoder(w).Encode(mistralEmbedResponse{
			Data: []mistralEmbeddingItem{{Embedding: vec, Index: 0}},
		})
	}))
	defer server.Close()

	e := &mistralEmbedder{
		apiKey: "k", model: "custom-mistral-embed", dim: 512,
		baseURL: server.URL, http: server.Client(),
	}
	_, err := e.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotReq.OutputDimension != 512 {
		t.Errorf("output_dimension = %d, want 512 (custom model)", gotReq.OutputDimension)
	}
}

func TestMistralEmbed_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(mistralEmbedResponse{
			Error: &mistralAPIError{Type: "invalid_request_error", Message: "bad key"},
		})
	}))
	defer server.Close()

	e := &mistralEmbedder{
		apiKey: "k", model: "mistral-embed", dim: 1024,
		baseURL: server.URL, http: server.Client(),
	}
	_, err := e.Embed(context.Background(), "x")
	if err == nil ||
		!strings.Contains(err.Error(), "invalid_request_error") ||
		!strings.Contains(err.Error(), "bad key") {
		t.Errorf("expected typed error, got %v", err)
	}
}

func TestMistralEmbed_EmptyText(t *testing.T) {
	e := &mistralEmbedder{apiKey: "k", model: "mistral-embed", dim: 1024, http: http.DefaultClient}
	if _, err := e.Embed(context.Background(), ""); err == nil {
		t.Error("expected error on empty text")
	}
}

// applyEnv: MISTRAL_BASE_URL must only apply when provider=mistral.
func TestLoad_MistralBaseURLScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EMBEDDING_PROVIDER", "mistral")
	t.Setenv("MISTRAL_API_KEY", "mis-test")
	t.Setenv("MISTRAL_BASE_URL", "https://mistral.internal/")
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedding.BaseURL != "https://mistral.internal/" {
		t.Errorf("BaseURL = %q, want lifted from MISTRAL_BASE_URL", cfg.Embedding.BaseURL)
	}
}

func TestSidecarEnv_MistralBaseURL(t *testing.T) {
	cfg := &Config{
		Embedding: Embedding{
			Provider: "mistral",
			BaseURL:  "https://mistral.internal",
		},
	}
	got := map[string]string{}
	for _, kv := range cfg.SidecarEnv() {
		if i := strings.Index(kv, "="); i > 0 {
			got[kv[:i]] = kv[i+1:]
		}
	}
	if got["MISTRAL_BASE_URL"] != "https://mistral.internal" {
		t.Errorf("MISTRAL_BASE_URL not emitted: %+v", got)
	}
}
