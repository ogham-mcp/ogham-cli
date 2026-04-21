package extraction

import (
	"errors"
	"reflect"
	"testing"
)

// Parity + sanity for the embedded language YAMLs. Goals:
//   1. Every language file parses cleanly -- a malformed YAML anywhere
//      in the embed set must fail loudly at first LoadLanguage() call.
//   2. English has every top-level section populated (our reference
//      language; Python parity harness keys off this).
//   3. Other languages have the "big four" core sections
//      (decision_words, error_words, architecture_words, stop-equiv)
//      so multi-language Importance() never silently degrades.
//   4. Set()/SetLower() helpers round-trip correctly -- empty slice
//      -> empty set, dup -> one entry, case folding works.
//
// Uses reflection over LanguageRules so adding a YAML key (and struct
// field) doesn't silently skip a parity assertion: if the field is
// missing here, the reflective loop catches it.

func TestLanguages_AllFilesParseCleanly(t *testing.T) {
	codes := ListLanguages()
	if len(codes) < 10 {
		t.Fatalf("expected at least 10 languages embedded, got %d (%v)", len(codes), codes)
	}
	for _, code := range codes {
		rules, err := LoadLanguage(code)
		if err != nil {
			t.Errorf("%s: load failed: %v", code, err)
			continue
		}
		if rules == nil {
			t.Errorf("%s: loaded nil rules", code)
		}
	}
}

func TestLanguages_EnglishHasEveryKey(t *testing.T) {
	// English is the reference. Every struct field must have a
	// non-zero value after parsing en.yaml -- if a field is zero,
	// either the YAML section is missing or the yaml tag has drifted.
	en, err := LoadLanguage("en")
	if err != nil {
		t.Fatalf("load en: %v", err)
	}
	v := reflect.ValueOf(*en)
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := tp.Field(i)
		val := v.Field(i)
		// Maps and slices register as zero-length on the nil side; we
		// want BOTH nil AND empty to count as failure, since an empty
		// section means nothing would match.
		switch val.Kind() {
		case reflect.Map, reflect.Slice:
			if val.Len() == 0 {
				t.Errorf("en.yaml field %q (tag %q) is empty -- section missing or mis-tagged",
					f.Name, f.Tag.Get("yaml"))
			}
		}
	}
}

func TestLanguages_CoreSectionsPopulatedInEveryLanguage(t *testing.T) {
	// Not every language has every section (e.g. some skip compression_
	// decision_words, which is an English-only concept in some corpora).
	// But the "big four" scoring signals MUST be present everywhere, or
	// Importance() silently drops to base 0.2 for that language.
	required := []string{"DecisionWords", "ErrorWords", "ArchitectureWords", "TemporalKeywords"}
	for _, code := range ListLanguages() {
		rules, err := LoadLanguage(code)
		if err != nil {
			t.Fatalf("load %s: %v", code, err)
		}
		v := reflect.ValueOf(*rules)
		for _, name := range required {
			field := v.FieldByName(name)
			if !field.IsValid() {
				t.Fatalf("struct field %q not found (rename in languages.go?)", name)
			}
			if field.Len() == 0 {
				t.Errorf("%s.yaml: core section %q is empty", code, name)
			}
		}
	}
}

func TestLoadLanguage_NotFound(t *testing.T) {
	_, err := LoadLanguage("klingon")
	if err == nil {
		t.Fatal("want error for unknown language")
	}
	if !errors.Is(err, ErrLanguageNotFound) {
		t.Errorf("want errors.Is(err, ErrLanguageNotFound); got %v", err)
	}
}

func TestLoadLanguage_EmptyCodeFallsBackToEnglish(t *testing.T) {
	// Empty / whitespace should route to English rather than error.
	// Callers pass cfg.Language which may be unset; we want sensible
	// defaults, not a crash.
	got, err := LoadLanguage("   ")
	if err != nil {
		t.Fatalf("empty code: %v", err)
	}
	ref, _ := LoadLanguage("en")
	if got != ref {
		t.Error("empty code should return the English registry entry")
	}
}

func TestLoadLanguage_CaseInsensitive(t *testing.T) {
	ref, _ := LoadLanguage("en")
	got, err := LoadLanguage("EN")
	if err != nil {
		t.Fatalf("uppercase EN: %v", err)
	}
	if got != ref {
		t.Error("uppercase code should resolve to the same entry as lowercase")
	}
}

func TestSet_DeduplicatesAndHandlesNil(t *testing.T) {
	got := Set([]string{"a", "b", "a"})
	if len(got) != 2 {
		t.Errorf("Set dedup: len=%d, want 2 (%+v)", len(got), got)
	}
	// nil slice must yield an empty (but non-nil) set so callers can
	// always do `_, ok := set[word]` without a nil guard.
	empty := Set(nil)
	if empty == nil || len(empty) != 0 {
		t.Errorf("Set(nil) = %v, want empty non-nil map", empty)
	}
}

func TestSetLower_FoldsCase(t *testing.T) {
	got := SetLower([]string{"Hello", "WORLD", "hello"})
	if len(got) != 2 {
		t.Errorf("SetLower: len=%d, want 2 (%+v)", len(got), got)
	}
	if _, ok := got["hello"]; !ok {
		t.Error("SetLower should contain 'hello'")
	}
	if _, ok := got["Hello"]; ok {
		t.Error("SetLower should NOT contain case-preserved 'Hello'")
	}
}

// TestLanguages_SampleValuesMatchPython spot-checks a few known-good
// words from en.yaml to catch yaml tag drift. If someone renames the
// struct field or the YAML key without updating the other side, these
// pins fail immediately instead of silently producing an empty match.
func TestLanguages_SampleValuesMatchPython(t *testing.T) {
	en, err := LoadLanguage("en")
	if err != nil {
		t.Fatalf("load en: %v", err)
	}
	// day_names: Python's convention is Monday=1, Tuesday=2, ...
	if en.DayNames["monday"] != 1 {
		t.Errorf("DayNames[monday] = %d, want 1", en.DayNames["monday"])
	}
	// decision_words: "decided" is one of the reliable English signals.
	hasDecided := false
	for _, w := range en.DecisionWords {
		if w == "decided" {
			hasDecided = true
			break
		}
	}
	if !hasDecided {
		t.Error("en.DecisionWords missing 'decided' -- parity broken vs Python")
	}
	// word_numbers: "three" = 3.
	if en.WordNumbers["three"] != 3 {
		t.Errorf("WordNumbers[three] = %d, want 3", en.WordNumbers["three"])
	}
	// direction_words.after: "following" is in the after bucket.
	hasFollowing := false
	for _, w := range en.DirectionWords["after"] {
		if w == "following" {
			hasFollowing = true
			break
		}
	}
	if !hasFollowing {
		t.Error("en.DirectionWords[after] missing 'following' -- yaml tag or nested key drift")
	}
}
