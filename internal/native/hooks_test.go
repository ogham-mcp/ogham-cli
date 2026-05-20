package native

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProjectFromCwd(t *testing.T) {
	cases := []struct {
		name string
		cwd  string
		want string
	}{
		{"empty", "", "current project"},
		{"dot", ".", "current project"},
		{"slash", "/", "current project"},
		{"simple", "/home/kevin/work", "work"},
		{"trailing-slash", "/home/kevin/work/", "work"},
		{"single-segment", "myproj", "myproj"},
		{"deep-nested", "/Users/kevin/Developer/web-projects/ogham-cli", "ogham-cli"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := projectFromCwd(c.cwd); got != c.want {
				t.Errorf("projectFromCwd(%q) = %q, want %q", c.cwd, got, c.want)
			}
		})
	}
}

func TestTypeTagsFromResult(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want []string
	}{
		{"empty", nil, nil},
		{"none-match", []string{"foo", "bar"}, nil},
		{"type-only", []string{"type:decision", "foo"}, []string{"type:decision"}},
		{"source-only", []string{"source:claude-code", "bar"}, []string{"source:claude-code"}},
		{
			"mixed",
			[]string{"type:decision", "project:ogham", "source:claude-code", "other"},
			[]string{"type:decision", "source:claude-code"},
		},
		{"order-preserved", []string{"source:a", "type:b"}, []string{"source:a", "type:b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := typeTagsFromResult(SearchResult{Tags: c.tags})
			if len(got) != len(c.want) {
				t.Fatalf("typeTagsFromResult: len(got)=%d, len(want)=%d (got=%v want=%v)", len(got), len(c.want), got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("typeTagsFromResult[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestFormatRecallMarkdown(t *testing.T) {
	results := []SearchResult{
		{
			Content: "Picked Stripe over Paddle for the EU VAT handling",
			Tags:    []string{"type:decision", "decision:stripe-vs-paddle"},
		},
		{
			Content: "Migration 038 verified end-to-end against scratch Supabase",
			Tags:    []string{"type:event", "source:claude-code"},
		},
	}

	got := formatRecallMarkdown(results, "ogham-cli", "Session Context", "loaded for", 200)

	// Check structural elements that the hook output contract relies on:
	// the heading line, the bullet lines, the count line. Avoids a brittle
	// full-string match while still pinning the format.
	if !strings.HasPrefix(got, "## Session Context\n\n") {
		t.Errorf("missing heading prefix; got:\n%s", got)
	}
	if !strings.Contains(got, "- Picked Stripe over Paddle for the EU VAT handling (type:decision)") {
		t.Errorf("missing decision bullet with filtered tags; got:\n%s", got)
	}
	if !strings.Contains(got, "- Migration 038 verified end-to-end against scratch Supabase (type:event, source:claude-code)") {
		t.Errorf("missing event bullet with both tag kinds; got:\n%s", got)
	}
	if !strings.HasSuffix(got, "*2 memories loaded for ogham-cli*\n") {
		t.Errorf("missing count suffix; got:\n%s", got)
	}
}

func TestFormatRecallMarkdownTruncatesContent(t *testing.T) {
	long := strings.Repeat("x", 500)
	results := []SearchResult{{Content: long, Tags: nil}}

	got := formatRecallMarkdown(results, "p", "H", "loaded for", 200)

	// Bullet content must be truncated to <= 200 chars even if source is 500.
	// We check via Count, not substring, because the truncation is a slice
	// operation -- the bullet still has the "- " prefix and newline around it.
	if strings.Count(got, "x") != 200 {
		t.Errorf("truncation: expected 200 x's, got %d", strings.Count(got, "x"))
	}
}

func TestFormatRecallMarkdownEmptyTags(t *testing.T) {
	results := []SearchResult{{Content: "no tags here", Tags: nil}}
	got := formatRecallMarkdown(results, "p", "H", "loaded for", 200)
	// When no tags pass the type/source filter, the bullet has no parenthesis.
	if !strings.Contains(got, "- no tags here\n") {
		t.Errorf("no-tags bullet should not have parens; got:\n%s", got)
	}
}

func TestInscribeDryRunReturnsContentWithoutStoring(t *testing.T) {
	cfg := &Config{} // empty: ResolveBackend would error, but DryRun should never call it

	content, err := Inscribe(context.Background(), cfg, "sess-abc", "/Users/kev/work/ogham-cli", HookOptions{
		Profile: "test",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("DryRun Inscribe: unexpected error: %v", err)
	}
	for _, want := range []string{
		"Session drain before compaction.",
		"Project: ogham-cli",
		"Directory: /Users/kev/work/ogham-cli",
		"Session: sess-abc",
		"Time: ",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("DryRun content missing %q; got:\n%s", want, content)
		}
	}
}

func TestInscribeDryRunHandlesEmptySessionID(t *testing.T) {
	cfg := &Config{}
	content, err := Inscribe(context.Background(), cfg, "", ".", HookOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Inscribe with empty session: %v", err)
	}
	if !strings.Contains(content, "Session: unknown") {
		t.Errorf("empty session ID should map to 'unknown'; got:\n%s", content)
	}
}

func TestSessionStartNilConfigErrors(t *testing.T) {
	_, err := SessionStart(context.Background(), nil, "/tmp", HookOptions{})
	if err == nil {
		t.Fatal("SessionStart(nil cfg) should error")
	}
}

func TestRecallNilConfigErrors(t *testing.T) {
	_, err := Recall(context.Background(), nil, "/tmp", HookOptions{})
	if err == nil {
		t.Fatal("Recall(nil cfg) should error")
	}
}

func TestInscribeNilConfigErrors(t *testing.T) {
	_, err := Inscribe(context.Background(), nil, "sess", "/tmp", HookOptions{})
	if err == nil {
		t.Fatal("Inscribe(nil cfg) should error")
	}
}

func TestInscribeDryRunRespectsContext(t *testing.T) {
	cfg := &Config{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	// DryRun is purely local; context cancellation should NOT prevent the
	// content from being composed. This test pins the behaviour so a
	// future refactor doesn't accidentally start blocking on ctx when
	// DryRun is set.
	time.Sleep(20 * time.Millisecond) // ctx is now done
	content, err := Inscribe(ctx, cfg, "s", ".", HookOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DryRun should ignore ctx state: %v", err)
	}
	if content == "" {
		t.Fatal("DryRun content should be non-empty even with done ctx")
	}
}
