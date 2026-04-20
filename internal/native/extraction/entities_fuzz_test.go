package extraction

import (
	"regexp"
	"sort"
	"testing"
	"time"
)

// FuzzEntities asserts universal invariants that must hold for any input:
//
//   - Entities() must never panic
//   - output length always <= MaxEntities (never exceeds the cap)
//   - output is always sorted ascending
//   - output never contains duplicates (set-backed)
//
// Seed corpus is drawn from the PICT matrix so the fuzzer starts from
// known-realistic inputs and mutates outward. Run locally with:
//
//	go test -fuzz=FuzzEntities -fuzztime=60s ./internal/native/extraction/
//
// CI runs the seed corpus only (no mutation) via normal `go test`.
func FuzzEntities(f *testing.F) {
	// Seed: every row of the committed PICT matrix, rendered by the same
	// fixture factory the pairwise test uses.
	for _, row := range readPICTMatrix(f, "testdata/entities.pict.tsv") {
		f.Add(buildPICTFixture(row).Content)
	}
	// Adversarial seeds not easily reachable from the PICT axes: empty,
	// whitespace only, and a very long repetitive input to stress both
	// the 20-cap and the per-category file path cap.
	f.Add("")
	f.Add("   \t\n   ")
	f.Add(veryLongSeed())

	f.Fuzz(func(t *testing.T, content string) {
		got := Entities(content)

		if len(got) > MaxEntities {
			t.Fatalf("output length %d > MaxEntities=%d for input len=%d",
				len(got), MaxEntities, len(content))
		}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("output not sorted: %v", got)
		}
		if hasDuplicates(got) {
			t.Fatalf("output has duplicates: %v", got)
		}
	})
}

func veryLongSeed() string {
	const unit = "PaymentGateway RuntimeException ./pkg/a.go Kevin Burns "
	out := make([]byte, 0, len(unit)*100)
	for i := 0; i < 100; i++ {
		out = append(out, unit...)
	}
	return string(out)
}

// FuzzDates asserts universal invariants for Dates() / DatesAt():
//
//   - never panics on arbitrary input
//   - output is always sorted ascending
//   - output never contains duplicates
//   - every token matches YYYY-MM-DD exactly
//
// Seed corpus is drawn from the dates PICT matrix plus a few
// adversarial cases (empty, malformed relative phrases, impossible
// calendar dates). Run locally with:
//
//	go test -fuzz=FuzzDates -fuzztime=60s ./internal/native/extraction/
func FuzzDates(f *testing.F) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	for _, row := range readPICTMatrix(f, "testdata/dates.pict.tsv") {
		f.Add(buildDatesFixture(row, ref).Content)
	}
	// Adversarial seeds: the date parser is regex-driven, so impossible
	// calendar combinations + malformed quantifiers are the shapes most
	// likely to trip a panic.
	f.Add("")
	f.Add("Feb 30, 2026 is not a date.")
	f.Add("in 99999999999999999999 days")
	f.Add("last monday last tuesday last wednesday")
	f.Add("2026-13-45 is not valid ISO either.")

	dateFormatRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

	f.Fuzz(func(t *testing.T, content string) {
		got := DatesAt(content, ref)
		if !sort.StringsAreSorted(got) {
			t.Fatalf("output not sorted: %v", got)
		}
		if hasDuplicates(got) {
			t.Fatalf("output has duplicates: %v", got)
		}
		for _, d := range got {
			if !dateFormatRe.MatchString(d) {
				t.Fatalf("non-ISO date in output: %q (full: %v)", d, got)
			}
		}
	})
}
