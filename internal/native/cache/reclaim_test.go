package cache

// Space reclamation. SQLite reuses freed pages but never returns them to the
// filesystem unless auto_vacuum is on. Eviction and Clear both delete, so
// without it the file only grows: one real cache reached 2.16 GB holding
// ~7 MB of rows. Mirrors the Python tests in tests/test_embedding_cache.py --
// both stacks share the file, so both must reclaim the same way.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func pragmaInt(t *testing.T, path, name string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func fill(t *testing.T, c *EmbeddingCache, n int) {
	t.Helper()
	vec := make([]float32, 512)
	for i := range vec {
		vec[i] = 0.5
	}
	for i := 0; i < n; i++ {
		if err := c.Put(fmt.Sprintf("key-%d", i), vec, ""); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
}

func TestOpen_NewFileUsesIncrementalAutoVacuum(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if got := pragmaInt(t, filepath.Join(dir, dbFileName), "auto_vacuum"); got != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", got)
	}
}

func TestOpen_ConvertsAndShrinksAnExistingBloatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, dbFileName)

	// The shape of a pre-fix cache: auto_vacuum NONE, rows written then
	// deleted, freed pages left behind.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE embeddings (key TEXT PRIMARY KEY, value BLOB NOT NULL,
		created_at REAL NOT NULL DEFAULT (unixepoch('now')))`); err != nil {
		t.Fatal(err)
	}
	val := "[" + strings.TrimSuffix(strings.Repeat("0.5,", 512), ",") + "]"
	for i := 0; i < 400; i++ {
		if _, err := raw.Exec(`INSERT INTO embeddings (key, value) VALUES (?, ?)`, fmt.Sprintf("k%d", i), val); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`DELETE FROM embeddings`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if got := pragmaInt(t, path, "auto_vacuum"); got != 0 {
		t.Fatalf("fixture auto_vacuum = %d, want 0", got)
	}
	if free := pragmaInt(t, path, "freelist_count"); free <= 50 {
		t.Fatalf("fixture should leave freed pages behind, got %d", free)
	}

	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	if got := pragmaInt(t, path, "auto_vacuum"); got != 2 {
		t.Errorf("auto_vacuum after open = %d, want 2", got)
	}
	if got := pragmaInt(t, path, "freelist_count"); got != 0 {
		t.Errorf("freelist_count after open = %d, want 0", got)
	}
}

func TestClear_ReturnsSpaceToTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, dbFileName)
	c, err := Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	fill(t, c, 300)
	full := pragmaInt(t, path, "page_count")

	if _, err := c.Clear(); err != nil {
		t.Fatal(err)
	}

	if got := pragmaInt(t, path, "freelist_count"); got != 0 {
		t.Errorf("freelist_count after Clear = %d, want 0", got)
	}
	if got := pragmaInt(t, path, "page_count"); got >= full/4 {
		t.Errorf("page_count after Clear = %d, want < %d (a quarter of %d)", got, full/4, full)
	}
}

func TestEvict_ReturnsSpaceToTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	fill(t, c, 300) // every Put past 50 evicts

	if n, _ := c.Len(); n != 50 {
		t.Fatalf("Len = %d, want 50", n)
	}
	if got := pragmaInt(t, filepath.Join(dir, dbFileName), "freelist_count"); got != 0 {
		t.Errorf("freelist_count after eviction = %d, want 0", got)
	}
}

// The cache file is shared with the Python server, so both must evict at the
// same ceiling. Python's effective default has been 100,000 since 2026-04-23
// and is raised via EMBEDDING_CACHE_MAX_SIZE (500,000 for a LongMemEval
// re-run). A Go side capped at 10,000 deleted every Python row past that on
// its next Put -- ~490,000 rows at once against a benchmark cache.
func TestDefault_MaxSizeMatchesPython(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		want      int
	}{
		{"unset -> python default", "", 100_000},
		{"env honoured, as in python", "500000", 500_000},
		{"unparseable -> default", "lots", 100_000},
		{"non-positive -> default", "0", 100_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OGHAM_CACHE_DIR", t.TempDir())
			t.Setenv("EMBEDDING_CACHE_MAX_SIZE", tc.env)
			ResetDefault()
			t.Cleanup(ResetDefault)
			c, err := Default()
			if err != nil {
				t.Fatal(err)
			}
			st, err := c.Stats()
			if err != nil {
				t.Fatal(err)
			}
			if st.MaxSize != tc.want {
				t.Fatalf("MaxSize = %d, want %d", st.MaxSize, tc.want)
			}
		})
	}
}
