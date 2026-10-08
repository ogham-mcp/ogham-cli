package mcpserver

import (
	"os"
	"testing"
)

// TestMain bypasses the shared embedding cache for every test in this
// package, mirroring internal/native/main_test.go. That cache lives at
// $HOME/.cache/ogham/embeddings.db and is SHARED with the Python server --
// same schema, same keys. Without this guard the pgcontainer sweeps here
// wrote their stub's zero vectors into a developer's real cache and read
// them back on later runs, so a fixed stub still produced NaN scores.
//
// A test that needs the cache wrapper active can
// t.Setenv("OGHAM_EMBEDDING_CACHE", "1").
func TestMain(m *testing.M) {
	_ = os.Setenv("OGHAM_EMBEDDING_CACHE", "0")
	os.Exit(m.Run())
}
