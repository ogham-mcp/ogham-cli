package native

import (
	"context"
	"strings"
	"testing"
)

func TestPgvectorLiteral(t *testing.T) {
	cases := []struct {
		in   []float32
		want string
	}{
		{nil, "[]"},
		{[]float32{}, "[]"},
		{[]float32{0.5}, "[0.5]"},
		{[]float32{1.0, 2.0, 3.0}, "[1,2,3]"},
		{[]float32{-0.1, 0.0, 0.1}, "[-0.1,0,0.1]"},
	}
	for _, tc := range cases {
		got := pgvectorLiteral(tc.in)
		if got != tc.want {
			t.Errorf("pgvectorLiteral(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPgvectorLiteral_LargeVector(t *testing.T) {
	// Real-world shape: 512-dim vector must serialize without issue and
	// produce a single well-formed bracket-wrapped literal.
	v := make([]float32, 512)
	for i := range v {
		v[i] = float32(i) / 512.0
	}
	got := pgvectorLiteral(v)
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Error("missing brackets")
	}
	// Number of commas should equal len-1 (512 values => 511 commas).
	commas := strings.Count(got, ",")
	if commas != 511 {
		t.Errorf("got %d commas, want 511", commas)
	}
}

func TestNullableStringSlice(t *testing.T) {
	if got := nullableStringSlice(nil); got != nil {
		t.Errorf("nil -> %v, want nil", got)
	}
	if got := nullableStringSlice([]string{}); got != nil {
		t.Errorf("empty -> %v, want nil", got)
	}
	if got := nullableStringSlice([]string{"a", "b"}); got == nil {
		t.Error("non-empty -> nil, want slice")
	}
}

func TestSearch_NoConfig(t *testing.T) {
	if _, err := Search(context.Background(), nil, "x", SearchOptions{}); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestSearch_NoBackendConfigured(t *testing.T) {
	cfg := &Config{Embedding: Embedding{Provider: "gemini", APIKey: "k"}}
	_, err := Search(context.Background(), cfg, "x", SearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "no database configured") {
		t.Errorf("expected no-database-configured error, got %v", err)
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	cfg := &Config{
		Database:  Database{URL: "postgres://x"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
	}
	if _, err := Search(context.Background(), cfg, "   ", SearchOptions{}); err == nil {
		t.Error("expected error on whitespace-only query")
	}
}

func TestSearch_EmbedderError_BeforeConnect(t *testing.T) {
	// No provider configured -- NewEmbedder fails, so we never attempt a DB
	// connection. This keeps the error message relevant (embedding, not DB)
	// when the user's config is wrong on the embedding side.
	cfg := &Config{
		Database: Database{URL: "postgres://nonexistent"},
	}
	_, err := Search(context.Background(), cfg, "query", SearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("expected provider error before connect, got %v", err)
	}
}
