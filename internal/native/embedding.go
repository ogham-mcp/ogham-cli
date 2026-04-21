package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

	var inner Embedder
	var model string
	switch provider {
	case "gemini":
		model = cfg.Embedding.Model
		if model == "" {
			model = "gemini-embedding-2-preview"
		}
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("native embedder: gemini provider selected but GEMINI_API_KEY is not set")
		}
		inner = &geminiEmbedder{
			apiKey: cfg.Embedding.APIKey,
			model:  model,
			dim:    dim,
			http:   &http.Client{Timeout: 30 * time.Second},
		}
	case "ollama":
		model = cfg.Embedding.Model
		if model == "" {
			model = "embeddinggemma"
		}
		// Ollama is local by default. cfg.Embedding.BaseURL overrides
		// the host (populated from OLLAMA_URL in applyEnv, or from
		// [embedding] base_url in config.toml). No API key required.
		// Longer timeout than Gemini because local inference on a cold
		// model can take several seconds.
		baseURL := strings.TrimRight(strings.TrimSpace(cfg.Embedding.BaseURL), "/")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		inner = &ollamaEmbedder{
			baseURL: baseURL,
			model:   model,
			dim:     dim,
			http:    &http.Client{Timeout: 60 * time.Second},
		}
	case "openai":
		model = cfg.Embedding.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("native embedder: openai provider selected but OPENAI_API_KEY is not set")
		}
		baseURL := strings.TrimRight(strings.TrimSpace(cfg.Embedding.BaseURL), "/")
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		inner = &openaiEmbedder{
			apiKey:  cfg.Embedding.APIKey,
			model:   model,
			dim:     dim,
			baseURL: baseURL,
			http:    &http.Client{Timeout: 30 * time.Second},
		}
	case "voyage":
		model = cfg.Embedding.Model
		if model == "" {
			model = "voyage-3-lite"
		}
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("native embedder: voyage provider selected but VOYAGE_API_KEY is not set")
		}
		baseURL := strings.TrimRight(strings.TrimSpace(cfg.Embedding.BaseURL), "/")
		if baseURL == "" {
			baseURL = "https://api.voyageai.com"
		}
		inner = &voyageEmbedder{
			apiKey:  cfg.Embedding.APIKey,
			model:   model,
			dim:     dim,
			baseURL: baseURL,
			// Voyage treats input_type semantically: "document" for the
			// store path, "query" for the search path. v0.5 absorbs the
			// store path only; query-side goes in when search absorbs.
			inputType: "document",
			http:      &http.Client{Timeout: 30 * time.Second},
		}
	case "mistral":
		model = cfg.Embedding.Model
		if model == "" {
			model = "mistral-embed"
		}
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("native embedder: mistral provider selected but MISTRAL_API_KEY is not set")
		}
		// mistral-embed is a fixed-dim 1024 model; it does not accept
		// output_dimension. Reject any other dim so the schema/request
		// mismatch surfaces at construction rather than at every call.
		if model == "mistral-embed" && dim != 1024 {
			return nil, fmt.Errorf("native embedder: mistral-embed only supports dim=1024, got %d -- update EMBEDDING_DIM to 1024 or switch to a different Mistral embedding model", dim)
		}
		baseURL := strings.TrimRight(strings.TrimSpace(cfg.Embedding.BaseURL), "/")
		if baseURL == "" {
			baseURL = "https://api.mistral.ai"
		}
		inner = &mistralEmbedder{
			apiKey:  cfg.Embedding.APIKey,
			model:   model,
			dim:     dim,
			baseURL: baseURL,
			http:    &http.Client{Timeout: 30 * time.Second},
		}
	case "onnx":
		return nil, fmt.Errorf("native embedder: provider %q not yet absorbed -- use the sidecar path (default) until ported", provider)
	default:
		return nil, fmt.Errorf("native embedder: unknown provider %q", provider)
	}

	// Every provider returns via the shared SQLite cache. Disabled via
	// OGHAM_EMBEDDING_CACHE=0 for troubleshooting or tests that want
	// to exercise the raw provider path without hitting the cache.
	return maybeWrapWithCache(inner, provider, model), nil
}

// geminiEmbedder speaks the Gemini REST embeddings API directly --
// POST /v1beta/models/{model}:embedContent -- to avoid pulling in the
// full google-genai SDK (grpc + protobuf + cloud.google.com/* -- adds
// ~15MB to the stripped binary for a single call shape).
//
// Two quality concerns the Python SDK handles opaquely but we handle
// explicitly here:
//  1. Endpoint: we use :embedContent (singular). The earlier
//     :batchEmbedContents call carried a batch-of-one that worked
//     but isn't the clean single-call path Google's docs recommend.
//  2. Normalization: Gemini returns pre-normalized vectors only at the
//     model's native 3072 dim. At 512 / 768 / 1536 the vector magnitude
//     varies, which turns cosine similarity into a magnitude-weighted
//     score. We L2-normalize client-side when dim < 3072; the docs at
//     https://ai.google.dev/gemini-api/docs/embeddings explicitly
//     recommend this.
type geminiEmbedder struct {
	apiKey string
	model  string
	dim    int
	http   *http.Client

	// baseURL lets tests point at an httptest.Server. Production leaves it
	// empty and picks up the default.
	baseURL string
}

func (g *geminiEmbedder) Name() string   { return "gemini/" + g.model }
func (g *geminiEmbedder) Dimension() int { return g.dim }

// geminiEmbedRequest is the single-embedding request body for :embedContent.
type geminiEmbedRequest struct {
	Model                string        `json:"model"`
	Content              geminiContent `json:"content"`
	OutputDimensionality int           `json:"outputDimensionality,omitempty"`
	TaskType             string        `json:"taskType,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// geminiEmbedResponse mirrors the :embedContent response shape. Note the
// singular "embedding" field -- the legacy :batchEmbedContents endpoint
// used "embeddings" (array).
type geminiEmbedResponse struct {
	Embedding *geminiEmbeddingPayload `json:"embedding"`
	// Error envelope when status != 200. Gemini returns {"error":{"code":...,"message":"..."}}.
	Error *geminiAPIError `json:"error,omitempty"`
}

type geminiEmbeddingPayload struct {
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
	return fmt.Sprintf("%s/v1beta/models/%s:embedContent", base, g.model)
}

// Embed returns a single embedding for text. L2-normalizes the result
// when dim < 3072 because Gemini only pre-normalizes at its native dim.
func (g *geminiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("gemini embed: empty text")
	}

	body := geminiEmbedRequest{
		Model:                "models/" + g.model,
		Content:              geminiContent{Parts: []geminiPart{{Text: text}}},
		OutputDimensionality: g.dim,
		TaskType:             "RETRIEVAL_QUERY",
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

	var parsed geminiEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("gemini embed: parse response: %w (body: %s)", err, truncateForError(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return nil, fmt.Errorf("gemini embed: %s (%d): %s", parsed.Error.Status, parsed.Error.Code, parsed.Error.Message)
		}
		return nil, fmt.Errorf("gemini embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	if parsed.Embedding == nil || len(parsed.Embedding.Values) == 0 {
		return nil, fmt.Errorf("gemini embed: empty embedding in response")
	}
	vec := parsed.Embedding.Values
	if len(vec) != g.dim {
		return nil, fmt.Errorf("gemini embed: expected dim=%d, got %d -- check EMBEDDING_DIM matches your schema", g.dim, len(vec))
	}
	if g.dim < 3072 {
		vec = l2Normalize(vec)
	}
	return vec, nil
}

// l2Normalize rescales v to unit length in place and returns the same
// slice. Zero vectors pass through unchanged (normalizing would divide
// by zero). Callers needing an independent copy must clone first.
func l2Normalize(v []float32) []float32 {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return v
	}
	norm := float32(math.Sqrt(sumSq))
	for i := range v {
		v[i] /= norm
	}
	return v
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

// -----------------------------------------------------------------------
// OpenAI embedder -- HTTPS POST to /v1/embeddings.
//
// Request shape matches the OpenAI REST API documentation:
//   POST {baseURL}/v1/embeddings
//   Authorization: Bearer {api_key}
//   {"model": "text-embedding-3-small", "input": "text", "dimensions": 512}
// Response:
//   {"data": [{"embedding": [0.1, ...], "index": 0}], "model": "...", "usage": {...}}
// Error envelope (any non-200):
//   {"error": {"message": "...", "type": "...", "code": "..."}}
//
// `dimensions` is supported by text-embedding-3-small and
// text-embedding-3-large. Older models (ada-002) ignore it and always
// emit 1536 dims; NewEmbedder will surface a dim mismatch in that case.
//
// baseURL override lets operators point at Azure OpenAI proxies,
// LocalAI, or similar OpenAI-wire-compatible servers.

type openaiEmbedder struct {
	apiKey  string
	model   string
	dim     int
	baseURL string
	http    *http.Client
}

func (o *openaiEmbedder) Name() string   { return "openai/" + o.model }
func (o *openaiEmbedder) Dimension() int { return o.dim }

type openaiEmbedRequest struct {
	Model      string `json:"model"`
	Input      string `json:"input"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type openaiEmbedResponse struct {
	Data  []openaiEmbeddingItem `json:"data"`
	Error *openaiAPIError       `json:"error,omitempty"`
}

type openaiEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openaiAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (o *openaiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("openai embed: empty text")
	}

	body := openaiEmbedRequest{Model: o.model, Input: text, Dimensions: o.dim}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/v1/embeddings", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("openai embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embed: read: %w", err)
	}

	var parsed openaiEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("openai embed: parse: %w (body: %s)", err, truncateForError(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			// OpenAI error envelope: emit the typed message without
			// leaking the endpoint URL (which could carry API-key-bearing
			// Azure proxy paths in some deployments).
			return nil, fmt.Errorf("openai embed: %s: %s (code=%s)",
				parsed.Error.Type, parsed.Error.Message, parsed.Error.Code)
		}
		return nil, fmt.Errorf("openai embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai embed: empty data array in response")
	}
	vec := parsed.Data[0].Embedding
	if len(vec) != o.dim {
		return nil, fmt.Errorf("openai embed: expected dim=%d, got %d -- check EMBEDDING_DIM matches your schema; text-embedding-3-small and -3-large honour the dimensions request, ada-002 does not", o.dim, len(vec))
	}
	return vec, nil
}

// -----------------------------------------------------------------------
// Voyage embedder -- HTTPS POST to /v1/embeddings.
//
// Differs from OpenAI in three specific ways:
//   * body field is output_dimension (not dimensions)
//   * input MUST be an array of strings, even for a single embed
//   * input_type ("document" | "query") is semantic, not shape-related
//     -- Voyage tokens the text slightly differently for retrieval vs
//     document paths. v0.5 absorbs the store-side only, so we hard-code
//     "document" at construction time.
//
// Auth is a Bearer token. Response shape matches OpenAI: data[].embedding.
// baseURL is overridable via VOYAGE_BASE_URL (lifted onto
// cfg.Embedding.BaseURL in applyEnv only when provider=voyage).

type voyageEmbedder struct {
	apiKey    string
	model     string
	dim       int
	baseURL   string
	inputType string
	http      *http.Client
}

func (v *voyageEmbedder) Name() string   { return "voyage/" + v.model }
func (v *voyageEmbedder) Dimension() int { return v.dim }

type voyageEmbedRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	InputType       string   `json:"input_type,omitempty"`
}

type voyageEmbedResponse struct {
	Data  []voyageEmbeddingItem `json:"data"`
	Error *voyageAPIError       `json:"error,omitempty"`
}

type voyageEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type voyageAPIError struct {
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

func (v *voyageEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("voyage embed: empty text")
	}

	body := voyageEmbedRequest{
		Model:           v.model,
		Input:           []string{text},
		OutputDimension: v.dim,
		InputType:       v.inputType,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("voyage embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.baseURL+"/v1/embeddings", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("voyage embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage embed: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voyage embed: read: %w", err)
	}

	var parsed voyageEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("voyage embed: parse: %w (body: %s)", err, truncateForError(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			msg := parsed.Error.Message
			if msg == "" {
				msg = parsed.Error.Detail
			}
			return nil, fmt.Errorf("voyage embed: http %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("voyage embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("voyage embed: empty data array in response")
	}
	vec := parsed.Data[0].Embedding
	if len(vec) != v.dim {
		return nil, fmt.Errorf("voyage embed: expected dim=%d, got %d -- check EMBEDDING_DIM matches your schema (voyage-3-lite and voyage-3 honour output_dimension; older models do not)", v.dim, len(vec))
	}
	return vec, nil
}

// -----------------------------------------------------------------------
// Mistral embedder -- HTTPS POST to /v1/embeddings.
//
// The default mistral-embed model is fixed 1024 dim; it does not accept
// output_dimension. NewEmbedder rejects any dim != 1024 for that model
// so the mismatch surfaces at construction rather than at every call.
// Alternative Mistral embedding models that DO support output_dimension
// can be configured via [embedding] model (constructor skips the dim
// guard for non-default models).
//
// Auth is Bearer; response shape matches OpenAI: data[].embedding.
// baseURL is overridable via MISTRAL_BASE_URL (lifted in applyEnv only
// when provider=mistral).

type mistralEmbedder struct {
	apiKey  string
	model   string
	dim     int
	baseURL string
	http    *http.Client
}

func (m *mistralEmbedder) Name() string   { return "mistral/" + m.model }
func (m *mistralEmbedder) Dimension() int { return m.dim }

type mistralEmbedRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	OutputDimension int      `json:"output_dimension,omitempty"`
}

type mistralEmbedResponse struct {
	Data  []mistralEmbeddingItem `json:"data"`
	Error *mistralAPIError       `json:"error,omitempty"`
}

type mistralEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type mistralAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (m *mistralEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("mistral embed: empty text")
	}

	body := mistralEmbedRequest{
		Model: m.model,
		Input: []string{text},
	}
	// Only request output_dimension for models other than the fixed-dim
	// mistral-embed. Attaching it there returns a 400 from the API.
	if m.model != "mistral-embed" {
		body.OutputDimension = m.dim
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("mistral embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.baseURL+"/v1/embeddings", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("mistral embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mistral embed: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mistral embed: read: %w", err)
	}

	var parsed mistralEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("mistral embed: parse: %w (body: %s)", err, truncateForError(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return nil, fmt.Errorf("mistral embed: %s: %s", parsed.Error.Type, parsed.Error.Message)
		}
		return nil, fmt.Errorf("mistral embed: http %d: %s", resp.StatusCode, truncateForError(respBody))
	}

	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("mistral embed: empty data array in response")
	}
	vec := parsed.Data[0].Embedding
	if len(vec) != m.dim {
		return nil, fmt.Errorf("mistral embed: expected dim=%d, got %d -- mistral-embed returns 1024 natively; use a model that honours output_dimension for smaller dims", m.dim, len(vec))
	}
	return vec, nil
}
