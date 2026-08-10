package extraction

import (
	"strings"
	"testing"
)

// Regression tests for the v0.7 person-name classifier tightening.
//
// Covers the three-rule approach:
//   1. Punctuation gate -- reject tokens with interior "." or "(".
//   2. YAML denylist -- Docker, Postgres, Next, CLI, MCP, ...
//   3. Multi-lang stopwords union -- covers "Clear Stats", "Open Get"
//      method-name noise via the 34-language union Python uses.
//
// Positive path: "by Kevin", "from John", "user Alice" -- names
// preceded by context words continue to tag correctly.
//
// Source of today's false-positive list (2026-04-21 scratch smoke
// + memory 18d15505): person:Docker Postgres, person:Scratch DB,
// person:Next.js, person:Claude Code, person:Managed Agents,
// person:Clear Stats, person:Contains Len, person:Open Get,
// person:Put Contains, person:Agent Zero, person:Uses FastMCP.

// TestPersonTightening_RejectTechTerms asserts every known tech-term
// bigram from the task description is filtered out.
func TestPersonTightening_RejectTechTerms(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "docker postgres scratch",
			content: "Scratch DB smoke: local Docker Postgres on :5433 with pgvector/pgvector:pg17.",
		},
		{
			name:    "claude code repo",
			content: "The Claude Code release dropped support for the old transport.",
		},
		{
			name:    "managed agents reference",
			content: "Managed Agents are a separate product line from Claude Code.",
		},
		{
			name:    "method enumeration clear stats",
			content: "The EmbeddingCache type has Open, Get, Put, Contains, Len, Clear, Stats methods plus a Key() helper.",
		},
		{
			name:    "uses fastmcp",
			content: "Uses FastMCP with StdioTransport and exposes store_memory, hybrid_search, list_recent as tools.",
		},
		{
			name:    "agent zero task",
			content: "Maya Martins is interested in picking up the Agent Zero importer as a contributor task.",
		},
		{
			name:    "see PR link",
			content: "See PR #42 at https://github.com/ogham-mcp/ogham-cli/pull/42 for the Gemini normalization fix.",
		},
		{
			name:    "next.js interior dot",
			content: "The dashboard is a Next.js app on Vercel.",
		},
		{
			name:    "docker.postgres namespaced",
			content: "Call Docker.Postgres.Start() before the test.",
		},
	}

	bannedBigrams := []string{
		"person:Docker Postgres",
		"person:Scratch DB",
		"person:Next.js",
		"person:Claude Code",
		"person:Managed Agents",
		"person:Clear Stats",
		"person:Contains Len",
		"person:Open Get",
		"person:Put Contains",
		"person:Agent Zero",
		"person:Uses FastMCP",
		"person:See PR",
		"person:Next js",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entities(tc.content)
			for _, banned := range bannedBigrams {
				for _, g := range got {
					if g == banned {
						t.Errorf("unexpected %q in output %v", banned, got)
					}
				}
			}
			// Also assert no person: tag contains any denied unigram.
			for _, g := range got {
				if !strings.HasPrefix(g, "person:") {
					continue
				}
				lower := strings.ToLower(g)
				for _, d := range []string{
					"docker", "postgres", "next.js", "claude", "mcp",
					"cli", "sdk", "scratch",
				} {
					if strings.Contains(lower, d) {
						t.Errorf("person tag %q contains denied token %q", g, d)
					}
				}
			}
		})
	}
}

// TestPersonTightening_AcceptNames asserts the legitimate patterns the
// task calls out still emit person: tags after tightening.
func TestPersonTightening_AcceptNames(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "by Kevin Burns",
			content: "Change authored by Kevin Burns for the release.",
			want:    []string{"person:Kevin Burns"},
		},
		{
			name:    "from John Doe",
			content: "Feedback came from John Doe on Friday.",
			want:    []string{"person:John Doe"},
		},
		{
			name:    "user Alice Smith",
			content: "The bug was filed by user Alice Smith during the session.",
			want:    []string{"person:Alice Smith"},
		},
		{
			name:    "met Hiroshi Tanaka yesterday",
			content: "We met Hiroshi Tanaka yesterday at the conference.",
			want:    []string{"person:Hiroshi Tanaka"},
		},
		{
			name:    "name list with multiple",
			content: "Kevin Burns, Owen Fletcher and Luis Ramirez agreed.",
			want: []string{
				"person:Kevin Burns",
				"person:Owen Fletcher",
				"person:Luis Ramirez",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entities(tc.content)
			gotSet := map[string]struct{}{}
			for _, g := range got {
				gotSet[g] = struct{}{}
			}
			for _, w := range tc.want {
				if _, ok := gotSet[w]; !ok {
					t.Errorf("missing %q in %v", w, got)
				}
			}
		})
	}
}

// TestPersonTightening_NoFalsePositivesInScratchSmoke asserts no
// person: tag fires on the exact content that produced the bug report
// (memory 18d15505).
func TestPersonTightening_NoFalsePositivesInScratchSmoke(t *testing.T) {
	content := "Scratch DB smoke: local Docker Postgres on :5433 with pgvector/pgvector:pg17, " +
		"fresh schema load, round-trip test at 2026-04-21T20:05:32Z"
	got := Entities(content)
	for _, g := range got {
		if strings.HasPrefix(g, "person:") {
			t.Errorf("unexpected person tag in scratch smoke: %q (full: %v)", g, got)
		}
	}
}

// TestPersonTightening_NonEnglishLocaleMixesEnglishContext exercises
// the personGateFor non-English branch: a German memo with an English
// context word ("by Kevin") must still accept the name. Confirms the
// Gate's English baseline merge path.
func TestPersonTightening_NonEnglishLocaleMixesEnglishContext(t *testing.T) {
	content := "Die Analyse by Kevin Burns ergab ein Problem."
	got := EntitiesForLang(content, "de")
	found := false
	for _, g := range got {
		if g == "person:Kevin Burns" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected person:Kevin Burns in %v", got)
	}
}

// --- Rule 4: context gate for non-Titlecase bigrams (TBU-232) --------
//
// A real person-name token is Titlecase: uppercase initial, no interior
// uppercase. ALL-CAPS ("SECURITY DEFINER") and CamelCase
// ("Native PostToolUse") bigrams pass rules 1-3 whenever the denylist
// hasn't been populated for those specific terms, which is how
// person:SECURITY DEFINER reached the entity graph. Those now require a
// context word within personContextWindow tokens; Titlecase pairs do
// not, so "Kevin Burns, Owen Fletcher and Luis Ramirez agreed." is
// unaffected.

// TestPersonContextGate_RejectsAllCapsBigramWithoutContext pins the
// headline TBU-232 repro: an all-caps technical bigram with no
// licensing cue must not become a person.
func TestPersonContextGate_RejectsAllCapsBigramWithoutContext(t *testing.T) {
	content := "Migration 037 revokes EXECUTE on SECURITY DEFINER functions"
	got := Entities(content)
	for _, g := range got {
		if g == "person:SECURITY DEFINER" {
			t.Errorf("unexpected person:SECURITY DEFINER in %v", got)
		}
	}
}

// TestPersonContextGate_RejectsCamelCaseBigramWithoutContext covers the
// hook-captured command strings that produced person:Native PostToolUse
// and person:PostToolUse Defects (ogham-cli#26 finding 5).
func TestPersonContextGate_RejectsCamelCaseBigramWithoutContext(t *testing.T) {
	content := "Bash: gh issue create --title Native PostToolUse Defects"
	got := Entities(content)
	for _, g := range got {
		if strings.HasPrefix(g, "person:") {
			t.Errorf("unexpected person tag %q in %v", g, got)
		}
	}
}

// TestPersonContextGate_AcceptsNonTitlecaseNameWithContext asserts the
// gate is a context requirement, not an outright ban: a real name with
// interior capitals still tags when a cue word precedes it.
func TestPersonContextGate_AcceptsNonTitlecaseNameWithContext(t *testing.T) {
	content := "The migration was reviewed by DeShawn McArthur last week."
	got := Entities(content)
	found := false
	for _, g := range got {
		if g == "person:DeShawn McArthur" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected person:DeShawn McArthur in %v", got)
	}
}

// TestHasContextWordBefore_Direct exercises the helper explicitly, at
// the unit level. Rule 4 of addPersonNames is its production caller;
// these cases pin the window arithmetic independently of that path.
func TestHasContextWordBefore_Direct(t *testing.T) {
	ctx := map[string]struct{}{
		"by":   {},
		"from": {},
	}
	cases := []struct {
		name  string
		words []string
		idx   int
		want  bool
	}{
		{"token immediately before", []string{"by", "Kevin"}, 1, true},
		{"context at distance 2", []string{"signed", "by", "recent", "Kevin"}, 3, true},
		{"out of window", []string{"from", "a", "b", "c", "d", "Kevin"}, 5, false},
		{"no context", []string{"some", "random", "Kevin"}, 2, false},
		{"idx=0 has no before", []string{"Kevin"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasContextWordBefore(tc.words, tc.idx, ctx)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
