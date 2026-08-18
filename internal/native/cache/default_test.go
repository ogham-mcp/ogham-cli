package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// Default and ResetDefault were the only exported functions in this
// package with no test at all. They went unnoticed while the extraction
// package carried enough well-covered code to hold the shared
// strict-pkgs threshold above 90%; removing the person: classifier
// (ogham-cli#44/#45) took that cushion away and exposed them.

// TestDefaultIsASingletonRootedAtTheEnvDir pins the documented
// behaviour: OGHAM_CACHE_DIR wins, and the first call is the one that
// decides -- later env changes do not move an already-open cache.
func TestDefaultIsASingletonRootedAtTheEnvDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envOghamCacheDir, dir)
	ResetDefault()
	t.Cleanup(ResetDefault)

	first, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if first == nil {
		t.Fatal("Default returned a nil cache and no error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, dbFileName)); statErr != nil {
		t.Errorf("cache not created under OGHAM_CACHE_DIR: %v", statErr)
	}

	// First call wins: repointing the env must not move the singleton.
	t.Setenv(envOghamCacheDir, t.TempDir())
	second, err := Default()
	if err != nil {
		t.Fatalf("second Default: %v", err)
	}
	if first != second {
		t.Error("Default returned a different instance on the second call")
	}
}

// TestDefaultFallsBackToEmbeddingCacheDir covers the second env var in
// the documented precedence order.
func TestDefaultFallsBackToEmbeddingCacheDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envOghamCacheDir, "")
	t.Setenv(envEmbeddingCacheDir, dir)
	ResetDefault()
	t.Cleanup(ResetDefault)

	if _, err := Default(); err != nil {
		t.Fatalf("Default: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, dbFileName)); err != nil {
		t.Errorf("cache not created under EMBEDDING_CACHE_DIR: %v", err)
	}
}

// TestResetDefaultAllowsReopeningElsewhere is the behaviour the helper
// exists for -- without the sync.Once reset the second Open would be
// skipped and the first cache returned.
func TestResetDefaultAllowsReopeningElsewhere(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()

	t.Setenv(envOghamCacheDir, dirA)
	ResetDefault()
	a, err := Default()
	if err != nil {
		t.Fatalf("Default A: %v", err)
	}

	t.Setenv(envOghamCacheDir, dirB)
	ResetDefault()
	t.Cleanup(ResetDefault)
	b, err := Default()
	if err != nil {
		t.Fatalf("Default B: %v", err)
	}

	if a == b {
		t.Error("ResetDefault did not clear the singleton")
	}
	if _, err := os.Stat(filepath.Join(dirB, dbFileName)); err != nil {
		t.Errorf("second cache not created under the new dir: %v", err)
	}
}

// TestResetDefaultOnAnUnopenedCacheIsSafe -- it is called from test
// cleanup paths that may run before any Default().
func TestResetDefaultOnAnUnopenedCacheIsSafe(t *testing.T) {
	ResetDefault()
	ResetDefault()
}
