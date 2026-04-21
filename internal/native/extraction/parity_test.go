package extraction

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Day 5 Python parity harness. The committed fixture at
// testdata/parity/parity.json is produced by the sibling Python script
// gen_parity_fixture.py (lives in the same dir) and pins the expected
// outputs of Python's extract_entities / extract_dates /
// compute_importance for a 100-memory corpus.
//
// This test asserts the Go side's Entities / DatesAt / Importance
// agree with Python within locked tolerances. Today's purpose: ship
// v0.5 with confidence that flipping --native-store-preview to the
// default in Day 6 will not introduce silent behaviour drift.
//
// Baselines (locked 2026-04-21 against the 97-record corpus):
//
//   entities (shared subset)  exact-match rate >= 75%
//   dates                     exact-match rate >= 70%  (relative dates
//                                                      slip between
//                                                      parsedatetime
//                                                      and Go's
//                                                      time-based
//                                                      resolver; some
//                                                      drift accepted)
//   importance                within 0.05 of Python    >= 85%
//
// Tighten these when we improve Go extraction to match Python more
// closely (v0.6 scope: event:/activity:/emotion:/relationship:/
// quantity:/preference: prefixes).

type parityRecord struct {
	Index                int      `json:"index"`
	Content              string   `json:"content"`
	Tags                 []string `json:"tags"`
	PythonEntitiesFull   []string `json:"python_entities_full"`
	PythonEntitiesShared []string `json:"python_entities_shared"`
	PythonDates          []string `json:"python_dates"`
	PythonImportance     float64  `json:"python_importance"`
}

type parityFixture struct {
	ReferenceDate        string         `json:"reference_date"`
	SharedEntityPrefixes []string       `json:"shared_entity_prefixes"`
	Count                int            `json:"count"`
	Records              []parityRecord `json:"records"`
}

func loadParityFixture(t *testing.T) *parityFixture {
	t.Helper()
	path := filepath.Join("testdata", "parity", "parity.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v -- run gen_parity_fixture.py first", path, err)
	}
	var fx parityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fx.Count != len(fx.Records) {
		t.Fatalf("fixture header count=%d disagrees with record count=%d", fx.Count, len(fx.Records))
	}
	return &fx
}

func parityReferenceDate(t *testing.T, fx *parityFixture) time.Time {
	t.Helper()
	// Try strict first; fall back to a lenient parse. The generator
	// writes "2026-04-21T12:00:00Z" so strict is sufficient.
	candidates := []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05"}
	for _, layout := range candidates {
		if t0, err := time.Parse(layout, fx.ReferenceDate); err == nil {
			return t0
		}
	}
	t.Fatalf("cannot parse reference_date %q", fx.ReferenceDate)
	return time.Time{}
}

// filterShared returns the subset of tags with one of the v0.5 shared
// prefixes. Mirrors gen_parity_fixture.py's shared_entities() filter
// so the Go output is compared on the same axis.
func filterShared(tags []string, prefixes []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		for _, p := range prefixes {
			if strings.HasPrefix(tag, p) {
				out = append(out, tag)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// setDiff returns (common, goOnly, pythonOnly). Inputs must be sorted.
func setDiff(goTags, pyTags []string) (common, goOnly, pyOnly []string) {
	goSet := make(map[string]struct{}, len(goTags))
	for _, t := range goTags {
		goSet[t] = struct{}{}
	}
	pySet := make(map[string]struct{}, len(pyTags))
	for _, t := range pyTags {
		pySet[t] = struct{}{}
	}
	for _, t := range goTags {
		if _, ok := pySet[t]; ok {
			common = append(common, t)
		} else {
			goOnly = append(goOnly, t)
		}
	}
	for _, t := range pyTags {
		if _, ok := goSet[t]; !ok {
			pyOnly = append(pyOnly, t)
		}
	}
	return common, goOnly, pyOnly
}

func TestParity_Entities_Shared(t *testing.T) {
	fx := loadParityFixture(t)
	prefixes := fx.SharedEntityPrefixes

	var exactMatches, total int
	var goOnlyCounts, pyOnlyCounts int
	for _, rec := range fx.Records {
		goOutput := filterShared(Entities(rec.Content), prefixes)
		want := rec.PythonEntitiesShared

		_, goOnly, pyOnly := setDiff(goOutput, want)
		total++
		if len(goOnly) == 0 && len(pyOnly) == 0 {
			exactMatches++
			continue
		}
		goOnlyCounts += len(goOnly)
		pyOnlyCounts += len(pyOnly)
		t.Logf("record %d diff: go-only=%v py-only=%v\n  content: %s",
			rec.Index, goOnly, pyOnly, truncate(rec.Content, 70))
	}
	rate := float64(exactMatches) / float64(total)
	t.Logf("entities shared-subset exact-match rate: %.1f%% (%d/%d) -- go-only tags: %d, python-only tags: %d",
		rate*100, exactMatches, total, goOnlyCounts, pyOnlyCounts)
	if rate < 0.75 {
		t.Errorf("entity parity rate %.1f%% below locked 75%% baseline", rate*100)
	}
}

func TestParity_Dates(t *testing.T) {
	fx := loadParityFixture(t)
	ref := parityReferenceDate(t, fx)

	var exactMatches, total int
	for _, rec := range fx.Records {
		goOutput := DatesAt(rec.Content, ref)
		sort.Strings(goOutput)

		want := make([]string, len(rec.PythonDates))
		copy(want, rec.PythonDates)
		sort.Strings(want)

		_, goOnly, pyOnly := setDiff(goOutput, want)
		total++
		if len(goOnly) == 0 && len(pyOnly) == 0 {
			exactMatches++
			continue
		}
		t.Logf("record %d date diff: go-only=%v py-only=%v\n  content: %s",
			rec.Index, goOnly, pyOnly, truncate(rec.Content, 70))
	}
	rate := float64(exactMatches) / float64(total)
	t.Logf("dates exact-match rate: %.1f%% (%d/%d)",
		rate*100, exactMatches, total)
	if rate < 0.70 {
		t.Errorf("date parity rate %.1f%% below locked 70%% baseline", rate*100)
	}
}

func TestParity_Importance(t *testing.T) {
	fx := loadParityFixture(t)

	const tolerance = 0.05
	var withinTolerance, total int
	var maxDelta float64
	for _, rec := range fx.Records {
		goScore := Importance(rec.Content, rec.Tags)
		delta := math.Abs(goScore - rec.PythonImportance)
		total++
		if delta <= tolerance {
			withinTolerance++
		} else {
			t.Logf("record %d importance diff: go=%.3f py=%.3f delta=%.3f\n  content: %s",
				rec.Index, goScore, rec.PythonImportance, delta,
				truncate(rec.Content, 70))
		}
		if delta > maxDelta {
			maxDelta = delta
		}
	}
	rate := float64(withinTolerance) / float64(total)
	t.Logf("importance within-%.2f rate: %.1f%% (%d/%d), max delta: %.3f",
		tolerance, rate*100, withinTolerance, total, maxDelta)
	if rate < 0.85 {
		t.Errorf("importance parity rate %.1f%% below locked 85%% baseline", rate*100)
	}
}

// truncate shortens content for log output so we don't dump multi-KB
// blobs into test traces on diff.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
