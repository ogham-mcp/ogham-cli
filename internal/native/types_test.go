package native

import "testing"

// PICT-style coverage for ParseTLDRLevel:
//   parameter: input string
//   values: "", "body", "short", "one_line", and an invalid token
//   expected: every legitimate alias resolves to its constant; unknown
//             input errors rather than silently downgrading.
//
// Test parameters are pairwise-complete by virtue of being a single
// dimension -- one test per representative.
func TestParseTLDRLevel(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    TLDRLevel
		wantErr bool
	}{
		{"empty defaults to body", "", LevelBody, false},
		{"body", "body", LevelBody, false},
		{"short", "short", LevelShort, false},
		{"one_line", "one_line", LevelOneLine, false},
		{"invalid", "tldr", "", true},
		{"case-sensitive: BODY rejected", "BODY", "", true},
		{"case-sensitive: ShOrT rejected", "ShOrT", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTLDRLevel(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTLDRLevel(%q) want error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTLDRLevel(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseTLDRLevel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestProjectLevel verifies fallback semantics on partial-population
// rows (the v0.12 -> v0.13 migration case). PICT-style:
//   levels: one_line, short, body
//   row state: full, only_short_body, only_body
//   expected fallback: caller asks one_line on only_short_body -> short
func TestProjectLevel(t *testing.T) {
	full := func() *TopicSummary {
		one := "one-liner"
		shrt := "short paragraph"
		return &TopicSummary{Content: "BODY", TLDROneLine: &one, TLDRShort: &shrt}
	}
	onlyShortBody := func() *TopicSummary {
		shrt := "short paragraph"
		return &TopicSummary{Content: "BODY", TLDRShort: &shrt}
	}
	onlyBody := func() *TopicSummary {
		return &TopicSummary{Content: "BODY"}
	}

	cases := []struct {
		name      string
		row       *TopicSummary
		req       TLDRLevel
		wantBody  string
		wantLevel TLDRLevel
	}{
		// Full row: every level is honoured exactly.
		{"full + one_line", full(), LevelOneLine, "one-liner", LevelOneLine},
		{"full + short", full(), LevelShort, "short paragraph", LevelShort},
		{"full + body", full(), LevelBody, "BODY", LevelBody},
		// Pre-TLDR-generation row: short+body present, one_line missing.
		{"only_short_body + one_line falls to short", onlyShortBody(), LevelOneLine, "short paragraph", LevelShort},
		{"only_short_body + short", onlyShortBody(), LevelShort, "short paragraph", LevelShort},
		{"only_short_body + body", onlyShortBody(), LevelBody, "BODY", LevelBody},
		// Pre-033 row: only body. Every request falls back to body.
		{"only_body + one_line falls to body", onlyBody(), LevelOneLine, "BODY", LevelBody},
		{"only_body + short falls to body", onlyBody(), LevelShort, "BODY", LevelBody},
		{"only_body + body", onlyBody(), LevelBody, "BODY", LevelBody},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, lvl := projectLevel(tc.row, tc.req)
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if lvl != tc.wantLevel {
				t.Errorf("served level = %q, want %q", lvl, tc.wantLevel)
			}
		})
	}
}

// TestProjectLevel_EmptyTLDRsTreatedAsAbsent: a TLDR field pointing to
// the empty string ("" not nil) should still trigger fallback. Avoids
// returning an empty body to the caller when an LLM-generation glitch
// upserted a blank TLDR row.
func TestProjectLevel_EmptyTLDRsTreatedAsAbsent(t *testing.T) {
	empty := ""
	s := &TopicSummary{
		Content:     "BODY",
		TLDROneLine: &empty,
		TLDRShort:   &empty,
	}
	body, lvl := projectLevel(s, LevelOneLine)
	if body != "BODY" || lvl != LevelBody {
		t.Errorf("empty TLDRs should fall back to body; got %q %q", body, lvl)
	}
}
