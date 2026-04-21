package extraction

import (
	"reflect"
	"testing"
	"time"
)

// TestDatesForLang_German verifies the German date pack parses the
// anchors ("heute", "morgen", "gestern"), modifier+weekday phrases
// ("nächsten Montag"), month names ("15. März 2026"), and "vor N
// Wochen" past offsets. The pack is YAML-driven so this test guards
// against silent YAML drift (e.g. a missing modifier entry would
// regress this output without visibly failing a parity test because
// the parity corpus doesn't exercise every axis).
func TestDatesForLang_German(t *testing.T) {
	// Wed 2026-04-15, same ref the English tests use so the relative
	// arithmetic lines up.
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name, content string
		want          []string
	}{
		{
			name:    "heute anchor",
			content: "Wir liefern heute.",
			want:    []string{iso(2026, time.April, 15)},
		},
		{
			name:    "morgen anchor resolves to next day",
			content: "Wir machen das morgen.",
			want:    []string{iso(2026, time.April, 16)},
		},
		{
			name:    "gestern anchor resolves to previous day",
			content: "Das war gestern erledigt.",
			want:    []string{iso(2026, time.April, 14)},
		},
		{
			name:    "nächsten Montag",
			content: "Wir treffen uns nächsten Montag.",
			want:    []string{iso(2026, time.April, 20)}, // Mon after Wed 15
		},
		{
			name:    "letzte Woche",
			content: "Das wurde letzte Woche entschieden.",
			want:    []string{iso(2026, time.April, 8)},
		},
		{
			name:    "vor 2 Wochen",
			content: "Wir haben das vor 2 Wochen besprochen.",
			want:    []string{iso(2026, time.April, 1)},
		},
		{
			name:    "in 3 Tagen",
			content: "Der Release erfolgt in 3 Tagen.",
			want:    []string{iso(2026, time.April, 18)},
		},
		{
			name:    "natural date 15. März 2026",
			content: "Die Konferenz war am 15. März 2026.",
			want:    []string{iso(2026, time.March, 15)},
		},
		{
			name:    "natural date 15 Maerz 2026 (ascii variant)",
			content: "Konferenz am 15 Maerz 2026.",
			want:    []string{iso(2026, time.March, 15)},
		},
		{
			name:    "absolute ISO still normalised under DE",
			content: "Geliefert am 2026/03/15.",
			want:    []string{iso(2026, time.March, 15)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, "de")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DatesAtForLang(de) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDatesForLang_UnknownFallsBackToEnglish makes sure an unknown
// language code resolves the English anchors rather than returning
// empty. "klingon" is never in the registry.
func TestDatesForLang_UnknownFallsBackToEnglish(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := DatesAtForLang("yesterday", ref, "klingon")
	want := []string{iso(2026, time.April, 14)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatesAtForLang(klingon) = %v, want %v", got, want)
	}
}

// TestDatesForLang_EmptyCodeDefaultsToEnglish asserts the ""->"en" path
// in datePackFor -- a common caller shape when StoreOptions.Language
// isn't set.
func TestDatesForLang_EmptyCodeDefaultsToEnglish(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := DatesAtForLang("tomorrow", ref, "")
	want := []string{iso(2026, time.April, 16)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatesAtForLang('') = %v, want %v", got, want)
	}
}
