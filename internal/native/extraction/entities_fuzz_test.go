package extraction

import (
	"sort"
	"testing"
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
