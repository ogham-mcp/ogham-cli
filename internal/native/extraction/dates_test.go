package extraction

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// Fixed reference for relative-phrase tests. Wed 2026-04-15.
var testRef = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

// iso is a shorthand for time.Date(...).Format("2006-01-02") to keep
// table entries readable.
func iso(y int, m time.Month, d int) string {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func TestDates_ISO(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"dash", "Shipped on 2026-04-20.", []string{"2026-04-20"}},
		{"slash normalised to dash", "See 2026/04/20 in the log.", []string{"2026-04-20"}},
		{"single-digit month + day padded", "On 2026-4-2 we shipped.", []string{"2026-04-02"}},
		{"two dates deduplicated", "2026-04-20 then 2026-04-20 again.", []string{"2026-04-20"}},
		{"two distinct dates sorted", "Due 2026-12-01 from 2025-11-03.", []string{"2025-11-03", "2026-12-01"}},
		{"no date in plain prose", "this sentence mentions no year at all.", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Errorf("unexpected dates: %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDates_Natural(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"month-first full", "April 20, 2026 was the day.", []string{"2026-04-20"}},
		{"month-first ordinal", "Due April 20th, 2026.", []string{"2026-04-20"}},
		{"month-first abbrev", "Shipped Apr 20 2026.", []string{"2026-04-20"}},
		{"day-first full", "The 20 April 2026 meeting.", []string{"2026-04-20"}},
		{"day-first abbrev", "By 20 Apr 2026 latest.", []string{"2026-04-20"}},
		{"case insensitive month", "meeting on APRIL 20, 2026 only.", []string{"2026-04-20"}},
		{"impossible date rejected", "Feb 30, 2026 not a real date.", nil},
		{"no comma allowed", "April 20 2026 is fine too.", []string{"2026-04-20"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Errorf("unexpected dates: %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDates_Relative_Anchored(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"yesterday I shipped it", iso(2026, 4, 14)},
		{"today is the day", iso(2026, 4, 15)},
		{"tomorrow we ship", iso(2026, 4, 16)},
		{"YESTERDAY at noon", iso(2026, 4, 14)}, // case-insensitive
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v want [%s]", got, tc.want)
			}
		})
	}
}

func TestDates_Relative_Quantified_Ago(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"3 days ago", iso(2026, 4, 12)},
		{"1 day ago", iso(2026, 4, 14)},
		{"2 weeks ago", iso(2026, 4, 1)},
		{"1 week ago", iso(2026, 4, 8)},
		{"1 month ago", iso(2026, 3, 15)},
		{"6 months ago", iso(2025, 10, 15)},
		{"1 year ago", iso(2025, 4, 15)},
		{"2 years ago", iso(2024, 4, 15)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v want [%s]", got, tc.want)
			}
		})
	}
}

func TestDates_Relative_Quantified_In(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"in 3 days we ship", iso(2026, 4, 18)},
		{"in 1 day i'll be back", iso(2026, 4, 16)},
		{"in 2 weeks", iso(2026, 4, 29)},
		{"in 1 week", iso(2026, 4, 22)},
		{"in 1 month", iso(2026, 5, 15)},
		{"in 3 months", iso(2026, 7, 15)},
		{"in 1 year", iso(2027, 4, 15)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v want [%s]", got, tc.want)
			}
		})
	}
}

func TestDates_Relative_Weekday(t *testing.T) {
	// testRef is Wed 2026-04-15.
	cases := []struct {
		in   string
		want string
	}{
		{"last monday i shipped", iso(2026, 4, 13)},
		{"last sunday was busy", iso(2026, 4, 12)},
		{"next friday we demo", iso(2026, 4, 17)},
		{"next wednesday", iso(2026, 4, 22)},
		{"this thursday", iso(2026, 4, 16)},
		{"this monday", iso(2026, 4, 13)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v want [%s]", got, tc.want)
			}
		})
	}
}

func TestDates_Relative_Period(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"last week", iso(2026, 4, 8)},
		{"next week", iso(2026, 4, 22)},
		{"this week", iso(2026, 4, 15)},
		{"last month", iso(2026, 3, 15)},
		{"next month", iso(2026, 5, 15)},
		{"this month", iso(2026, 4, 15)},
		{"last year", iso(2025, 4, 15)},
		{"next year", iso(2027, 4, 15)},
		{"this year", iso(2026, 4, 15)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := DatesAt(tc.in, testRef)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v want [%s]", got, tc.want)
			}
		})
	}
}

// Relative phrases only fire when there's no absolute date in the
// content -- Python parity.
func TestDates_RelativeSkippedWhenAbsolutePresent(t *testing.T) {
	// Content has an ISO date AND a relative phrase; only the ISO date
	// should be emitted.
	in := "On 2026-04-20 we met, yesterday we debugged."
	got := DatesAt(in, testRef)
	want := []string{"2026-04-20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// Dates() wraps DatesAt(..., time.Now()). Smoke-test by checking that
// an absolute date flows through regardless of wall clock.
func TestDates_ConveniencePoint(t *testing.T) {
	got := Dates("Shipped on 2026-04-20.")
	want := []string{"2026-04-20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// Mixed content exercises dedup when ISO and natural reference the
// same date, and sort when they don't.
func TestDates_Mixed(t *testing.T) {
	in := "2026-04-20 and April 20, 2026 and December 1, 2026"
	got := DatesAt(in, testRef)
	want := []string{"2026-04-20", "2026-12-01"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// Outputs must always be sorted -- universal invariant.
func TestDates_OutputAlwaysSorted(t *testing.T) {
	in := "dates: 2026-12-01, 2025-01-15, 2026-04-20, 2024-09-30"
	got := DatesAt(in, testRef)
	if !sort.StringsAreSorted(got) {
		t.Errorf("output not sorted: %v", got)
	}
}

// Malformed relative phrases return empty without panicking.
func TestDates_MalformedRelative(t *testing.T) {
	// Regex won't match these, so they shouldn't produce output.
	for _, in := range []string{
		"in some weeks", // no number
		"last nothing",  // unknown target
		"",              // empty
	} {
		got := DatesAt(in, testRef)
		if len(got) != 0 {
			t.Errorf("%q produced %v, want empty", in, got)
		}
	}
}
