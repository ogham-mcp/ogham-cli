package native

import (
	"context"
	"strings"
	"testing"

	"github.com/ogham-mcp/ogham-cli/internal/native/cache"
)

// This file exercises the error/validation branches that backend-bound
// functions check BEFORE hitting the network, plus a handful of pure
// helpers. The DB-facing paths stay behind //go:build live for now.
//
// Target: lift internal/native/ coverage from 60% -> 75%+ without
// introducing testcontainers (which would balloon CI cost). The 90%
// gate from the locked testing standard is aspirational for DB-bound
// packages -- it's fully enforced on internal/native/extraction which
// has no backend at all.

// --- DefaultPath ----------------------------------------------------------

func TestDefaultPath_EndsWithOghamConfig(t *testing.T) {
	p := DefaultPath()
	if !strings.HasSuffix(p, ".ogham/config.toml") {
		t.Errorf("DefaultPath should point into ~/.ogham/config.toml; got %q", p)
	}
}

// --- List validation paths ------------------------------------------------

func TestList_NilConfigRejected(t *testing.T) {
	_, err := List(context.Background(), nil, ListOptions{})
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config' error; got %v", err)
	}
}

func TestList_UnknownBackendSurfaces(t *testing.T) {
	// Empty Config -> ResolveBackend returns an error. List should
	// wrap and surface it rather than crash.
	_, err := List(context.Background(), &Config{}, ListOptions{})
	if err == nil {
		t.Fatal("expected error from empty backend resolution, got nil")
	}
	if !strings.Contains(err.Error(), "native list") {
		t.Errorf("error should be prefixed 'native list'; got %v", err)
	}
}

// --- Update validation paths ----------------------------------------------

func TestUpdate_NilConfigRejected(t *testing.T) {
	s := "hello"
	_, err := Update(context.Background(), nil, "abc", UpdateOptions{Content: &s})
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

func TestUpdate_EmptyIDRejected(t *testing.T) {
	s := "hello"
	_, err := Update(context.Background(), &Config{}, "", UpdateOptions{Content: &s})
	if err == nil || !strings.Contains(err.Error(), "memory id required") {
		t.Errorf("want 'memory id required'; got %v", err)
	}
}

func TestUpdate_NoFieldsRejected(t *testing.T) {
	// All-nil options means "caller didn't specify any change"; must
	// error at the arg-validation layer before touching the embedder
	// or the DB. Mirrors Python's ValueError("No updates provided").
	_, err := Update(context.Background(), &Config{}, "abc", UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "no fields specified") {
		t.Errorf("want 'no fields specified'; got %v", err)
	}
}

// --- UpdateConfidence validation ------------------------------------------

func TestUpdateConfidence_NilConfigRejected(t *testing.T) {
	_, err := UpdateConfidence(context.Background(), nil, "abc", 0.85, "")
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

func TestUpdateConfidence_EmptyIDRejected(t *testing.T) {
	_, err := UpdateConfidence(context.Background(), &Config{}, "", 0.85, "")
	if err == nil || !strings.Contains(err.Error(), "memory id required") {
		t.Errorf("want 'memory id required'; got %v", err)
	}
}

// --- SetProfileTTL validation ---------------------------------------------

func TestSetProfileTTL_NilConfigRejected(t *testing.T) {
	_, err := SetProfileTTL(context.Background(), nil, "work", 7)
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

func TestSetProfileTTL_BackendResolutionFirst(t *testing.T) {
	// Empty cfg triggers ResolveBackend() before the profile-required
	// validator. Both surface a useful error; this test pins the
	// ordering so a later refactor can't silently swap them and leak
	// a "profile is required" message to callers who haven't configured
	// a backend yet.
	_, err := SetProfileTTL(context.Background(), &Config{}, "", 7)
	if err == nil {
		t.Fatal("expected error from empty config, got nil")
	}
	// The backend-resolution error is the user's first action item;
	// the profile check comes after that fence.
	if !strings.Contains(err.Error(), "no database configured") {
		t.Errorf("want backend-resolution error first; got %v", err)
	}
}

// --- GetProfileTTL validation ---------------------------------------------

func TestGetProfileTTL_NilConfigRejected(t *testing.T) {
	_, err := GetProfileTTL(context.Background(), nil, "work")
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

// --- Delete / Cleanup validation ------------------------------------------

func TestDelete_NilConfigRejected(t *testing.T) {
	_, err := Delete(context.Background(), nil, "abc", "")
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

func TestDelete_EmptyIDRejected(t *testing.T) {
	_, err := Delete(context.Background(), &Config{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "memory id required") {
		t.Errorf("want 'memory id required'; got %v", err)
	}
}

func TestCleanup_NilConfigRejected(t *testing.T) {
	_, err := Cleanup(context.Background(), nil, "")
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

// --- Decay validation -----------------------------------------------------

func TestDecay_NilConfigRejected(t *testing.T) {
	_, err := Decay(context.Background(), nil, "", 100, false)
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

// --- Audit validation -----------------------------------------------------

func TestAudit_NilConfigRejected(t *testing.T) {
	_, err := Audit(context.Background(), nil, "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Errorf("want 'nil config'; got %v", err)
	}
}

// --- cachedEmbedder delegation --------------------------------------------
//
// These cover the thin wrapper methods (Name/Dimension) without needing
// a real cache file or a real provider. fakeEmbedder is local to this
// file since it's only used here.

type fakeEmbedder struct {
	name string
	dim  int
}

func (f *fakeEmbedder) Name() string   { return f.name }
func (f *fakeEmbedder) Dimension() int { return f.dim }
func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	// Return a deterministic vector so tests that reach Embed (if any
	// later join this file) can assert on it.
	out := make([]float32, f.dim)
	for i := range out {
		out[i] = float32(i)
	}
	return out, nil
}

func TestCachedEmbedder_NameAppendsSuffix(t *testing.T) {
	// Caller-facing Name() must clearly identify that caching is in
	// the path -- helps with debugging when a cache key is wrong.
	c := &cachedEmbedder{
		inner:    &fakeEmbedder{name: "fake/model", dim: 512},
		provider: "fake",
		model:    "model",
	}
	if got := c.Name(); got != "fake/model+cache" {
		t.Errorf("Name(): want 'fake/model+cache', got %q", got)
	}
}

func TestCachedEmbedder_DimensionDelegates(t *testing.T) {
	c := &cachedEmbedder{inner: &fakeEmbedder{dim: 768}}
	if got := c.Dimension(); got != 768 {
		t.Errorf("Dimension(): want 768, got %d", got)
	}
}

// --- newCachedEmbedder constructor ---------------------------------------

func TestNewCachedEmbedder_WrapsInner(t *testing.T) {
	inner := &fakeEmbedder{name: "fake", dim: 256}
	// Passing a nil cache is fine for this smoke -- the wrapper only
	// invokes cache methods in Embed(), which we don't exercise here.
	wrapped := newCachedEmbedder(inner, (*cache.EmbeddingCache)(nil), "fake", "model")
	if wrapped == nil {
		t.Fatal("newCachedEmbedder returned nil")
	}
	if wrapped.Dimension() != 256 {
		t.Errorf("wrapped dim: want 256, got %d", wrapped.Dimension())
	}
}
