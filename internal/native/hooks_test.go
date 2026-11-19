package native

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// envelopeCase is one row of the parity fixture shared with the Python
// implementation (tests/fixtures/recall_envelope_cases.json in the dev repo).
type envelopeCase struct {
	Name    string `json:"name"`
	Event   string `json:"event"`
	Project string `json:"project"`
	Profile string `json:"profile"`
	Rows    []struct {
		Content      string   `json:"content"`
		Tags         []string `json:"tags"`
		Source       string   `json:"source"`
		CreatedAt    *string  `json:"created_at"`
		SupersededBy *string  `json:"superseded_by"`
	} `json:"rows"`
	Expected string `json:"expected"`
}

func TestRecallEnvelopeParityFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/recall_envelope_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []envelopeCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	cases := fixture.Cases
	if len(cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var rows []SearchResult
			for _, r := range c.Rows {
				sr := SearchResult{Content: r.Content, Tags: r.Tags, Source: r.Source}
				if r.CreatedAt != nil {
					sr.CreatedAt = ParseEnvelopeTime(*r.CreatedAt)
				}
				if r.SupersededBy != nil {
					sr.SupersededBy = *r.SupersededBy
				}
				rows = append(rows, sr)
			}
			limit, trunc := 8, 200
			if c.Event == eventPostCompact {
				limit, trunc = 10, 300
			}
			got := formatRecallEnvelope(rows, c.Event, c.Project, c.Profile, limit, trunc)
			if !utf8.ValidString(got) {
				t.Errorf("output is not valid UTF-8: %q", got)
			}
			if got != c.Expected {
				t.Errorf("bytes differ\n got: %q\nwant: %q", got, c.Expected)
			}
			if n := utf8.RuneCountInString(got); n > envelopeMaxChars {
				t.Errorf("output is %d chars, cap is %d", n, envelopeMaxChars)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("a\u2192b", 2); got != "a\u2192\u2026" || !utf8.ValidString(got) {
		t.Errorf("got %q", got)
	}
	if got := truncateRunes("abc", 3); got != "abc" {
		t.Errorf("got %q", got)
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

func TestParseEnvelopeTime(t *testing.T) {
	for in, want := range map[string]string{
		"2026-05-01T23:30:00-05:00": "2026-05-02",
		"2026-11-10T09:30:00Z":      "2026-11-10",
		"2026-11-10T09:30:00":       "2026-11-10",
		"2026-11-10":                "2026-11-10",
		"":                          "",
		"yesterday":                 "",
	} {
		got := ParseEnvelopeTime(in)
		if want == "" {
			if !got.IsZero() {
				t.Errorf("%q: want undated, got %v", in, got)
			}
			continue
		}
		if g := got.UTC().Format("2006-01-02"); g != want {
			t.Errorf("%q: got %s want %s", in, g, want)
		}
	}
}

func TestCollapseASCIIWhitespaceKeepsUnicodeSpace(t *testing.T) {
	in := " a\u0085\u001f b\t\n\v\f\r c\u00a0d "
	if got, want := collapseASCIIWhitespace(in), "a\u0085\u001f b c\u00a0d"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
