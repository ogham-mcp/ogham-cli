//go:build live

// Live embedder smoke tests. Opted into via `go test -tags live` --
// off by default so CI + normal dev flows stay hermetic. Each test
// self-skips when its preconditions aren't met (API key missing,
// local server down, etc.) so `make live` on any machine "does the
// right thing" with whatever credentials that machine has.
//
// Run:   go test -tags live -v -run Live ./internal/native/
// Or:    make live

package native

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Reachability probe: returns nil if the Ollama API is answering on
// the given base URL. Used as a t.Skip gate.
func probeOllama(baseURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &roundTripError{status: resp.StatusCode}
	}
	return nil
}

type roundTripError struct{ status int }

func (e *roundTripError) Error() string { return "http " + itoa(e.status) }

func itoa(n int) string { // avoid importing strconv just for this
	if n == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestLiveOllama_Embed drives an actual embedding request against a
// local Ollama server. The model defaults to OGHAM_LIVE_OLLAMA_MODEL
// (or embeddinggemma), dim to OGHAM_LIVE_OLLAMA_DIM (or 768 which
// matches the default model). Override when pointing at a different
// embedder -- e.g. mxbai-embed-large emits 1024 dims natively.
func TestLiveOllama_Embed(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("OLLAMA_URL"))
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if err := probeOllama(baseURL); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}

	model := os.Getenv("OGHAM_LIVE_OLLAMA_MODEL")
	if model == "" {
		model = "embeddinggemma"
	}
	dim := 768
	if v := os.Getenv("OGHAM_LIVE_OLLAMA_DIM"); v != "" {
		parsed, err := parseDim(v)
		if err != nil {
			t.Fatalf("OGHAM_LIVE_OLLAMA_DIM: %v", err)
		}
		dim = parsed
	}

	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "ollama",
		Model:     model,
		Dimension: dim,
		BaseURL:   baseURL,
	}})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vec, err := e.Embed(ctx, "hello world from the ogham live smoke test")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != dim {
		t.Fatalf("vector length = %d, want %d", len(vec), dim)
	}
	var zeros int
	for _, v := range vec {
		if v == 0 {
			zeros++
		}
	}
	if zeros > dim/2 {
		t.Errorf("suspicious: %d/%d zeros in live embedding -- model may be misconfigured", zeros, dim)
	}
	t.Logf("ok: ollama model=%s dim=%d first=%g last=%g zeros=%d",
		model, dim, vec[0], vec[len(vec)-1], zeros)
}

// TestLiveOpenAI_Embed drives an actual embedding request against the
// OpenAI API. Skips when OPENAI_API_KEY isn't set.
//
// Override model via OGHAM_LIVE_OPENAI_MODEL (default
// text-embedding-3-small) and dim via OGHAM_LIVE_OPENAI_DIM (default 512).
// Base URL can be redirected to an Azure OpenAI or LocalAI endpoint
// via OPENAI_BASE_URL.
func TestLiveOpenAI_Embed(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set -- skipping live OpenAI embed test")
	}

	model := os.Getenv("OGHAM_LIVE_OPENAI_MODEL")
	if model == "" {
		model = "text-embedding-3-small"
	}
	dim := 512
	if v := os.Getenv("OGHAM_LIVE_OPENAI_DIM"); v != "" {
		parsed, err := parseDim(v)
		if err != nil {
			t.Fatalf("OGHAM_LIVE_OPENAI_DIM: %v", err)
		}
		dim = parsed
	}
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))

	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    apiKey,
		Model:     model,
		Dimension: dim,
		BaseURL:   baseURL,
	}})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vec, err := e.Embed(ctx, "hello world from the ogham live smoke test")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != dim {
		t.Fatalf("vector length = %d, want %d", len(vec), dim)
	}
	t.Logf("ok: openai model=%s dim=%d first=%g last=%g",
		model, dim, vec[0], vec[len(vec)-1])
}

func parseDim(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, &parseErr{s: s}
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 0, &parseErr{s: s}
	}
	return n, nil
}

type parseErr struct{ s string }

func (e *parseErr) Error() string { return "cannot parse dim " + e.s }
