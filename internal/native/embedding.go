package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder produces a fixed-dimension embedding vector for a single text.
// All providers in the absorption path implement this same contract so the
// rest of the code doesn't care which provider is configured.
type Embedder interface {
	// Embed returns the embedding vector for text. Length must equal the
	// dimension the Embedder was configured with.
	Embed(ctx context.Context, text string) ([]float32, error)

	// Name returns a short identifier for error/log messages.
	Name() string

	// Dimension returns the output vector length. Callers that need to
	// assert schema compatibility (vector(512) etc.) can check here.
	Dimension() int
}

// NewEmbedder constructs an Embedder from a native.Config. Providers are
// added as they are absorbed; Gemini is the first, matching the user's
// current .env configuration. Returns a clear error on unknown providers
// rather than silently falling back to a default.
func NewEmbedder(cfg *Config) (Embedder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native embedder: nil config")
	}
	provider := cfg.Embedding.Provider
	if provider == "" {
		return nil, fmt.Errorf("native embedder: no provider configured (set EMBEDDING_PROVIDER or [embedding] provider in config.toml)")
	}

	dim := cfg.Embedding.Dimension
	if dim <= 0 {
		dim = 512
	}

	switch provider {
	case "gemini":
		model := cfg.Embedding.Model
		if model == "" {
			model = "gemini-embedding-2-preview"
		}
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("native embedder: gemini provider selected but GEMINI_API_KEY is not set")
		}
		return &geminiEmbedder{
			apiKey: cfg.Embedding.APIKey,
			model:  model,
			dim:    dim,
			http:   &http.Client{Timeout: 30 * time.Second},
		}, nil
	case "ollama":
		model := cfg.Embedding.Model
		if model == "" {
			model = "embeddinggemma"
		}
		// Ollama is local by default. OLLAMA_URL overrides the host; no
		// API key is required. Longer timeout than Gemini because local
		// inference on a cold model can take several seconds.
		baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("OLLAMA_URL")), "/")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &ollamaEmbedder{
			baseURL: baseURL,
			model:   model,
			dim:     dim,
			http:    &http.Client{Timeout: 60 * time.Second},
		}, nil
	case "openai", "voyage", "mistral", "onnx":
		return nil, fmt.Errorf("native embedder: provider %q not yet absorbed -- use the sidecar path (default) until ported", provider)
	default:
		return nil, fmt.Errorf("native embedder: unknown provider %q", provider)
	}
}

// geminiEmbedder speaks the Gemini REST embeddings API directly --
// POST /v1beta/models/{model}:batchEmbedContents -- to avoid pulling
// in the full google-genai SDK for a single call shape. Matches the
// request/response schema observed from the Python google-genai client.
type geminiEmbedder struct {
	apiKey string
	model  string
	dim    int
	http   *http.Client

	// baseURL lets tests point at an httptest.Server. Production leaves it
	// empty and picks up the default.
	baseURL string
}

func (g *geminiEmbedder) Name() string    { return "gemini/" + g.model }
func (g *geminiEmbedder) Dimension() int  { return g.dim }

// geminiBatchRequest / geminiBatchResponse mirror the public Gemini schema
// for batchEmbedContents.
type geminiBatchRequest struct {
	Requests []geminiEmbedRequest `json:"requests"`
}

type geminiEmbedRequest struct {
	Model                string             `json:"model"`
	Content              geminiContent      `json:"content"`
	OutputDimensionality int                `json:"outputDimensionality,omitempty"`
	TaskType             string             `json:"taskType,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiBatchResponse struct {
	Embeddings []geminiEmbeddingValue `json:"embeddings"`
	// Error envelope when status != 200. Gemini returns {"error":{"code":...,"message":"..."}}.
	Error *geminiAPIError `json:"error,omitempty"`
}

type geminiEmbeddingValue struct {
	Values []float32 `json:"values"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (g *geminiEmbedder) endpoint() string {
	base := g.baseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	return fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents", base, g.model)
}

// Embed returns a single embedding for text. Uses the batch endpoint with
// a single request because that matches what Python's google-genai does
// and keeps a shared code path for future bulk embedding work.
func (g *geminiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("gemini embed: empty text")
	}

	body := geminiBatchRequest{
		Requests: []geminiEmbedRequest{{
			Model:                "models/" + g.model,
			Content:              geminiContent{Parts: []geminiPart{{Text: text}}},
			OutputDimensionality: g.dim,
			TaskType:             "RETRIEVAL_QUERY",
		}},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("gemini embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: read response: %w", err)
	}

	var parsed geminiBatchResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("gemini embed: parse response: %w (body: %s)", err, truncateForError(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return nil, fmt.Errorf("gemini embed: %s (%d): %s", parsed.Error.Status, parsed.Error.Code, parsed.Error.Message)
		}
		return nil, fmt.Errorf("gemini embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	if len(parsed.Embeddings) == 0 || len(parsed.Embeddings[0].Values) == 0 {
		return nil, fmt.Errorf("gemini embed: empty embedding in response")
	}
	vec := parsed.Embeddings[0].Values
	if len(vec) != g.dim {
		return nil, fmt.Errorf("gemini embed: expected dim=%d, got %d -- check EMBEDDING_DIM matches your schema", g.dim, len(vec))
	}
	return vec, nil
}

func truncateForError(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

// -----------------------------------------------------------------------
// Ollama embedder -- local, no auth, HTTP POST to /api/embed.
//
// Request/response shape matches the ollama-python SDK (which Python
// ogham uses):
//   POST {baseURL}/api/embed
//   {"model": "embeddinggemma", "input": "text", "dimensions": 512}
// Response:
//   {"embeddings": [[0.1, 0.2, ...]]}
//
// dimensions lets Matryoshka Representation Learning models (like
// embeddinggemma) truncate to the schema's vector size. Without it,
// embeddinggemma returns 768d which would fail our vector(512) schema.

type ollamaEmbedder struct {
	baseURL string
	model   string
	dim     int
	http    *http.Client
}

func (o *ollamaEmbedder) Name() string   { return "ollama/" + o.model }
func (o *ollamaEmbedder) Dimension() int { return o.dim }

type ollamaEmbedRequest struct {
	Model      string `json:"model"`
	Input      string `json:"input"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

func (o *ollamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("ollama embed: empty text")
	}

	body := ollamaEmbedRequest{Model: o.model, Input: text, Dimensions: o.dim}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/embed", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: reach %s: %w", o.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	var parsed ollamaEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("ollama embed: parse: %w (body: %s)", err, truncateForError(respBody))
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ollama embed: %s", parsed.Error)
	}
	if len(parsed.Embeddings) == 0 || len(parsed.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("ollama embed: empty embeddings array in response")
	}
	vec := parsed.Embeddings[0]
	if len(vec) != o.dim {
		return nil, fmt.Errorf("ollama embed: expected dim=%d, got %d -- check EMBEDDING_DIM matches your schema, or use a model that supports MRL truncation (e.g. embeddinggemma) so the dimensions request is honoured", o.dim, len(vec))
	}
	return vec, nil
}
