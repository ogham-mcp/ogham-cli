package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ogham-mcp/ogham-cli/internal/native/cache"
)

// Wire the real cache (via NewEmbedder) and verify a repeated Embed
// call goes to the provider exactly once: second call must be served
// from the SQLite cache without a second HTTP round trip.
func TestNewEmbedder_CacheWrapping_HitSkipsProvider(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "1") // override the package TestMain default
	t.Setenv("HOME", t.TempDir())          // isolated cache dir
	cache.ResetDefault()
	t.Cleanup(cache.ResetDefault)

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		vec := make([]float32, 4)
		for i := range vec {
			vec[i] = 0.1 + float32(i)*0.1
		}
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{
			Data: []openaiEmbeddingItem{{Embedding: vec, Index: 0}},
		})
	}))
	defer server.Close()

	e, err := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 4,
		BaseURL:   server.URL,
	}})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	// Name() should advertise the wrapper so tooling can see it.
	if got := e.Name(); got != "openai/text-embedding-3-small+cache" {
		t.Errorf("Name = %q, want openai/text-embedding-3-small+cache", got)
	}

	v1, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed #1: %v", err)
	}
	v2, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed #2: %v", err)
	}

	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (second call must hit cache)", calls.Load())
	}
	if len(v1) != len(v2) {
		t.Fatalf("cache returned a different-length vector: %d vs %d", len(v1), len(v2))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Errorf("vec[%d]: live=%v cached=%v", i, v1[i], v2[i])
		}
	}
}

// Different texts must miss independently (Key scoping).
func TestNewEmbedder_CacheWrapping_DifferentTexts(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "1")
	t.Setenv("HOME", t.TempDir())
	cache.ResetDefault()
	t.Cleanup(cache.ResetDefault)

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		vec := []float32{0.1, 0.2, 0.3, 0.4}
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{
			Data: []openaiEmbeddingItem{{Embedding: vec, Index: 0}},
		})
	}))
	defer server.Close()

	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 4,
		BaseURL:   server.URL,
	}})
	_, _ = e.Embed(context.Background(), "one")
	_, _ = e.Embed(context.Background(), "two")
	_, _ = e.Embed(context.Background(), "three")
	if calls.Load() != 3 {
		t.Errorf("HTTP calls = %d, want 3 (distinct texts each miss)", calls.Load())
	}
}

// OGHAM_EMBEDDING_CACHE=0 must bypass the wrapper entirely, so the
// provider is called every time regardless of the text.
func TestNewEmbedder_CacheWrapping_Disabled(t *testing.T) {
	t.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	t.Setenv("HOME", t.TempDir())
	cache.ResetDefault()
	t.Cleanup(cache.ResetDefault)

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		vec := []float32{0.1, 0.2, 0.3, 0.4}
		_ = json.NewEncoder(w).Encode(openaiEmbedResponse{
			Data: []openaiEmbeddingItem{{Embedding: vec, Index: 0}},
		})
	}))
	defer server.Close()

	e, _ := NewEmbedder(&Config{Embedding: Embedding{
		Provider:  "openai",
		APIKey:    "sk-test",
		Dimension: 4,
		BaseURL:   server.URL,
	}})
	// Name() should NOT have the +cache suffix when disabled.
	if got := e.Name(); got != "openai/text-embedding-3-small" {
		t.Errorf("Name with cache disabled = %q, want no +cache suffix", got)
	}
	_, _ = e.Embed(context.Background(), "same")
	_, _ = e.Embed(context.Background(), "same")
	if calls.Load() != 2 {
		t.Errorf("HTTP calls = %d, want 2 (cache disabled)", calls.Load())
	}
}
