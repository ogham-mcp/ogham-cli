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

// --- #33: all-caps shape rejection + weak-cue distance ----------------
//
// Rule 4 shipped in #27 leaned entirely on the cue list, and the cue
// list is 85% generic prepositions (by/to/from/with) by match volume.
// In technical prose one of those is routinely within 3 tokens, so
// `person:SECURITY DEFINER` -- the tag #27's commit message named --
// came straight back via "Grant EXECUTE to SECURITY DEFINER".
//
// Two independent gates now:
//   Rule 3b -- an all-caps bigram is never a name, whatever precedes it.
//   Rule 4  -- generic cues only license a bigram at distance 1.
//
// These cases are deliberately NOT drawn from testdata/parity: that
// corpus holds exactly 3 all-caps bigrams (ON CONFLICT / CONFLICT DO /
// DO NOTHING), all already killed at Rule 2, so it is structurally
// blind to this defect. Asserting on a corpus rate is what let #27 ship
// believing it was fixed.

func personTags(content string) []string {
	var out []string
	for _, e := range Entities(content) {
		if strings.HasPrefix(e, "person:") {
			out = append(out, e)
		}
	}
	return out
}

// TestAllCapsBigramIsNeverAPerson pins Rule 3b: shape alone rejects,
// regardless of any cue word in the window.
func TestAllCapsBigramIsNeverAPerson(t *testing.T) {
	cases := []struct{ name, content string }{
		{"cue 'to'", "Grant EXECUTE to SECURITY DEFINER functions in the migration."},
		{"cue 'by'", "The wrangler.jsonc is unset by default. THE BUILD BELONGS IN VERSION CONTROL."},
		{"cue 'from'", "Wrangler detects tool versions from the REPOSITORY ROOT regardless of subdir."},
		{"cue 'with'", "Deployed with STATIC ASSETS enabled on the account."},
		{"strong cue 'said'", "The reviewer said SECURITY DEFINER was the culprit."},
		{"no cue", "Nothing precedes it. SECURITY DEFINER appears alone."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personTags(tc.content); len(got) != 0 {
				t.Errorf("all-caps bigram produced %v", got)
			}
		})
	}
}

// TestGenericCueOnlyLicensesAtDistanceOne pins Rule 4's weak-cue
// tightening for the CamelCase route, which Rule 3b does not cover.
func TestGenericCueOnlyLicensesAtDistanceOne(t *testing.T) {
	// "from" three tokens back -- a drive-by preposition, not a byline.
	far := "Wrangler reads config from the deploy step. Native PostToolUse Defects remain."
	if got := personTags(far); len(got) != 0 {
		t.Errorf("distant generic cue licensed %v", got)
	}
	// "from" immediately before -- a genuine attribution.
	near := "Feedback came from John Doe on Friday."
	if got := personTags(near); len(got) != 1 || got[0] != "person:John Doe" {
		t.Errorf("adjacent generic cue should license the name, got %v", got)
	}
}

// TestStrongCuesStillWorkAtFullWindow asserts the tightening is aimed
// at the generic prepositions only -- verb and role cues keep the
// 3-token window.
func TestStrongCuesStillWorkAtFullWindow(t *testing.T) {
	// "met" at distance 3 from the bigram.
	content := "We met briefly last week Hiroshi Tanaka at the conference."
	found := false
	for _, g := range personTags(content) {
		if g == "person:Hiroshi Tanaka" {
			found = true
		}
	}
	if !found {
		t.Errorf("strong cue at distance 3 should still license, got %v", personTags(content))
	}
}

// TestIssue33DoesNotRegressGenuineNames is the control set. Every one
// of these passes today and must keep passing -- the Titlecase
// exemption is load-bearing.
func TestIssue33DoesNotRegressGenuineNames(t *testing.T) {
	cases := []struct{ content, want string }{
		{"Change authored by Kevin Burns for the release.", "person:Kevin Burns"},
		{"Feedback came from John Doe on Friday.", "person:John Doe"},
		{"The bug was filed by user Alice Smith during the session.", "person:Alice Smith"},
		{"Kevin Burns, Owen Fletcher and Luis Ramirez agreed.", "person:Kevin Burns"},
		{"The migration was reviewed by DeShawn McArthur last week.", "person:DeShawn McArthur"},
	}
	for _, tc := range cases {
		found := false
		for _, g := range personTags(tc.content) {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("lost %q from %q -- got %v", tc.want, tc.content, personTags(tc.content))
		}
	}
}

// TestWeakContextWordsAreASubsetOfContextWords guards a silent-failure
// mode in the YAML: hasLicensingCueBefore only consults WeakContext for
// tokens that already matched ContextWords, so a weak entry that is not
// also a context word is dead config -- it neither licenses nor
// restricts anything, and nothing would fail.
func TestWeakContextWordsAreASubsetOfContextWords(t *testing.T) {
	gate := personGateFor("en")
	if len(gate.WeakContext) == 0 {
		t.Fatal("en has no weak context words -- the #33 tightening is not loaded")
	}
	for w := range gate.WeakContext {
		if _, ok := gate.ContextWords[w]; !ok {
			t.Errorf("weak cue %q is not in person_name_context_words -- dead config", w)
		}
	}
}

// TestParityCorpusCannotSeeAllCapsBigrams documents why the corpus is
// not evidence for this rule, and fails if that ever stops being true
// so the comment cannot rot into a false claim.
//
// #27 shipped Rule 4 on a parity measurement and believed the all-caps
// case was fixed. It was not: the corpus holds 3 all-caps bigrams, all
// killed earlier at Rule 2, so the rate could not move either way. The
// #33 fix likewise moves it 0.0%. Judge person-name changes on the
// explicit fixtures above, never on the parity rate.
func TestParityCorpusCannotSeeAllCapsBigrams(t *testing.T) {
	fx := loadParityFixture(t)
	var reaching []string
	for _, r := range fx.Records {
		words := strings.Fields(r.Content)
		for i := 0; i < len(words)-1; i++ {
			w1, w2 := stripPersonPunct(words[i]), stripPersonPunct(words[i+1])
			// Only count pairs that would actually reach the new rule.
			if isLikelyPersonNamePart(w1) && isLikelyPersonNamePart(w2) &&
				isAllCapsToken(w1) && isAllCapsToken(w2) {
				reaching = append(reaching, w1+" "+w2)
			}
		}
	}
	if len(reaching) != 0 {
		t.Errorf("parity corpus now contains %d all-caps bigrams reaching Rule 3b (%v) -- "+
			"it is no longer blind to this defect, so update the comment above and "+
			"consider whether the parity rate is now meaningful here", len(reaching), reaching)
	}
}
