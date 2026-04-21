package extraction

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestRecurrence_Hand covers the most important shapes by hand so a
// regression is caught without decoding the PICT matrix.
func TestRecurrence_Hand(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		lang        string
		wantPattern string
		wantTagSub  []string // subset that must appear
		wantOK      bool
	}{
		{
			name:        "english daily explicit",
			content:     "We run the backup daily at midnight.",
			lang:        "en",
			wantPattern: "daily",
			wantTagSub:  []string{"recurrence:daily"},
			wantOK:      true,
		},
		{
			name:        "english weekly explicit",
			content:     "Weekly sync on Mondays.",
			lang:        "en",
			wantPattern: "weekly",
			wantTagSub:  []string{"recurrence:weekly"},
			wantOK:      true,
		},
		{
			name:        "english biweekly via bi-weekly hyphen",
			content:     "Status goes out bi-weekly.",
			lang:        "en",
			wantPattern: "biweekly",
			wantTagSub:  []string{"recurrence:biweekly"},
			wantOK:      true,
		},
		{
			name:        "english fortnightly alias",
			content:     "Fortnightly retro.",
			lang:        "en",
			wantPattern: "biweekly",
			wantTagSub:  []string{"recurrence:biweekly"},
			wantOK:      true,
		},
		{
			name:        "english every monday",
			content:     "Standup every Monday at 9am.",
			lang:        "en",
			wantPattern: "weekly",
			wantTagSub:  []string{"recurrence:monday", "recurrence:weekly"},
			wantOK:      true,
		},
		{
			name:        "english every tuesday and thursday",
			content:     "Pairing every Tuesday and Thursday.",
			lang:        "en",
			wantPattern: "weekly",
			wantTagSub:  []string{"recurrence:tuesday", "recurrence:thursday", "recurrence:weekly"},
			wantOK:      true,
		},
		{
			name:        "german monthly explicit",
			content:     "Der Bericht erscheint monatlich.",
			lang:        "de",
			wantPattern: "monthly",
			wantTagSub:  []string{"recurrence:monthly"},
			wantOK:      true,
		},
		{
			name:        "german jeden Dienstag",
			content:     "Wir treffen uns jeden Dienstag.",
			lang:        "de",
			wantPattern: "weekly",
			wantTagSub:  []string{"recurrence:tuesday", "recurrence:weekly"},
			wantOK:      true,
		},
		{
			name:        "german adverbial montags without jeden",
			content:     "Montags gehen wir laufen.",
			lang:        "de",
			wantPattern: "weekly",
			wantTagSub:  []string{"recurrence:monday", "recurrence:weekly"},
			wantOK:      true,
		},
		{
			name:        "no recurrence in plain prose",
			content:     "We shipped the release yesterday.",
			lang:        "en",
			wantPattern: "",
			wantTagSub:  nil,
			wantOK:      false,
		},
		{
			name:        "unknown language falls back to English",
			content:     "Daily digest.",
			lang:        "klingon",
			wantPattern: "daily",
			wantTagSub:  []string{"recurrence:daily"},
			wantOK:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, tags, ok := Recurrence(tc.content, tc.lang)
			if ok != tc.wantOK {
				t.Fatalf("Recurrence ok=%v, want %v (content=%q)", ok, tc.wantOK, tc.content)
			}
			if p != tc.wantPattern {
				t.Errorf("pattern=%q, want %q", p, tc.wantPattern)
			}
			for _, want := range tc.wantTagSub {
				found := false
				for _, got := range tags {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("tag %q missing from %v", want, tags)
				}
			}
			if !sort.StringsAreSorted(tags) {
				t.Errorf("tags not sorted: %v", tags)
			}
		})
	}
}

// TestRecurrence_PICT consumes testdata/recurrence.pict.tsv. Each row
// synthesises a content string per (Language, PhraseType, TimeUnit)
// tuple and asserts the output shape. The axis cross-product is kept
// small by PICT itself (~25 rows) so running this sub-test is cheap.
func TestRecurrence_PICT(t *testing.T) {
	rows := readPICTMatrix(t, "testdata/recurrence.pict.tsv")
	if len(rows) == 0 {
		t.Fatal("no rows in PICT matrix; regenerate via make pict-regen")
	}

	for i, row := range rows {
		row := row
		name := testNameFromRecurrenceRow(i, row)
		t.Run(name, func(t *testing.T) {
			content, expectHit, expectPattern := buildRecurrenceFixture(row)
			lang := recurrenceLangCode(row["Language"])

			pattern, tags, ok := Recurrence(content, lang)
			if ok != expectHit {
				t.Fatalf("ok=%v, want %v (content=%q lang=%q pattern=%q tags=%v)",
					ok, expectHit, content, lang, pattern, tags)
			}
			if expectHit && pattern != expectPattern {
				t.Errorf("pattern=%q, want %q (content=%q lang=%q)",
					pattern, expectPattern, content, lang)
			}
			if expectHit && len(tags) == 0 {
				t.Errorf("hit with empty tags (content=%q)", content)
			}
			if !expectHit && (pattern != "" || len(tags) != 0) {
				t.Errorf("no-hit row returned pattern=%q tags=%v", pattern, tags)
			}
		})
	}
}

func testNameFromRecurrenceRow(i int, row map[string]string) string {
	return "row_" + itoaPad(i) + "_" + row["Language"] + "_" + row["PhraseType"] + "_" + row["TimeUnit"]
}

func itoaPad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	// cheap 2-digit representation for our ~25-row matrix
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func recurrenceLangCode(s string) string {
	switch s {
	case "EN":
		return "en"
	case "DE":
		return "de"
	case "Unknown":
		return "klingon"
	default:
		return "en"
	}
}

// buildRecurrenceFixture synthesises a deterministic content string
// per PICT row. Returns (content, expectHit, expectPattern). expectHit
// is false for PhraseType=None rows.
func buildRecurrenceFixture(row map[string]string) (string, bool, string) {
	lang := row["Language"]
	phrase := row["PhraseType"]
	unit := row["TimeUnit"]

	switch phrase {
	case "None":
		return "A plain sentence with no recurrence signal at all.", false, ""
	case "EveryDay":
		if lang == "DE" {
			return "Wir treffen uns jeden Dienstag.", true, "weekly"
		}
		return "Standup every Monday at 9am.", true, "weekly"
	case "Adverbial":
		// German-only axis (PICT constraint).
		return "Montags gehen wir laufen.", true, "weekly"
	case "Explicit":
		return explicitRecurrencePhrase(lang, unit), true, normalisedFromTimeUnit(unit)
	}
	return "", false, ""
}

func explicitRecurrencePhrase(lang, unit string) string {
	switch lang {
	case "DE":
		switch unit {
		case "Daily":
			return "Das Backup läuft täglich."
		case "Weekly":
			return "Der Bericht erscheint wöchentlich."
		case "Biweekly":
			return "Status erscheint zweiwöchentlich."
		case "Monthly":
			return "Zahlungen kommen monatlich."
		case "Quarterly":
			return "Wir reporten quartalsweise."
		case "Yearly":
			return "Der Review findet jährlich statt."
		}
	default:
		switch unit {
		case "Daily":
			return "We run the backup daily at midnight."
		case "Weekly":
			return "Weekly sync every Monday."
		case "Biweekly":
			return "Status goes out bi-weekly."
		case "Monthly":
			return "Invoices arrive monthly."
		case "Quarterly":
			return "We report quarterly."
		case "Yearly":
			return "Reviews happen yearly."
		}
	}
	return ""
}

func normalisedFromTimeUnit(unit string) string {
	switch unit {
	case "Daily":
		return "daily"
	case "Weekly":
		return "weekly"
	case "Biweekly":
		return "biweekly"
	case "Monthly":
		return "monthly"
	case "Quarterly":
		return "quarterly"
	case "Yearly":
		return "yearly"
	}
	return ""
}

// TestRecurrence_TagsFormat asserts the tag output is stable and
// strictly prefixed. Downstream FTS queries filter on the prefix so
// any drift here would silently break the dashboards.
func TestRecurrence_TagsFormat(t *testing.T) {
	_, tags, ok := Recurrence("Standup every Monday and Wednesday.", "en")
	if !ok {
		t.Fatal("expected hit")
	}
	wantTags := []string{
		"recurrence:monday",
		"recurrence:wednesday",
		"recurrence:weekly",
	}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Errorf("tags = %v, want %v", tags, wantTags)
	}
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "recurrence:") {
			t.Errorf("tag %q missing recurrence: prefix", tag)
		}
	}
}
