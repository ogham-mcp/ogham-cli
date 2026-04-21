package native

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ogham-mcp/ogham-cli/internal/native/cache"
)

// cachedEmbedder is a transparent Embedder decorator: on Embed it hashes
// the (provider, model, dim, text) tuple, checks the shared SQLite cache,
// and short-circuits on a hit. A miss falls through to the wrapped
// embedder and the returned vector is written back to the cache.
//
// Wire-compatible with Python's generate_embedding cache path in
// src/ogham/embeddings.py so a Python sidecar and a Go native binary
// share the same on-disk vectors at $HOME/.cache/ogham/embeddings.db.
type cachedEmbedder struct {
	inner    Embedder
	cache    *cache.EmbeddingCache
	provider string
	model    string
}

// newCachedEmbedder wraps inner with the shared cache. Callers pass the
// concrete provider name because Embedder.Name() returns "provider/model"
// and the cache key hashes over provider separately.
func newCachedEmbedder(inner Embedder, c *cache.EmbeddingCache, provider, model string) Embedder {
	return &cachedEmbedder{
		inner:    inner,
		cache:    c,
		provider: provider,
		model:    model,
	}
}

func (c *cachedEmbedder) Name() string   { return c.inner.Name() + "+cache" }
func (c *cachedEmbedder) Dimension() int { return c.inner.Dimension() }

func (c *cachedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	key := cache.Key(c.provider, c.model, c.inner.Dimension(), text)
	if vec, ok, err := c.cache.Get(key); err != nil {
		// Cache read failure isn't fatal -- log and fall through to the
		// provider so the caller still gets a vector. Repeated warnings
		// would be annoying, but embedder paths aren't that hot.
		slog.Warn("embedding cache read failed; bypassing cache",
			"err", err, "key", key)
	} else if ok {
		return vec, nil
	}

	vec, err := c.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if putErr := c.cache.Put(key, vec, ""); putErr != nil {
		// Don't surface cache-put failures to the caller: they already
		// have the vector. Logging at warn keeps the error visible
		// without breaking the request.
		slog.Warn("embedding cache write failed; continuing uncached",
			"err", putErr, "key", key)
	}
	return vec, nil
}

// maybeWrapWithCache returns inner unless caching is explicitly disabled
// via OGHAM_EMBEDDING_CACHE=0 / off / false. On Default() failure (rare:
// home-dir resolution, MkdirAll, sqlite open) it logs and returns inner
// so callers never crash for an opportunistic optimisation.
func maybeWrapWithCache(inner Embedder, provider, model string) Embedder {
	if isCacheDisabled() {
		return inner
	}
	c, err := cache.Default()
	if err != nil {
		slog.Warn("embedding cache unavailable; running uncached",
			"err", err)
		return inner
	}
	return newCachedEmbedder(inner, c, provider, model)
}

// isCacheDisabled reads OGHAM_EMBEDDING_CACHE. Matching the Python side
// would be ideal; for now an explicit opt-out via env is enough for the
// tests and troubleshooting paths that need a bypass.
func isCacheDisabled() bool {
	v := os.Getenv("OGHAM_EMBEDDING_CACHE")
	switch v {
	case "0", "off", "false", "no", "OFF", "FALSE", "NO":
		return true
	}
	return false
}

// ensure the wrapper satisfies the Embedder contract.
var _ Embedder = (*cachedEmbedder)(nil)

// Ensure fmt import isn't dropped if slog lines are removed later.
var _ = fmt.Sprintf
