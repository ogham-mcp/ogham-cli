// Package cache implements the shared SQLite-backed embedding cache.
//
// Wire-compatible with the Python EmbeddingCache in
// src/ogham/embedding_cache.py: same schema, same key format, same JSON
// value encoding. A Python sidecar writing an entry and a Go native
// binary reading it (or vice versa) must see the same vector within
// float32 round-trip precision.
//
// Default path is $HOME/.cache/ogham/embeddings.db on every platform so
// Python and Go agree even on macOS where os.UserCacheDir would drift
// to ~/Library/Caches/. Override via cacheDir.
package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// Default file name under cacheDir. Matches Python's embedding_cache.py.
const dbFileName = "embeddings.db"

// DefaultMaxSize is the eviction ceiling used when none is specified.
// Matches Python's EmbeddingCache default.
const DefaultMaxSize = 10_000

// Env var names honoured by Default(). OGHAM_CACHE_DIR is the Go-side
// canonical override; EMBEDDING_CACHE_DIR mirrors the name pydantic-
// settings uses on the Python side so operators that set it once have
// it cover both stacks.
const (
	envOghamCacheDir     = "OGHAM_CACHE_DIR"
	envEmbeddingCacheDir = "EMBEDDING_CACHE_DIR"
)

var (
	defaultOnce  sync.Once
	defaultCache *EmbeddingCache
	defaultErr   error
)

// Default returns a process-wide singleton cache rooted at
// $OGHAM_CACHE_DIR (or $EMBEDDING_CACHE_DIR, then $HOME/.cache/ogham/).
// First call wins; subsequent calls return the same instance even if
// the env vars have since changed. Safe to call from multiple goroutines.
func Default() (*EmbeddingCache, error) {
	defaultOnce.Do(func() {
		dir := os.Getenv(envOghamCacheDir)
		if dir == "" {
			dir = os.Getenv(envEmbeddingCacheDir)
		}
		defaultCache, defaultErr = Open(dir, DefaultMaxSize)
	})
	return defaultCache, defaultErr
}

// ResetDefault is a test helper: it closes the singleton (ignoring any
// close error) and clears the sync.Once so the next Default() call can
// open a fresh cache -- for example under a newly set HOME. Not safe
// to use from production code; only exported for cross-package tests.
func ResetDefault() {
	if defaultCache != nil {
		_ = defaultCache.Close()
	}
	defaultCache = nil
	defaultErr = nil
	defaultOnce = sync.Once{}
}

// dsnPragmas are DSN-attached pragmas that apply to every connection
// modernc.org/sqlite opens. WAL lets readers and writers proceed in
// parallel (Python sidecar + Go binary can coexist without blocking).
// busy_timeout blocks briefly instead of returning SQLITE_BUSY when
// another writer holds the lock -- smoother contention under load.
const dsnPragmas = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"

// EmbeddingCache is a goroutine-safe SQLite-backed cache for embedding
// vectors keyed by the SHA-256 of {provider}:{model}:{dim}:{text}.
type EmbeddingCache struct {
	db      *sql.DB
	maxSize int

	// mu serializes writes and the count check that gates eviction so
	// concurrent Puts can't race past the limit. Reads are fine without
	// it because SQLite handles concurrent readers itself (WAL mode).
	mu sync.Mutex

	// Stats counters. Atomic for lock-free reads from Stats().
	hits   atomic.Int64
	misses atomic.Int64
}

// Stats reports cache usage. Evictions counter always reports 0 for
// parity with Python's sqlite cache which also doesn't track them.
type Stats struct {
	Size      int     `json:"size"`
	MaxSize   int     `json:"max_size"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Evictions int64   `json:"evictions"`
	HitRate   float64 `json:"hit_rate"`
}

// Open opens (or creates) the cache at cacheDir/embeddings.db. Empty
// cacheDir means $HOME/.cache/ogham/ (cross-stack shared path).
func Open(cacheDir string, maxSize int) (*EmbeddingCache, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("embedding cache: resolve home: %w", err)
		}
		cacheDir = filepath.Join(home, ".cache", "ogham")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("embedding cache: create %s: %w", cacheDir, err)
	}
	path := filepath.Join(cacheDir, dbFileName)

	db, err := sql.Open("sqlite", path+dsnPragmas)
	if err != nil {
		return nil, fmt.Errorf("embedding cache: open %s: %w", path, err)
	}

	c := &EmbeddingCache{db: db, maxSize: maxSize}
	if err := c.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return c, nil
}

// init creates the table + adds the sparse column if missing. Mirrors
// the Python constructor's CREATE + migrate step exactly so both stacks
// converge on the same schema regardless of who opens the file first.
func (c *EmbeddingCache) init() error {
	// Schema: matches Python's src/ogham/embedding_cache.py line-for-line.
	_, err := c.db.Exec(`CREATE TABLE IF NOT EXISTS embeddings (
		key TEXT PRIMARY KEY,
		value BLOB NOT NULL,
		created_at REAL NOT NULL DEFAULT (unixepoch('now'))
	)`)
	if err != nil {
		return fmt.Errorf("embedding cache: create table: %w", err)
	}
	// Online migration: add sparse column if the older Python writer
	// created the table before the column existed.
	rows, err := c.db.Query("PRAGMA table_info(embeddings)")
	if err != nil {
		return fmt.Errorf("embedding cache: table_info: %w", err)
	}
	defer func() { _ = rows.Close() }()
	hasSparse := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("embedding cache: scan table_info: %w", err)
		}
		if name == "sparse" {
			hasSparse = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("embedding cache: table_info iter: %w", err)
	}
	if !hasSparse {
		if _, err := c.db.Exec("ALTER TABLE embeddings ADD COLUMN sparse TEXT"); err != nil {
			return fmt.Errorf("embedding cache: add sparse column: %w", err)
		}
	}
	return nil
}

// Close releases the underlying SQLite handle. Safe to call multiple
// times; a second call returns sql.DB's idempotent error.
func (c *EmbeddingCache) Close() error {
	return c.db.Close()
}

// Key returns the cache key for the given embedding-request scope.
// Byte-identical to Python's src/ogham/embeddings.py _cache_key().
// Exported so callers that want to pre-compute keys (batch paths) can.
func Key(provider, model string, dim int, text string) string {
	payload := fmt.Sprintf("%s:%s:%d:%s", provider, model, dim, text)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Get returns the embedding for key, or (nil, false, nil) on miss.
// The hit/miss counters are updated as a side effect.
func (c *EmbeddingCache) Get(key string) ([]float32, bool, error) {
	var raw []byte
	err := c.db.QueryRow(
		"SELECT value FROM embeddings WHERE key = ?", key,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		c.misses.Add(1)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("embedding cache: get %s: %w", key, err)
	}
	var vec []float32
	if err := json.Unmarshal(raw, &vec); err != nil {
		return nil, false, fmt.Errorf("embedding cache: decode value for %s: %w", key, err)
	}
	c.hits.Add(1)
	return vec, true, nil
}

// GetFull returns (embedding, sparse, true, nil) on hit or a miss
// sentinel. Sparse is "" when the column is NULL.
func (c *EmbeddingCache) GetFull(key string) ([]float32, string, bool, error) {
	var raw []byte
	var sparse sql.NullString
	err := c.db.QueryRow(
		"SELECT value, sparse FROM embeddings WHERE key = ?", key,
	).Scan(&raw, &sparse)
	if err == sql.ErrNoRows {
		c.misses.Add(1)
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("embedding cache: get_full %s: %w", key, err)
	}
	var vec []float32
	if err := json.Unmarshal(raw, &vec); err != nil {
		return nil, "", false, fmt.Errorf("embedding cache: decode value for %s: %w", key, err)
	}
	c.hits.Add(1)
	s := ""
	if sparse.Valid {
		s = sparse.String
	}
	return vec, s, true, nil
}

// Put writes (or replaces) the entry for key. sparse="" stores NULL.
// After inserting it runs eviction if the row count exceeded maxSize.
func (c *EmbeddingCache) Put(key string, embedding []float32, sparse string) error {
	value, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("embedding cache: marshal embedding: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var sparseArg any
	if sparse == "" {
		sparseArg = nil
	} else {
		sparseArg = sparse
	}
	_, err = c.db.Exec(
		"INSERT OR REPLACE INTO embeddings (key, value, sparse) VALUES (?, ?, ?)",
		key, value, sparseArg,
	)
	if err != nil {
		return fmt.Errorf("embedding cache: insert %s: %w", key, err)
	}
	return c.evictLocked()
}

// Contains reports whether key is present without touching hit/miss.
func (c *EmbeddingCache) Contains(key string) (bool, error) {
	var one int
	err := c.db.QueryRow("SELECT 1 FROM embeddings WHERE key = ?", key).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("embedding cache: contains: %w", err)
	}
	return true, nil
}

// Len returns the row count. Stats() is cheaper for repeated calls
// because it reads the same value under the same lock.
func (c *EmbeddingCache) Len() (int, error) {
	var n int
	err := c.db.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("embedding cache: count: %w", err)
	}
	return n, nil
}

// Clear removes every row and zeroes the hit/miss counters. Returns
// the number of rows that existed before the clear.
func (c *EmbeddingCache) Clear() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var n int
	if err := c.db.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&n); err != nil {
		return 0, fmt.Errorf("embedding cache: count before clear: %w", err)
	}
	if _, err := c.db.Exec("DELETE FROM embeddings"); err != nil {
		return 0, fmt.Errorf("embedding cache: delete all: %w", err)
	}
	c.hits.Store(0)
	c.misses.Store(0)
	return n, nil
}

// Stats returns a snapshot. Never errors on a healthy DB but propagates
// any DB error from the underlying COUNT.
func (c *EmbeddingCache) Stats() (Stats, error) {
	n, err := c.Len()
	if err != nil {
		return Stats{}, err
	}
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	var rate float64
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	return Stats{
		Size:      n,
		MaxSize:   c.maxSize,
		Hits:      hits,
		Misses:    misses,
		Evictions: 0,
		HitRate:   rate,
	}, nil
}

// evictLocked deletes rows until count <= maxSize. Called by Put while
// holding c.mu. Uses created_at ASC to match Python's FIFO eviction.
func (c *EmbeddingCache) evictLocked() error {
	var n int
	if err := c.db.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&n); err != nil {
		return fmt.Errorf("embedding cache: count for evict: %w", err)
	}
	if n <= c.maxSize {
		return nil
	}
	excess := n - c.maxSize
	_, err := c.db.Exec(
		"DELETE FROM embeddings WHERE key IN "+
			"(SELECT key FROM embeddings ORDER BY created_at ASC LIMIT ?)",
		excess,
	)
	if err != nil {
		return fmt.Errorf("embedding cache: evict: %w", err)
	}
	return nil
}
