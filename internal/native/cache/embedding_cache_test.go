package cache

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------
// Key() tests.
//
// These prove the Go key function is byte-for-byte compatible with the
// Python _cache_key in src/ogham/embeddings.py. The reference hashes
// were produced by running the Python side against the same inputs.
// ---------------------------------------------------------------------

func TestKey_MatchesPythonHash(t *testing.T) {
	// Reference hashes produced by:
	//   python3 -c 'import hashlib; print(hashlib.sha256(b"openai:text-embedding-3-small:512:hello world").hexdigest())'
	// Same formula as Python's _cache_key -- proves byte-for-byte parity.
	cases := []struct {
		name            string
		provider, model string
		dim             int
		text            string
		want            string
	}{
		{
			name:     "openai 512 hello world",
			provider: "openai", model: "text-embedding-3-small", dim: 512, text: "hello world",
			want: "6aaa6ba290e78f5c9d9ad9ee0051c2ebe22b969eed26a4a79733b7c3458977af",
		},
		{
			name:     "ollama 768 gemma local",
			provider: "ollama", model: "embeddinggemma", dim: 768, text: "gemma local",
			want: "bd27b6154cee4132aa33c9354e6524ee1124e7894b0006d34c26df9223414b73",
		},
		{
			name:     "unicode cafe",
			provider: "openai", model: "text-embedding-3-small", dim: 512, text: "unicode: café ☕ — résumé",
			want: "51f4fd0b30c6a7f8afdb219640882f210bbb96ed075a393f52d8460693470719",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Key(tc.provider, tc.model, tc.dim, tc.text)
			if got != tc.want {
				t.Errorf("Key = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKey_ScopesChangeHash(t *testing.T) {
	// Flipping any axis of the scope must change the key. Protects
	// against a provider-or-dim change silently reading stale vectors.
	base := Key("openai", "text-embedding-3-small", 512, "same text")
	cases := []struct {
		name string
		got  string
	}{
		{"provider changed", Key("voyage", "text-embedding-3-small", 512, "same text")},
		{"model changed", Key("openai", "text-embedding-3-large", 512, "same text")},
		{"dim changed", Key("openai", "text-embedding-3-small", 768, "same text")},
		{"text changed", Key("openai", "text-embedding-3-small", 512, "different text")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == base {
				t.Errorf("scope change produced identical key: %s", tc.got)
			}
		})
	}
}

func TestKey_Stable(t *testing.T) {
	// Repeated calls must return the same key -- no hidden state.
	k1 := Key("openai", "text-embedding-3-small", 512, "x")
	k2 := Key("openai", "text-embedding-3-small", 512, "x")
	if k1 != k2 {
		t.Errorf("Key not stable: %s vs %s", k1, k2)
	}
}

// ---------------------------------------------------------------------
// Open + constructor behaviour.
// ---------------------------------------------------------------------

func openTmp(t *testing.T, maxSize int) *EmbeddingCache {
	t.Helper()
	c, err := Open(t.TempDir(), maxSize)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestOpen_DefaultPath(t *testing.T) {
	// Empty cacheDir resolves to $HOME/.cache/ogham/. Point HOME at a
	// temp dir so we don't pollute the developer's real cache.
	home := t.TempDir()
	t.Setenv("HOME", home)
	c, err := Open("", 0)
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	defer func() { _ = c.Close() }()

	expected := filepath.Join(home, ".cache", "ogham", "embeddings.db")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected cache file at %s: %v", expected, err)
	}
}

func TestOpen_DefaultMaxSize(t *testing.T) {
	c := openTmp(t, 0) // 0 => DefaultMaxSize
	s, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.MaxSize != DefaultMaxSize {
		t.Errorf("MaxSize = %d, want %d", s.MaxSize, DefaultMaxSize)
	}
}

func TestOpen_CreatesSchemaAndSparseColumn(t *testing.T) {
	c := openTmp(t, 100)
	// Put + GetFull proves sparse column exists and round-trips.
	k := Key("openai", "text-embedding-3-small", 512, "sparse-test")
	if err := c.Put(k, []float32{0.1, 0.2}, "{1:0.5}/1024"); err != nil {
		t.Fatalf("Put with sparse: %v", err)
	}
	_, sparse, ok, err := c.GetFull(k)
	if err != nil || !ok {
		t.Fatalf("GetFull: ok=%v err=%v", ok, err)
	}
	if sparse != "{1:0.5}/1024" {
		t.Errorf("sparse = %q, want {1:0.5}/1024", sparse)
	}
}

// ---------------------------------------------------------------------
// Put / Get round-trip + misses.
// ---------------------------------------------------------------------

func TestPut_Get_RoundTrip(t *testing.T) {
	c := openTmp(t, 100)
	k := Key("openai", "text-embedding-3-small", 512, "hello")
	want := []float32{0.1, 0.2, -0.3, 1.0}
	if err := c.Put(k, want, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := c.Get(k)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if math.Abs(float64(got[i]-want[i])) > 1e-7 {
			t.Errorf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestGet_Miss(t *testing.T) {
	c := openTmp(t, 100)
	got, ok, err := c.Get("definitely-not-a-real-key")
	if err != nil {
		t.Fatalf("Get miss: %v", err)
	}
	if ok || got != nil {
		t.Errorf("miss returned (%v, %v), want (nil, false)", got, ok)
	}
	s, _ := c.Stats()
	if s.Misses != 1 {
		t.Errorf("misses = %d, want 1", s.Misses)
	}
}

func TestGetFull_Miss(t *testing.T) {
	c := openTmp(t, 100)
	vec, sparse, ok, err := c.GetFull("not-present")
	if err != nil {
		t.Fatalf("GetFull miss: %v", err)
	}
	if ok || vec != nil || sparse != "" {
		t.Errorf("miss returned (%v, %q, %v), want (nil, \"\", false)", vec, sparse, ok)
	}
}

func TestGet_HitMissCounters(t *testing.T) {
	c := openTmp(t, 100)
	k := Key("openai", "text-embedding-3-small", 512, "count me")
	_ = c.Put(k, []float32{1, 2, 3}, "")

	for i := 0; i < 3; i++ {
		_, _, _ = c.Get(k)
	}
	_, _, _ = c.Get("nope")
	_, _, _ = c.Get("also-nope")

	s, _ := c.Stats()
	if s.Hits != 3 || s.Misses != 2 {
		t.Errorf("hits=%d misses=%d, want 3/2", s.Hits, s.Misses)
	}
	if math.Abs(s.HitRate-0.6) > 1e-9 {
		t.Errorf("hit_rate = %v, want 0.6", s.HitRate)
	}
}

func TestPut_OverwriteSameKey(t *testing.T) {
	c := openTmp(t, 100)
	k := Key("openai", "text-embedding-3-small", 512, "overwrite")
	_ = c.Put(k, []float32{0.1}, "")
	_ = c.Put(k, []float32{0.9, 0.8}, "")
	got, ok, _ := c.Get(k)
	if !ok {
		t.Fatal("Get: miss after overwrite")
	}
	if len(got) != 2 || got[0] != 0.9 || got[1] != 0.8 {
		t.Errorf("overwrite lost: %v", got)
	}
}

// ---------------------------------------------------------------------
// Contains / Len / Clear / Stats.
// ---------------------------------------------------------------------

func TestContains(t *testing.T) {
	c := openTmp(t, 100)
	k := Key("openai", "text-embedding-3-small", 512, "x")
	ok, err := c.Contains(k)
	if err != nil || ok {
		t.Errorf("pre-put: ok=%v err=%v", ok, err)
	}
	_ = c.Put(k, []float32{1}, "")
	ok, err = c.Contains(k)
	if err != nil || !ok {
		t.Errorf("post-put: ok=%v err=%v", ok, err)
	}
}

func TestLen(t *testing.T) {
	c := openTmp(t, 100)
	n, err := c.Len()
	if err != nil || n != 0 {
		t.Errorf("empty Len = %d err=%v", n, err)
	}
	for i := 0; i < 5; i++ {
		k := Key("openai", "m", 512, fmt.Sprintf("t%d", i))
		_ = c.Put(k, []float32{float32(i)}, "")
	}
	n, _ = c.Len()
	if n != 5 {
		t.Errorf("Len = %d, want 5", n)
	}
}

func TestClear(t *testing.T) {
	c := openTmp(t, 100)
	for i := 0; i < 3; i++ {
		k := Key("openai", "m", 512, fmt.Sprintf("t%d", i))
		_ = c.Put(k, []float32{float32(i)}, "")
	}
	// Warm hit/miss counters.
	_, _, _ = c.Get(Key("openai", "m", 512, "t0"))
	_, _, _ = c.Get("missing")

	n, err := c.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n != 3 {
		t.Errorf("Clear returned %d, want 3", n)
	}
	after, _ := c.Len()
	if after != 0 {
		t.Errorf("Len after clear = %d, want 0", after)
	}
	s, _ := c.Stats()
	if s.Hits != 0 || s.Misses != 0 {
		t.Errorf("counters not reset: hits=%d misses=%d", s.Hits, s.Misses)
	}
}

// ---------------------------------------------------------------------
// Eviction (FIFO by created_at ASC) matches Python.
// ---------------------------------------------------------------------

func TestEviction_RemovesOldest(t *testing.T) {
	c := openTmp(t, 3)
	// Use hand-picked created_at values to force deterministic order.
	// The cache itself sets created_at via unixepoch('now'); to test
	// eviction ordering we drop down to the raw sql.DB handle.
	_, err := c.db.Exec("INSERT INTO embeddings (key, value, created_at, sparse) VALUES (?, ?, ?, NULL)",
		"k-oldest", []byte("[1]"), 100.0)
	if err != nil {
		t.Fatalf("seed oldest: %v", err)
	}
	_, err = c.db.Exec("INSERT INTO embeddings (key, value, created_at, sparse) VALUES (?, ?, ?, NULL)",
		"k-middle", []byte("[2]"), 200.0)
	if err != nil {
		t.Fatalf("seed middle: %v", err)
	}
	_, err = c.db.Exec("INSERT INTO embeddings (key, value, created_at, sparse) VALUES (?, ?, ?, NULL)",
		"k-newer", []byte("[3]"), 300.0)
	if err != nil {
		t.Fatalf("seed newer: %v", err)
	}
	// 3 rows at maxSize=3. Add a 4th via Put to trigger eviction.
	if err := c.Put("k-newest", []float32{4}, ""); err != nil {
		t.Fatalf("Put newest: %v", err)
	}

	got, _ := c.Len()
	if got != 3 {
		t.Errorf("Len after evict = %d, want 3", got)
	}

	// Oldest must be gone, the other three must remain.
	has := func(k string) bool {
		ok, err := c.Contains(k)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if has("k-oldest") {
		t.Error("k-oldest should have been evicted")
	}
	for _, k := range []string{"k-middle", "k-newer", "k-newest"} {
		if !has(k) {
			t.Errorf("%s should still be present", k)
		}
	}
}

// ---------------------------------------------------------------------
// Schema migration: opening a legacy DB without the sparse column
// should transparently add it.
// ---------------------------------------------------------------------

func TestMigration_AddsSparseColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "embeddings.db")

	// Write a legacy DB with no sparse column, matching an older
	// Python writer that predates the migration.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE embeddings (
		key TEXT PRIMARY KEY,
		value BLOB NOT NULL,
		created_at REAL NOT NULL DEFAULT (unixepoch('now'))
	)`)
	if err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	_ = legacy.Close()

	// Open() should notice the missing column and ALTER it in.
	c, err := Open(dir, 100)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Put with a sparse value to confirm the column is writable now.
	k := Key("openai", "m", 512, "post-migration")
	if err := c.Put(k, []float32{1}, "{0:1.0}/4"); err != nil {
		t.Fatalf("Put post-migration: %v", err)
	}
	_, sparse, ok, err := c.GetFull(k)
	if err != nil || !ok || sparse != "{0:1.0}/4" {
		t.Errorf("post-migration GetFull: ok=%v sparse=%q err=%v", ok, sparse, err)
	}
}

// ---------------------------------------------------------------------
// Concurrency: many goroutines Putting and Getting should not race.
// Use -race when running these.
// ---------------------------------------------------------------------

func TestConcurrent_16Goroutines(t *testing.T) {
	c := openTmp(t, 10_000)

	const workers = 16
	const perWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := Key("openai", "m", 512, fmt.Sprintf("w%d-i%d", w, i))
				_ = c.Put(k, []float32{float32(w), float32(i)}, "")
				_, _, _ = c.Get(k)
			}
		}()
	}
	wg.Wait()

	n, _ := c.Len()
	if n != workers*perWorker {
		t.Errorf("Len = %d, want %d", n, workers*perWorker)
	}
}

// ---------------------------------------------------------------------
// Fixture consumer: open the Python-written fixture at
// ../testdata/cache/fixture.db and verify every row decodes into the
// same float32 values the Python generator wrote.
//
// Proves cross-stack round-trip compatibility: Python writer + Go
// reader see identical vectors within float32 precision (1e-7).
// ---------------------------------------------------------------------

type pyFixtureRow struct {
	provider string
	model    string
	dim      int
	text     string
	vec      []float32
	sparse   string
}

// fixtureRows mirrors the FIXTURES list at the top of
// ../testdata/cache/gen_cache_fixture.py. If the generator changes,
// update this slice in the same commit so the test stays in sync.
var fixtureRows = []pyFixtureRow{
	{"openai", "text-embedding-3-small", 512, "hello world", []float32{0.1, 0.2, 0.3, 0.4}, ""},
	{"openai", "text-embedding-3-small", 512, "second entry", []float32{0.01, 0.02, 0.03}, ""},
	{"ollama", "embeddinggemma", 768, "gemma local", []float32{-0.5, 0.0, 0.5, 1.0, -1.0}, ""},
	{"ollama", "embeddinggemma", 512, "gemma mrl truncated", []float32{0.11, 0.22, 0.33, 0.44}, ""},
	{"gemini", "gemini-embedding-2-preview", 512, "normalized sub-3072", []float32{0.6, 0.8}, ""},
	{"voyage", "voyage-3-lite", 512, "voyage doc path", []float32{0.25, 0.25, 0.25, 0.25}, ""},
	{"mistral", "mistral-embed", 1024, "mistral fixed dim", []float32{0.125, 0.125, 0.125, 0.125, 0.125, 0.125, 0.125, 0.125}, ""},
	{"openai", "text-embedding-3-small", 512, "unicode: café ☕ — résumé", []float32{0.01, -0.01, 0.02, -0.02}, ""},
	{"openai", "text-embedding-3-small", 512, "sparse alongside dense", []float32{0.9, 0.1}, "{1:0.5,3:0.25}/1024"},
	{"openai", "text-embedding-3-small", 512, "deterministic row 10",
		[]float32{0.001, 0.002, 0.003, 0.004, 0.005, 0.006, 0.007, 0.008, 0.009, 0.010}, ""},
}

func TestFixtureConsumer(t *testing.T) {
	// Fixture lives under internal/native/testdata/cache/ (committed
	// alongside the Python generator). Open it via a copy in t.TempDir
	// so WAL / journal artifacts don't pollute the repo tree.
	src := filepath.Join("..", "testdata", "cache", "fixture.db")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "embeddings.db")
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	c, err := Open(dir, 100)
	if err != nil {
		t.Fatalf("Open fixture: %v", err)
	}
	defer func() { _ = c.Close() }()

	if n, _ := c.Len(); n != len(fixtureRows) {
		t.Fatalf("fixture row count = %d, want %d", n, len(fixtureRows))
	}

	for _, row := range fixtureRows {
		t.Run(row.text, func(t *testing.T) {
			k := Key(row.provider, row.model, row.dim, row.text)
			vec, sparse, ok, err := c.GetFull(k)
			if err != nil || !ok {
				t.Fatalf("GetFull(%s): ok=%v err=%v", k, ok, err)
			}
			if sparse != row.sparse {
				t.Errorf("sparse = %q, want %q", sparse, row.sparse)
			}
			if len(vec) != len(row.vec) {
				t.Fatalf("vec len = %d, want %d", len(vec), len(row.vec))
			}
			for i := range vec {
				if math.Abs(float64(vec[i]-row.vec[i])) > 1e-7 {
					t.Errorf("vec[%d] = %v, want %v (diff %v)",
						i, vec[i], row.vec[i],
						math.Abs(float64(vec[i]-row.vec[i])))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// Error paths -- make sure the 90% coverage gate sees them.
// ---------------------------------------------------------------------

func TestGet_CorruptJSONReturnsError(t *testing.T) {
	c := openTmp(t, 100)
	// Bypass Put() to inject a row with a malformed value blob.
	_, err := c.db.Exec("INSERT INTO embeddings (key, value, sparse) VALUES (?, ?, NULL)",
		"corrupt", []byte("not json at all"))
	if err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	_, _, err = c.Get("corrupt")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestGetFull_CorruptJSONReturnsError(t *testing.T) {
	c := openTmp(t, 100)
	_, err := c.db.Exec("INSERT INTO embeddings (key, value, sparse) VALUES (?, ?, NULL)",
		"corrupt", []byte("also not json"))
	if err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	_, _, _, err = c.GetFull("corrupt")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestOpen_CreateDirFailure(t *testing.T) {
	// Point cacheDir at a path whose parent is a regular file, so
	// os.MkdirAll errors.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(blocker, "child")
	if _, err := Open(blocked, 100); err == nil {
		t.Fatal("expected Open error when cacheDir cannot be created")
	}
}

// ---------------------------------------------------------------------
// Sanity check: hand-compute a hash the same way Python does, confirm
// it matches Key(). Guards against future drift in fmt.Sprintf encoding.
// ---------------------------------------------------------------------

func TestKey_ManualSHA256Agrees(t *testing.T) {
	payload := "openai:text-embedding-3-small:512:hello world"
	sum := sha256.Sum256([]byte(payload))
	want := hex.EncodeToString(sum[:])
	got := Key("openai", "text-embedding-3-small", 512, "hello world")
	if got != want {
		t.Errorf("Key = %s, hand-computed = %s", got, want)
	}
}

// Unused import guard so go vet doesn't complain if json stops being
// used after future refactors.
var _ = json.Marshal

// ---------------------------------------------------------------------
// Error paths when the DB handle is closed. Covers the error branches
// that are otherwise unreachable without mocking database/sql.
// ---------------------------------------------------------------------

func openClosed(t *testing.T) *EmbeddingCache {
	t.Helper()
	c, err := Open(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Seed a row so Get/GetFull actually reach the scan branch before
	// the closed-handle error surfaces. Otherwise some callers would
	// short-circuit on sql.ErrNoRows first and miss the target line.
	k := Key("openai", "m", 512, "seed")
	if err := c.Put(k, []float32{1}, "sparse-seed"); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return c
}

func TestClosedCache_GetErrors(t *testing.T) {
	c := openClosed(t)
	if _, _, err := c.Get("seed"); err == nil {
		t.Error("Get on closed DB: expected error")
	}
}

func TestClosedCache_GetFullErrors(t *testing.T) {
	c := openClosed(t)
	if _, _, _, err := c.GetFull("seed"); err == nil {
		t.Error("GetFull on closed DB: expected error")
	}
}

func TestClosedCache_PutErrors(t *testing.T) {
	c := openClosed(t)
	if err := c.Put("x", []float32{1}, ""); err == nil {
		t.Error("Put on closed DB: expected error")
	}
}

func TestClosedCache_ContainsErrors(t *testing.T) {
	c := openClosed(t)
	if _, err := c.Contains("x"); err == nil {
		t.Error("Contains on closed DB: expected error")
	}
}

func TestClosedCache_LenErrors(t *testing.T) {
	c := openClosed(t)
	if _, err := c.Len(); err == nil {
		t.Error("Len on closed DB: expected error")
	}
}

func TestClosedCache_ClearErrors(t *testing.T) {
	c := openClosed(t)
	// Clear's first operation is a COUNT -- errors there surface as the
	// returned error with n=0.
	if _, err := c.Clear(); err == nil {
		t.Error("Clear on closed DB: expected error")
	}
}

func TestClosedCache_StatsErrors(t *testing.T) {
	c := openClosed(t)
	if _, err := c.Stats(); err == nil {
		t.Error("Stats on closed DB: expected error")
	}
}

// Marshal-path error: Go's json encoder fails on NaN/Inf floats. Put
// should surface that cleanly rather than inserting a corrupt row.
func TestPut_NaNReturnsMarshalError(t *testing.T) {
	c := openTmp(t, 100)
	nan := float32(math.NaN())
	if err := c.Put("nan-key", []float32{nan, 1.0}, ""); err == nil {
		t.Error("Put with NaN: expected marshal error")
	}
	// And the row must not exist -- Put must fail atomically.
	if ok, _ := c.Contains("nan-key"); ok {
		t.Error("Put with NaN left a row in the cache")
	}
}

// ---------------------------------------------------------------------
// PICT matrix over Key() scoping. The committed .pict.tsv is the
// contract; regenerate via `make pict-regen` after editing .pict.
// ---------------------------------------------------------------------

// expandTextShape turns the PICT TextShape sentinel into concrete text.
// Centralising the mapping keeps the .tsv readable and makes the text
// length variation meaningful for the hash input.
func expandTextShape(shape string) string {
	switch shape {
	case "ascii":
		return "the quick brown fox"
	case "unicode":
		return "résumé naïve café Ω ☕ 中文"
	case "empty":
		return ""
	case "very-long":
		return strings.Repeat("long text block ", 64) // ~1 KB
	default:
		return shape
	}
}

// loadPICTMatrix reads a PICT-generated tsv, returning header + rows.
func loadPICTMatrix(t *testing.T, path string) (header []string, rows [][]string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	// PICT rows can be long -- bump the buffer past the default 64 KB.
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if header == nil {
			header = cols
			continue
		}
		rows = append(rows, cols)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return header, rows
}

func TestKey_PICT(t *testing.T) {
	header, rows := loadPICTMatrix(t, "testdata/key_scope.pict.tsv")
	if len(rows) == 0 {
		t.Fatal("empty PICT matrix -- run `make pict-regen`?")
	}

	col := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		t.Fatalf("missing header column %q in %v", name, header)
		return -1
	}
	providerCol := col("Provider")
	dimCol := col("Dim")
	textShapeCol := col("TextShape")

	seen := make(map[string]int, len(rows))
	for i, row := range rows {
		provider := row[providerCol]
		dim, err := strconv.Atoi(row[dimCol])
		if err != nil {
			t.Fatalf("row %d: non-integer dim %q", i, row[dimCol])
		}
		text := expandTextShape(row[textShapeCol])
		// Model name scales with provider but is not a PICT axis -- use
		// a fixed model-per-provider the way production does.
		model := fmt.Sprintf("%s-default", provider)

		got := Key(provider, model, dim, text)
		if len(got) != 64 {
			t.Errorf("row %d %v: Key len = %d, want 64", i, row, len(got))
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("row %d %v: Key not hex: %v", i, row, err)
		}
		// Manual sha256 agrees -- same formula as Python.
		payload := fmt.Sprintf("%s:%s:%d:%s", provider, model, dim, text)
		sum := sha256.Sum256([]byte(payload))
		want := hex.EncodeToString(sum[:])
		if got != want {
			t.Errorf("row %d %v: Key = %s, hand-computed = %s", i, row, got, want)
		}
		// Uniqueness: all PICT rows are distinct tuples by construction,
		// so their keys must all differ.
		if prev, ok := seen[got]; ok {
			t.Errorf("row %d %v collides with row %d %v: both key=%s",
				i, row, prev, rows[prev], got)
		}
		seen[got] = i
	}
}

// ---------------------------------------------------------------------
// Fuzz: Key() accepts arbitrary text bytes without panicking, and the
// output is always a 64-char lowercase-hex string.
// ---------------------------------------------------------------------

func FuzzKey(f *testing.F) {
	f.Add("openai", "text-embedding-3-small", 512, "hello")
	f.Add("ollama", "embeddinggemma", 768, "")
	f.Add("", "", 0, "")
	f.Add("voyage", "voyage-3-lite", 1024, "unicode: ☕ café")

	f.Fuzz(func(t *testing.T, provider, model string, dim int, text string) {
		got := Key(provider, model, dim, text)
		if len(got) != 64 {
			t.Errorf("Key(%q,%q,%d,%q) len = %d, want 64", provider, model, dim, text, len(got))
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("Key(%q,%q,%d,%q) not hex: %v", provider, model, dim, text, err)
		}
	})
}

// ---------------------------------------------------------------------
// Benchmark: hot-path Key() hashing. No allocation target -- fmt.Sprintf
// allocates the payload string, a fixed cost we accept for readability.
// ---------------------------------------------------------------------

func BenchmarkKey_ASCII(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Key("openai", "text-embedding-3-small", 512, "the quick brown fox jumps over the lazy dog")
	}
}

func BenchmarkKey_LongText(b *testing.B) {
	text := strings.Repeat("long ", 200) // 1 KB-ish
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Key("openai", "text-embedding-3-small", 512, text)
	}
}
