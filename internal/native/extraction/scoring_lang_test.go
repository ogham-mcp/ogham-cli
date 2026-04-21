package extraction

import (
	"testing"
)

// TestImportanceForLang_German verifies German signal-word detection
// fires via the YAML-loaded DecisionWords / ErrorWords / ArchitectureWords
// lists. Without the language plumbing, these contents would score a
// flat 0.2 because the baked-in English slice misses every German
// stem.
func TestImportanceForLang_German(t *testing.T) {
	cases := []struct {
		name, content string
		tags          []string
		want          float64
	}{
		{
			name:    "decision signal (entschieden) adds 0.3",
			content: "Wir haben uns für Postgres entschieden.",
			want:    0.5, // 0.2 base + 0.3 decision
		},
		{
			name:    "error signal (fehler) adds 0.2",
			content: "Der Fehler tritt reproduzierbar auf.",
			want:    0.4, // 0.2 base + 0.2 error
		},
		{
			name:    "architecture signal (refaktorisierung) adds 0.2",
			content: "Wir planen eine Refaktorisierung des Clients.",
			want:    0.4, // 0.2 base + 0.2 arch
		},
		{
			name:    "all three German signals + file path + code fence = 0.9",
			content: "Entschieden: Refaktorisierung des Clients, siehe `./cmd/root.go`. Ein Fehler ist aufgetreten.",
			want:    0.9, // 0.2 + 0.3 + 0.2 + 0.2 + 0.1 (file) + 0.1 (code) - but also caps; 1.1 -> 1.0
			// Recalculating: 0.2 + 0.3 + 0.2 + 0.2 + 0.1 + 0.1 = 1.1 -> min 1.0.
			// Updated want below.
		},
	}
	// Fix the last case's want based on the cap.
	cases[3].want = 1.0

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ImportanceForLang(tc.content, tc.tags, "de")
			if !near(got, tc.want) {
				t.Errorf("ImportanceForLang(de)=%v, want %v (content=%q)", got, tc.want, tc.content)
			}
		})
	}
}

// TestImportanceForLang_EnglishIdenticalToImportance asserts the public
// Importance() alias matches ImportanceForLang(..., "en") bit-for-bit.
// Guards against silent divergence if either code path mutates.
func TestImportanceForLang_EnglishIdenticalToImportance(t *testing.T) {
	inputs := []struct {
		content string
		tags    []string
	}{
		{"we decided to ship", nil},
		{"RuntimeException surfaced", nil},
		{"edit ./cmd/root.go to fix it", []string{"a", "b", "c"}},
	}
	for _, in := range inputs {
		en := ImportanceForLang(in.content, in.tags, "en")
		base := Importance(in.content, in.tags)
		if !near(en, base) {
			t.Errorf("Importance(%q) = %v but ImportanceForLang(en)=%v", in.content, base, en)
		}
	}
}

// TestImportanceForLang_UnknownFallsBackToEnglish verifies an unknown
// language code doesn't panic and doesn't return 0 -- it resolves the
// English rules silently.
func TestImportanceForLang_UnknownFallsBackToEnglish(t *testing.T) {
	got := ImportanceForLang("we decided to ship", nil, "klingon")
	want := 0.5 // 0.2 base + 0.3 decision
	if !near(got, want) {
		t.Errorf("ImportanceForLang(klingon)=%v, want %v", got, want)
	}
}

// TestImportanceForLang_EmptyCodeDefaultsToEnglish covers the common
// zero-value path where the caller didn't populate StoreOptions.Language.
func TestImportanceForLang_EmptyCodeDefaultsToEnglish(t *testing.T) {
	got := ImportanceForLang("we decided to ship", nil, "")
	want := 0.5
	if !near(got, want) {
		t.Errorf("ImportanceForLang('')=%v, want %v", got, want)
	}
}
