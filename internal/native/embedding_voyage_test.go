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

func TestNewEmbedder_VoyageDefaults(t *testing.T) {
	// Construction test asserts the inner type directly.
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "voyage",
		APIKey:    "voy-test",
		Dimension: 512,
	}})
	if err != nil {
		t.Fatalf("voyage embedder failed to build: %v", err)
	}
	if e.Name() != "voyage/voyage-3-lite" {
		t.Errorf("default model name = %q", e.Name())
	}
	if e.Dimension() != 512 {
		t.Errorf("dim = %d", e.Dimension())
	}
	ve := e.(*voyageEmbedder)
	if ve.baseURL != "https://api.voyageai.com" {
		t.Errorf("default baseURL = %q", ve.baseURL)
	}
	if ve.inputType != "document" {
		t.Errorf("default inputType = %q, want document (store-side)", ve.inputType)
	}
}

func TestNewEmbedder_VoyageMissingKey(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "voyage",
		Dimension: 512,
	}})
	if err == nil || !strings.Contains(err.Error(), "VOYAGE_API_KEY") {
		t.Errorf("expected VOYAGE_API_KEY error, got %v", err)
	}
}

// Trailing slashes on cfg.Embedding.BaseURL must be trimmed so the
// downstream "{baseURL}/v1/embeddings" concat doesn't produce "//v1".
func TestNewEmbedder_VoyageBaseURLOverride(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "voyage",
		APIKey:    "voy-test",
		Dimension: 512,
		BaseURL:   "https://api.voyageai.example.com/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	ve := e.(*voyageEmbedder)
	if ve.baseURL != "https://api.voyageai.example.com" {
		t.Errorf("baseURL = %q (trailing slash should be stripped)", ve.baseURL)
	}
}

// Round-trip test confirms the wire shape matches the Voyage docs:
//   * POST /v1/embeddings
//   * Authorization: Bearer ...
//   * body { model, input[], output_dimension, input_type }
//   * response data[].embedding
func TestVoyageEmbed_RoundTrip(t *testing.T) {
	wantVec := make([]float32, 4)
	for i := range wantVec {
		wantVec[i] = 0.25 * float32(i+1)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s, want /v1/embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer voy-test" {
			t.Errorf("auth = %q", got)
		}

		body, _ := io.ReadAll(r.Body)
		var req voyageEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req.Model != "voyage-3-lite" {
			t.Errorf("model = %q", req.Model)
		}
		// Input MUST be an array, even for a single text.
		if len(req.Input) != 1 || req.Input[0] != "hello voyage" {
			t.Errorf("input = %v, want [\"hello voyage\"]", req.Input)
		}
		if req.OutputDimension != 4 {
			t.Errorf("output_dimension = %d (field name must be output_dimension not dimensions)", req.OutputDimension)
		}
		if req.InputType != "document" {
			t.Errorf("input_type = %q, want document (store-side)", req.InputType)
		}
		_ = json.NewEncoder(w).Encode(voyageEmbedResponse{
			Data: []voyageEmbeddingItem{{Embedding: wantVec, Index: 0}},
		})
	}))
	defer server.Close()

	e := &voyageEmbedder{
		apiKey: "voy-test", model: "voyage-3-lite", dim: 4,
		baseURL: server.URL, inputType: "document", http: server.Client(),
	}
	got, err := e.Embed(context.Background(), "hello voyage")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("dim = %d, want 4", len(got))
	}
	for i := range got {
		if got[i] != wantVec[i] {
			t.Errorf("vec[%d] = %v, want %v", i, got[i], wantVec[i])
		}
	}
}

func TestVoyageEmbed_DimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(voyageEmbedResponse{
			Data: []voyageEmbeddingItem{{Embedding: []float32{0.1, 0.2}}},
		})
	}))
	defer server.Close()

	e := &voyageEmbedder{
		apiKey: "k", model: "m", dim: 4,
		baseURL: server.URL, inputType: "document", http: server.Client(),
	}
	if _, err := e.Embed(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "dim=4") {
		t.Errorf("expected dim mismatch error, got %v", err)
	}
}

func TestVoyageEmbed_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(voyageEmbedResponse{
			Error: &voyageAPIError{Message: "Invalid API key"},
		})
	}))
	defer server.Close()

	e := &voyageEmbedder{
		apiKey: "k", model: "m", dim: 4,
		baseURL: server.URL, inputType: "document", http: server.Client(),
	}
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("expected typed error, got %v", err)
	}
}

func TestVoyageEmbed_EmptyText(t *testing.T) {
	e := &voyageEmbedder{apiKey: "k", model: "m", dim: 4, http: http.DefaultClient}
	if _, err := e.Embed(context.Background(), ""); err == nil {
		t.Error("expected error on empty text")
	}
}

// applyEnv: VOYAGE_BASE_URL must only apply when provider=voyage.
// A stray value under provider=openai / mistral must NOT leak.
func TestLoad_VoyageBaseURLScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EMBEDDING_PROVIDER", "voyage")
	t.Setenv("VOYAGE_API_KEY", "voy-test")
	t.Setenv("VOYAGE_BASE_URL", "https://voyage.internal/")
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedding.BaseURL != "https://voyage.internal/" {
		t.Errorf("BaseURL = %q, want lifted from VOYAGE_BASE_URL", cfg.Embedding.BaseURL)
	}
}

func TestLoad_VoyageBaseURLIgnoredForOpenAI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("VOYAGE_BASE_URL", "https://voyage.internal/")
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedding.BaseURL == "https://voyage.internal/" {
		t.Errorf("VOYAGE_BASE_URL leaked into BaseURL for openai provider")
	}
}

// SidecarEnv: must only emit VOYAGE_BASE_URL when provider=voyage.
func TestSidecarEnv_VoyageBaseURL(t *testing.T) {
	cfg := &Config{
		Embedding: Embedding{
			Provider: "voyage",
			BaseURL:  "https://voyage.internal",
		},
	}
	got := map[string]string{}
	for _, kv := range cfg.SidecarEnv() {
		if i := strings.Index(kv, "="); i > 0 {
			got[kv[:i]] = kv[i+1:]
		}
	}
	if got["VOYAGE_BASE_URL"] != "https://voyage.internal" {
		t.Errorf("VOYAGE_BASE_URL not emitted: %+v", got)
	}

	// Non-voyage provider must not leak it.
	cfg2 := &Config{
		Embedding: Embedding{
			Provider: "openai",
			BaseURL:  "https://somewhere",
		},
	}
	for _, kv := range cfg2.SidecarEnv() {
		if strings.HasPrefix(kv, "VOYAGE_BASE_URL=") {
			t.Errorf("VOYAGE_BASE_URL leaked for openai provider: %s", kv)
		}
	}
}
