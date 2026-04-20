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

func TestNewEmbedder_NilConfig(t *testing.T) {
	if _, err := NewEmbedder(nil); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestNewEmbedder_NoProvider(t *testing.T) {
	if _, err := NewEmbedder(&Config{}); err == nil {
		t.Error("expected error when provider is empty")
	}
}

func TestNewEmbedder_UnknownProvider(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{Provider: "cohere"}})
	if err == nil || !strings.Contains(err.Error(), `provider "cohere"`) {
		t.Errorf("expected unknown-provider error, got %v", err)
	}
}

func TestNewEmbedder_UnimplementedProvider(t *testing.T) {
	// Providers not yet absorbed must fail clearly, not silently.
	// Ollama absorbed in rc6 (Iain's stack); Gemini absorbed earlier.
	for _, p := range []string{"openai", "voyage", "mistral", "onnx"} {
		cfg := &Config{Embedding: Embedding{Provider: p, APIKey: "x", Dimension: 512}}
		_, err := NewEmbedder(cfg)
		if err == nil {
			t.Errorf("%s should not be absorbed yet", p)
		}
	}
}

func TestNewEmbedder_OllamaNoKeyRequired(t *testing.T) {
	t.Setenv("OLLAMA_URL", "")
	e, err := NewEmbedder(&Config{Embedding: Embedding{Provider: "ollama", Dimension: 512}})
	if err != nil {
		t.Fatalf("ollama embedder should build without API key: %v", err)
	}
	if e.Name() != "ollama/embeddinggemma" {
		t.Errorf("default Ollama model name = %q", e.Name())
	}
	if e.Dimension() != 512 {
		t.Errorf("dim = %d", e.Dimension())
	}
}

func TestNewEmbedder_OllamaURLOverride(t *testing.T) {
	// BaseURL comes from cfg.Embedding now, not env. applyEnv lifts
	// OLLAMA_URL into the config field; the constructor is env-agnostic.
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "ollama",
		Dimension: 512,
		BaseURL:   "http://remote-ollama:11434/",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Trailing slash should be trimmed.
	oe, ok := e.(*ollamaEmbedder)
	if !ok {
		t.Fatalf("expected *ollamaEmbedder, got %T", e)
	}
	if oe.baseURL != "http://remote-ollama:11434" {
		t.Errorf("baseURL = %q, want stripped trailing slash", oe.baseURL)
	}
}

func TestOllamaEmbed_RoundTrip(t *testing.T) {
	wantVec := make([]float32, 512)
	for i := range wantVec {
		wantVec[i] = float32(i) * 0.001
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req ollamaEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req.Model != "embeddinggemma" {
			t.Errorf("model = %q", req.Model)
		}
		if req.Input != "hello" {
			t.Errorf("input = %q", req.Input)
		}
		if req.Dimensions != 512 {
			t.Errorf("dimensions = %d (should match schema)", req.Dimensions)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{wantVec},
		})
	}))
	defer server.Close()

	e := &ollamaEmbedder{
		baseURL: server.URL, model: "embeddinggemma", dim: 512,
		http: server.Client(),
	}
	got, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 512 {
		t.Errorf("dim = %d, want 512", len(got))
	}
}

func TestOllamaEmbed_DimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 768d (default embeddinggemma) when we asked for 512d.
		vec := make([]float32, 768)
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: [][]float32{vec}})
	}))
	defer server.Close()
	e := &ollamaEmbedder{baseURL: server.URL, model: "x", dim: 512, http: server.Client()}
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "dim=512") {
		t.Errorf("expected dim-mismatch error, got %v", err)
	}
}

func TestOllamaEmbed_ServerErrorSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found: embeddinggemma"}`))
	}))
	defer server.Close()
	e := &ollamaEmbedder{baseURL: server.URL, model: "embeddinggemma", dim: 512, http: server.Client()}
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 + error text, got %v", err)
	}
}

func TestNewEmbedder_GeminiRequiresKey(t *testing.T) {
	_, err := NewEmbedder(&Config{Embedding: Embedding{Provider: "gemini", Dimension: 512}})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("expected API key error, got %v", err)
	}
}

func TestNewEmbedder_GeminiDefaults(t *testing.T) {
	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider: "gemini",
		APIKey:   "test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Dimension() != 512 {
		t.Errorf("default dim = %d, want 512", e.Dimension())
	}
	if !strings.Contains(e.Name(), "gemini-embedding-2-preview") {
		t.Errorf("default model missing from name: %q", e.Name())
	}
}

func TestGeminiEmbed_RoundTrip(t *testing.T) {
	wantVec := make([]float32, 512)
	for i := range wantVec {
		wantVec[i] = float32(i) / 512.0
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate request: POST, correct path, JSON body, API key header.
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "batchEmbedContents") {
			t.Errorf("path = %s, want to contain batchEmbedContents", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("missing/wrong API key header: %q", got)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("content-type = %q", got)
		}

		body, _ := io.ReadAll(r.Body)
		var parsed geminiBatchRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("request body not JSON: %v\n%s", err, body)
		}
		if len(parsed.Requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(parsed.Requests))
		}
		req := parsed.Requests[0]
		if req.Model != "models/gemini-embedding-2-preview" {
			t.Errorf("model = %s", req.Model)
		}
		if req.OutputDimensionality != 512 {
			t.Errorf("outputDimensionality = %d", req.OutputDimensionality)
		}
		if len(req.Content.Parts) != 1 || req.Content.Parts[0].Text != "hello world" {
			t.Errorf("text payload wrong: %+v", req.Content.Parts)
		}

		resp := geminiBatchResponse{
			Embeddings: []geminiEmbeddingValue{{Values: wantVec}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := &geminiEmbedder{
		apiKey:  "test-key",
		model:   "gemini-embedding-2-preview",
		dim:     512,
		http:    server.Client(),
		baseURL: server.URL,
	}

	got, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 512 {
		t.Fatalf("embedding len = %d, want 512", len(got))
	}
	for i := 0; i < 512; i++ {
		if got[i] != wantVec[i] {
			t.Errorf("embedding[%d] = %v, want %v", i, got[i], wantVec[i])
			break
		}
	}
}

func TestGeminiEmbed_EmptyText(t *testing.T) {
	e := &geminiEmbedder{apiKey: "k", model: "m", dim: 512, http: http.DefaultClient}
	if _, err := e.Embed(context.Background(), ""); err == nil {
		t.Error("expected error on empty text")
	}
}

func TestGeminiEmbed_DimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiBatchResponse{
			Embeddings: []geminiEmbeddingValue{{Values: []float32{0.1, 0.2, 0.3}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := &geminiEmbedder{
		apiKey: "k", model: "m", dim: 512,
		http: server.Client(), baseURL: server.URL,
	}
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "dim=512") {
		t.Errorf("expected dim mismatch error, got %v", err)
	}
}

func TestGeminiEmbed_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		resp := geminiBatchResponse{Error: &geminiAPIError{
			Code:    400,
			Message: "API key not valid",
			Status:  "INVALID_ARGUMENT",
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := &geminiEmbedder{
		apiKey: "k", model: "m", dim: 512,
		http: server.Client(), baseURL: server.URL,
	}
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "INVALID_ARGUMENT") || !strings.Contains(err.Error(), "API key not valid") {
		t.Errorf("error missing API message: %v", err)
	}
}

func TestGeminiEmbed_EmptyEmbedding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(geminiBatchResponse{Embeddings: nil})
	}))
	defer server.Close()

	e := &geminiEmbedder{
		apiKey: "k", model: "m", dim: 512,
		http: server.Client(), baseURL: server.URL,
	}
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Error("expected error when response has no embeddings")
	}
}

func TestGeminiEmbed_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	e := &geminiEmbedder{
		apiKey: "k", model: "m", dim: 512,
		http: server.Client(), baseURL: server.URL,
	}
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Error("expected parse error")
	}
}
