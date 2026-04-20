package extraction

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// pictRef is the fixed reference time used to resolve "yesterday" /
// "today" / "tomorrow" / "N days ago" etc. in PICT fixtures. Must be
// stable so the committed matrix's invariants never drift with the
// wall clock. Chosen as a Wednesday so "last Monday" / "next Friday"
// resolve deterministically and never cross a month/year boundary.
var pictRef = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

// isoDateRe asserts that every output token is ISO YYYY-MM-DD.
var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// TestDates_PICT consumes testdata/dates.pict.tsv and drives DatesAt()
// against a fixed reference time, asserting per-row invariants.
func TestDates_PICT(t *testing.T) {
	rows := readPICTMatrix(t, "testdata/dates.pict.tsv")
	if len(rows) == 0 {
		t.Fatal("no rows in PICT matrix; regenerate via make pict-regen")
	}

	for i, row := range rows {
		row := row
		name := fmt.Sprintf("row_%02d_%s_%s_%s", i,
			row["Category"], row["FormatVariant"], row["DateCount"])
		t.Run(name, func(t *testing.T) {
			fx := buildDatesFixture(row, pictRef)
			got := DatesAt(fx.Content, pictRef)

			if !sort.StringsAreSorted(got) {
				t.Errorf("output not sorted: %v", got)
			}
			if hasDuplicates(got) {
				t.Errorf("output contains duplicates: %v", got)
			}
			for _, d := range got {
				if !isoDateRe.MatchString(d) {
					t.Errorf("non-ISO date in output: %q (full: %v)", d, got)
				}
			}

			cat := row["Category"]
			count := row["DateCount"]

			if cat == "None" {
				if len(got) != 0 {
					t.Errorf("None category produced dates: %v", got)
				}
				return
			}

			switch count {
			case "One":
				if len(got) < 1 {
					t.Errorf("%s/One produced no dates: input=%q output=%v",
						cat, fx.Content, got)
				}
			case "Multiple":
				if len(got) < 2 {
					t.Errorf("%s/Multiple produced %d dates (want >=2): input=%q output=%v",
						cat, len(got), fx.Content, got)
				}
			}

			// Every expected date for the fixture must appear in output.
			for _, want := range fx.ExpectedDates {
				found := false
				for _, g := range got {
					if g == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected date %q missing from output %v (input=%q)",
						want, got, fx.Content)
				}
			}
		})
	}
}

type datesFixture struct {
	Content       string
	ExpectedDates []string // ISO strings that MUST appear in output
}

// buildDatesFixture synthesises content from a PICT row + a fixed
// reference time. Deterministic: same row -> same content + expected.
func buildDatesFixture(row map[string]string, ref time.Time) datesFixture {
	cat := row["Category"]
	fv := row["FormatVariant"]
	ord := row["OrdinalSuffix"]
	cs := row["CaseMix"]
	count := row["DateCount"]

	if cat == "None" {
		return datesFixture{Content: "prose with no dates here."}
	}

	var pieces []string
	var want []string

	switch cat {
	case "ISO":
		// Two reference absolute dates that never straddle months.
		d1 := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
		d2 := time.Date(2025, 12, 5, 0, 0, 0, 0, time.UTC)
		pieces = append(pieces, formatISO(d1, fv))
		want = append(want, d1.Format("2006-01-02"))
		if count == "Multiple" {
			pieces = append(pieces, formatISO(d2, fv))
			want = append(want, d2.Format("2006-01-02"))
		}

	case "Natural":
		d1 := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
		d2 := time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC)
		pieces = append(pieces, formatNatural(d1, fv, ord, cs))
		want = append(want, d1.Format("2006-01-02"))
		if count == "Multiple" {
			pieces = append(pieces, formatNatural(d2, fv, ord, cs))
			want = append(want, d2.Format("2006-01-02"))
		}

	case "Relative":
		// Pin relative phrases to deterministic output against `ref`.
		// ref = 2026-04-15 Wed.
		if fv == "V1" {
			// Anchored: yesterday, today (Multiple adds tomorrow).
			pieces = append(pieces, applyCase("yesterday", cs))
			want = append(want, ref.AddDate(0, 0, -1).Format("2006-01-02"))
			if count == "Multiple" {
				pieces = append(pieces, applyCase("tomorrow", cs))
				want = append(want, ref.AddDate(0, 0, 1).Format("2006-01-02"))
			}
		} else {
			// Quantified: "2 days ago", and for Multiple also "in 1 week".
			pieces = append(pieces, applyCase("2 days ago", cs))
			want = append(want, ref.AddDate(0, 0, -2).Format("2006-01-02"))
			if count == "Multiple" {
				pieces = append(pieces, applyCase("in 1 week", cs))
				want = append(want, ref.AddDate(0, 0, 7).Format("2006-01-02"))
			}
		}

	case "Mixed":
		// ISO + Natural together, exercises sort + dedup when same date.
		d1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		d2 := time.Date(2025, 9, 14, 0, 0, 0, 0, time.UTC)
		pieces = append(pieces, formatISO(d1, fv))
		want = append(want, d1.Format("2006-01-02"))
		if count == "Multiple" || count == "One" {
			pieces = append(pieces, formatNatural(d2, fv, ord, cs))
			want = append(want, d2.Format("2006-01-02"))
		}
	}

	// Wrap pieces in a short prose sentence so parsing has realistic
	// boundaries around the date tokens.
	content := "The entry from " + strings.Join(pieces, " and ") + " matters."
	return datesFixture{Content: content, ExpectedDates: want}
}

// formatISO renders t with dash separator for V1 or slash for V2.
// dates.go normalises slashes back to dashes in the output.
func formatISO(t time.Time, fv string) string {
	if fv == "V2" {
		return t.Format("2006/01/02")
	}
	return t.Format("2006-01-02")
}

// formatNatural renders t in "Month day, year" order (V1) or
// "day Month year" (V2) with optional ordinal + case variation.
func formatNatural(t time.Time, fv, ord, cs string) string {
	month := t.Format("January")
	day := fmt.Sprintf("%d", t.Day())
	year := fmt.Sprintf("%d", t.Year())
	// Ordinal suffixes are only accepted by the month-first regex
	// (Python parity). Day-first "14th September 2025" would fail to
	// match on both sides, so the fixture drops the ordinal for V2.
	if ord == "Present" && fv == "V1" {
		day += englishOrdinal(t.Day())
	}
	var out string
	if fv == "V1" {
		out = month + " " + day + ", " + year
	} else {
		out = day + " " + month + " " + year
	}
	return applyCase(out, cs)
}

func englishOrdinal(d int) string {
	if d%100 >= 11 && d%100 <= 13 {
		return "th"
	}
	switch d % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

func applyCase(s, cs string) string {
	switch cs {
	case "Lower":
		return strings.ToLower(s)
	case "Upper":
		return strings.ToUpper(s)
	default:
		return s // Title / default passes through
	}
}
