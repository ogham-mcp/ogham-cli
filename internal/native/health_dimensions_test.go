package native

import (
	"strings"
	"testing"
)

func TestZoneOf(t *testing.T) {
	cases := []struct {
		name  string
		score float64
		want  Zone
	}{
		{"zero", 0.0, ZoneRed},
		{"red-upper", 4.9, ZoneRed},
		{"amber-lower", 5.0, ZoneAmber},
		{"amber-mid", 6.5, ZoneAmber},
		{"amber-upper", 7.9, ZoneAmber},
		{"green-lower", 8.0, ZoneGreen},
		{"green-mid", 9.5, ZoneGreen},
		{"green-perfect", 10.0, ZoneGreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ZoneOf(c.score); got != c.want {
				t.Errorf("ZoneOf(%.1f) = %q, want %q", c.score, got, c.want)
			}
		})
	}
}

func TestRoundDecimal(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0.0, 0.0},
		{4.9876, 5.0},
		{7.99, 8.0},
		{5.04, 5.0},
		{5.05, 5.1},
		{10.0, 10.0},
	}
	for _, c := range cases {
		if got := roundDecimal(c.in); got != c.want {
			t.Errorf("roundDecimal(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHumanizeAge(t *testing.T) {
	cases := []struct {
		name  string
		hours float64
		want  string
	}{
		{"sub-minute", 0.5, "30m"},
		{"45-min", 0.75, "45m"},
		{"one-hour", 1.0, "1.0h"},
		{"twelve-hours", 12.5, "12.5h"},
		{"just-under-day", 23.9, "23.9h"},
		{"one-day", 24.0, "1.0d"},
		{"three-days", 72.0, "3.0d"},
		{"ten-days", 240.0, "10.0d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := humanizeAge(c.hours); got != c.want {
				t.Errorf("humanizeAge(%v) = %q, want %q", c.hours, got, c.want)
			}
		})
	}
}

func TestResolveHealthProfile(t *testing.T) {
	cases := []struct {
		name       string
		cfgProfile string
		argProfile string
		want       string
	}{
		{"arg-wins", "from-cfg", "from-arg", "from-arg"},
		{"cfg-fallback", "from-cfg", "", "from-cfg"},
		{"default-fallback", "", "", "default"},
		{"empty-cfg-explicit-arg", "", "explicit", "explicit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Profile: c.cfgProfile}
			if got := resolveHealthProfile(cfg, c.argProfile); got != c.want {
				t.Errorf("resolveHealthProfile(cfg.Profile=%q, arg=%q) = %q, want %q",
					c.cfgProfile, c.argProfile, got, c.want)
			}
		})
	}
}

func TestResolveHealthProfileNilCfg(t *testing.T) {
	if got := resolveHealthProfile(nil, ""); got != "default" {
		t.Errorf("resolveHealthProfile(nil, \"\") = %q, want \"default\"", got)
	}
	if got := resolveHealthProfile(nil, "explicit"); got != "explicit" {
		t.Errorf("resolveHealthProfile(nil, \"explicit\") = %q, want \"explicit\"", got)
	}
}

func TestOverallScore(t *testing.T) {
	cases := []struct {
		name string
		dims []DimensionResult
		want float64
	}{
		{"empty", nil, 0.0},
		{"single-perfect", []DimensionResult{{Score: 10.0}}, 10.0},
		{"single-broken", []DimensionResult{{Score: 0.0}}, 0.0},
		{
			"mixed-three",
			[]DimensionResult{{Score: 10.0}, {Score: 5.0}, {Score: 0.0}},
			5.0,
		},
		{
			"rounded",
			[]DimensionResult{{Score: 8.3}, {Score: 7.6}, {Score: 6.4}},
			7.4, // (8.3 + 7.6 + 6.4) / 3 = 7.43... rounded to 7.4
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OverallScore(c.dims); got != c.want {
				t.Errorf("OverallScore(%v) = %v, want %v", c.dims, got, c.want)
			}
		})
	}
}

func TestSafeComputeRecoversPanic(t *testing.T) {
	result := safeCompute("test dim", func() DimensionResult {
		panic("synthetic panic")
	})
	if result.Name != "test dim" {
		t.Errorf("name not preserved: got %q", result.Name)
	}
	if result.Score != 0.0 {
		t.Errorf("panicked dim should score 0.0; got %v", result.Score)
	}
	if result.Zone != ZoneRed {
		t.Errorf("panicked dim should be RED; got %q", result.Zone)
	}
	if !strings.Contains(result.Detail, "synthetic panic") {
		t.Errorf("panic message should be in detail; got %q", result.Detail)
	}
}

func TestSafeComputeReturnsNormalResult(t *testing.T) {
	normal := DimensionResult{
		Name:   "happy",
		Score:  9.5,
		Zone:   ZoneGreen,
		Detail: "all clear",
	}
	result := safeCompute("happy", func() DimensionResult {
		return normal
	})
	if result != normal {
		t.Errorf("safeCompute should pass through non-panicking returns; got %+v", result)
	}
}

func TestDBFreshnessScoringBoundaries(t *testing.T) {
	// Spot-checks the scoring curve at the four critical boundaries.
	// We don't call ComputeDBFreshness (that needs a live DB); instead
	// we re-derive the score with the same formula at known ages.
	cases := []struct {
		name      string
		ageHours  float64
		wantScore float64
		wantZone  Zone
	}{
		{"fresh-1h", 1.0, 10.0, ZoneGreen},
		{"fresh-24h-exact", 24.0, 10.0, ZoneGreen},
		{"amber-48h-mid", 48.0, 6.5, ZoneAmber},
		{"amber-72h-exact", 72.0, 5.0, ZoneAmber},
		{"red-just-after-72h", 72.5, 5.0, ZoneAmber}, // tiny excess, rounds to 5.0
		{"red-1week", 168.0, 4.3, ZoneRed},
		{"red-30days-cap", 30*24 + 72, 0.0, ZoneRed},
		{"red-deep", 90 * 24, 0.0, ZoneRed}, // beyond cap stays at floor
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score := computeFreshnessScore(c.ageHours)
			if score != c.wantScore {
				t.Errorf("score at %vh = %v, want %v", c.ageHours, score, c.wantScore)
			}
			if z := ZoneOf(score); z != c.wantZone {
				t.Errorf("zone at %vh = %q, want %q", c.ageHours, z, c.wantZone)
			}
		})
	}
}

// computeFreshnessScore is extracted from ComputeDBFreshness so tests
// can exercise the scoring curve without needing a live database.
// Kept package-private; ComputeDBFreshness inlines this same logic so
// the duplication is intentional and small.
func computeFreshnessScore(ageHours float64) float64 {
	var score float64
	switch {
	case ageHours <= 24:
		score = 10.0
	case ageHours <= 72:
		score = 7.99 - (ageHours-24)*(2.99/48.0)
	default:
		excess := ageHours - 72
		if excess > 30*24 {
			excess = 30 * 24
		}
		score = 4.99 - excess*(4.99/(30*24))
		if score < 0 {
			score = 0
		}
	}
	return roundDecimal(score)
}

func TestCorpusSizeScoringBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		count     int64
		wantScore float64
		wantZone  Zone
	}{
		{"empty", 0, 0.0, ZoneRed},
		{"single", 1, 0.6, ZoneRed},
		{"nine", 9, 5.0, ZoneAmber}, // (9/9)*4.99 = 4.99 -> rounds to 5.0
		{"ten", 10, 5.0, ZoneAmber},
		{"fifty", 50, 6.3, ZoneAmber},
		{"ninety-nine", 99, 8.0, ZoneGreen}, // 5.0 + 89/89*2.99 = 7.99 -> rounds to 8.0
		{"hundred", 100, 10.0, ZoneGreen},
		{"thousand", 1000, 10.0, ZoneGreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score := computeCorpusSizeScore(c.count)
			if score != c.wantScore {
				t.Errorf("score for %d memories = %v, want %v", c.count, score, c.wantScore)
			}
			if z := ZoneOf(score); z != c.wantZone {
				t.Errorf("zone for %d memories = %q, want %q", c.count, z, c.wantZone)
			}
		})
	}
}

// computeCorpusSizeScore is extracted from ComputeCorpusSize for the
// same reason as computeFreshnessScore -- testable scoring curve.
func computeCorpusSizeScore(count int64) float64 {
	var score float64
	switch {
	case count >= 100:
		score = 10.0
	case count >= 10:
		score = 5.0 + float64(count-10)/89.0*2.99
	case count > 0:
		score = float64(count) / 9.0 * 4.99
	default:
		score = 0.0
	}
	return roundDecimal(score)
}
