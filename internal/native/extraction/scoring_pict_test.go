package extraction

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// TestImportance_PICT consumes testdata/scoring.pict.tsv, synthesises
// content per axis combination, and asserts the computed score matches
// the Python formula exactly. Each axis toggles a known delta, so the
// expected score is fully deterministic from the row.
//
// Language axis: EN exercises the English signal-word path, DE exercises
// the YAML-loaded German vocab, Unknown ("klingon") exercises the silent
// English fallback. Expected score is identical across languages because
// every language's word sets fire the same +0.3/+0.2/+0.2 axes when their
// respective signal phrase is present.
func TestImportance_PICT(t *testing.T) {
	rows := readPICTMatrix(t, "testdata/scoring.pict.tsv")
	if len(rows) == 0 {
		t.Fatal("no rows in PICT matrix; regenerate via make pict-regen")
	}

	for i, row := range rows {
		row := row
		name := fmt.Sprintf("row_%02d_%s_%s_%s_%s_%s_%s_%s_%s", i,
			row["DecisionSignal"], row["ErrorSignal"], row["ArchSignal"],
			row["FilePathSignal"], row["CodeFenceSignal"],
			row["LengthBucket"], row["TagCountBucket"],
			row["Language"])
		t.Run(name, func(t *testing.T) {
			content, tags := buildScoringFixture(row)
			lang := languageCodeFromRow(row)
			got := ImportanceForLang(content, tags, lang)
			want := expectedScore(row)

			if math.Abs(got-want) > 1e-9 {
				t.Errorf("Importance = %.3f, want %.3f (content=%q tags=%v lang=%q)",
					got, want, content, tags, lang)
			}
			if got < 0 || got > 1.0 {
				t.Errorf("score %.3f out of [0,1] range", got)
			}
		})
	}
}

// languageCodeFromRow maps the PICT Language axis value to the code
// the extraction loader expects. Unknown -> a deliberately invalid
// code so the resolveRules fallback path exercises itself.
func languageCodeFromRow(row map[string]string) string {
	switch row["Language"] {
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

// buildScoringFixture synthesises content + tags from a PICT row.
// Intentionally deterministic: same row -> same content. Signal phrases
// are chosen per-language so the DE axis actually exercises the YAML
// vocab and isn't just a pass-through over English-shaped content.
func buildScoringFixture(row map[string]string) (string, []string) {
	lang := row["Language"]
	if lang == "" {
		lang = "EN"
	}
	// Unknown falls back to English under the hood, so use English
	// signal phrases for the Unknown axis.
	if lang == "Unknown" {
		lang = "EN"
	}

	var parts []string
	if row["DecisionSignal"] == "Present" {
		switch lang {
		case "DE":
			parts = append(parts, "Wir haben uns entschieden, früh auszuliefern.")
		default:
			parts = append(parts, "we decided to ship early.")
		}
	} else {
		switch lang {
		case "DE":
			parts = append(parts, "die Diskussion geht weiter.")
		default:
			parts = append(parts, "the discussion continues.")
		}
	}
	if row["ErrorSignal"] == "Present" {
		// RuntimeException matches errorTypeRe in every language (regex
		// is language-agnostic). Use it as the universal trigger.
		parts = append(parts, "a RuntimeException surfaced.")
	}
	if row["ArchSignal"] == "Present" {
		switch lang {
		case "DE":
			parts = append(parts, "Refaktorisierung des modularen Clients.")
		default:
			parts = append(parts, "decoupled the modular interface.")
		}
	}
	if row["FilePathSignal"] == "Present" {
		parts = append(parts, "edit ./cmd/root.go please.")
	}
	if row["CodeFenceSignal"] == "Present" {
		parts = append(parts, "`ogham serve` launches the sidecar.")
	}

	content := strings.Join(parts, " ")

	// LengthBucket Long requires > 500 chars. Pad with filler that
	// contains no signal words so padding never inadvertently triggers
	// another axis. "and then and then and then..." is safe -- none
	// of those words appear in any decision/error/arch set for either
	// English or German.
	if row["LengthBucket"] == "Long" {
		for len(content) <= 500 {
			content += " and then and then and then"
		}
	}

	var tags []string
	if row["TagCountBucket"] == "Many" {
		tags = []string{"type:decision", "project:ogham", "v0.5"}
	} else {
		// Few = 0..2; pick 1 tag to exercise the non-zero-but-below-3 case.
		tags = []string{"type:decision"}
	}
	return content, tags
}

// expectedScore reproduces the Python formula bit-for-bit from axis
// values, so PICT coverage doubles as a formula regression test. The
// Language axis does not change the expected score -- every language
// adds the same deltas on signal hit.
func expectedScore(row map[string]string) float64 {
	score := 0.2
	if row["DecisionSignal"] == "Present" {
		score += 0.3
	}
	if row["ErrorSignal"] == "Present" {
		score += 0.2
	}
	if row["ArchSignal"] == "Present" {
		score += 0.2
	}
	if row["FilePathSignal"] == "Present" {
		score += 0.1
	}
	if row["CodeFenceSignal"] == "Present" {
		score += 0.1
	}
	if row["LengthBucket"] == "Long" {
		score += 0.1
	}
	if row["TagCountBucket"] == "Many" {
		score += 0.1
	}
	if score > 1.0 {
		score = 1.0
	}
	return score
}
