package native

import (
	"os"
	"testing"
)

// TestMain bypasses the shared embedding cache by default for every
// test in this package. The cache is a process-wide singleton rooted
// at $HOME/.cache/ogham/ -- tests that run it would otherwise leak
// state across packages and across developer machines.
//
// Cache behaviour itself is covered by the cache-specific tests in
// internal/native/cache and by TestNewEmbedder_CacheWrapping below.
// Anyone writing a new test that needs the wrapper active can
// t.Setenv("OGHAM_EMBEDDING_CACHE", "1") inside the test.
func TestMain(m *testing.M) {
	_ = os.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	os.Exit(m.Run())
}
