package extraction

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dates extracts sorted deduplicated ISO-format (YYYY-MM-DD) dates from
// content. Recognises three families and mirrors src/ogham/extraction.py::
// extract_dates:
//
//   - ISO machine dates:   2026-04-20 or 2026/04/20 (slash normalised)
//   - Natural English:     "April 20, 2026" / "20 April 2026"
//                          case-insensitive, optional ordinal suffix
//   - Relative phrases:    "yesterday" / "today" / "tomorrow" /
//                          "last|next|this <weekday|week|month|year>" /
//                          "N days|weeks|months|years ago" /
//                          "in N days|weeks|months|years"
//
// Relative phrases resolve only when no absolute date is present --
// matches Python behaviour.
//
// Output is always sorted ascending, deduplicated, and every token
// matches ^\d{4}-\d{2}-\d{2}$.
func Dates(content string) []string {
	return DatesAt(content, time.Now())
}

// DatesAt is the testable variant: relative phrases resolve against
// `ref` instead of time.Now(). Tests use a fixed ref so the committed
// PICT matrix asserts deterministic expected dates.
func DatesAt(content string, ref time.Time) []string {
	seen := make(map[string]struct{}, 4)

	// ISO (slash or dash separator). Normalise slashes to dashes on
	// output so all tokens share one canonical form.
	for _, m := range isoDateMatcher.FindAllString(content, -1) {
		seen[normaliseISOSeparators(m)] = struct{}{}
	}

	// Natural English: Month-first and Day-first. Each returns a parsed
	// time.Time already anchored to the detected year/month/day.
	for _, m := range naturalDateMatcher.FindAllStringSubmatchIndex(content, -1) {
		if d, ok := parseNaturalMatch(content, m); ok {
			seen[d.Format("2006-01-02")] = struct{}{}
		}
	}

	// Relative phrases only fire if no absolute date was detected.
	// Python: `if not dates:` guards this branch.
	if len(seen) == 0 {
		for _, m := range relativePhraseMatcher.FindAllString(content, -1) {
			if d, ok := parseRelative(strings.ToLower(m), ref); ok {
				seen[d.Format("2006-01-02")] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- compiled patterns ------------------------------------------------

var (
	// \b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b -- mirrors Python _ISO_DATE.
	isoDateMatcher = regexp.MustCompile(`\b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b`)

	// Case-insensitive natural dates, both orderings. Captures:
	//   group 1 = Month name (month-first ordering)
	//   group 2 = day number  (month-first)
	//   group 3 = year        (month-first)
	//   group 4 = day number  (day-first)
	//   group 5 = Month name  (day-first)
	//   group 6 = year        (day-first)
	naturalDateMatcher = regexp.MustCompile(
		`(?i)` +
			// month-first: Month day(optional ordinal)(optional comma) year
			`\b(` + englishMonthRe + `)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s*(\d{4})\b` +
			`|` +
			// day-first: day Month year
			`\b(\d{1,2})\s+(` + englishMonthRe + `)\s+(\d{4})\b`)

	// Relative phrases. Case-insensitive. Covers the same shapes the
	// Python regex captures; each concrete shape is handled by
	// parseRelative which does the actual arithmetic against `ref`.
	relativePhraseMatcher = regexp.MustCompile(
		`(?i)\b(` +
			`(?:last|next|this)\s+(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday|week|month|year)` +
			`|yesterday|today|tomorrow` +
			`|\d+\s+(?:days?|weeks?|months?|years?)\s+ago` +
			`|in\s+\d+\s+(?:days?|weeks?|months?|years?)` +
			`)\b`)

	englishMonthRe = `January|February|March|April|May|June|July|August|September|October|November|December` +
		`|Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sep|Oct|Nov|Dec`

	englishMonths = map[string]time.Month{
		"january": time.January, "jan": time.January,
		"february": time.February, "feb": time.February,
		"march": time.March, "mar": time.March,
		"april": time.April, "apr": time.April,
		"may":  time.May,
		"june": time.June, "jun": time.June,
		"july": time.July, "jul": time.July,
		"august": time.August, "aug": time.August,
		"september": time.September, "sep": time.September,
		"october": time.October, "oct": time.October,
		"november": time.November, "nov": time.November,
		"december": time.December, "dec": time.December,
	}
)

// --- parsers ----------------------------------------------------------

// normaliseISOSeparators canonicalises 2026/04/20 -> 2026-04-20 AND
// zero-pads single-digit month / day ("2026-4-2" -> "2026-04-02").
func normaliseISOSeparators(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return s
	}
	y, m, d := parts[0], parts[1], parts[2]
	if len(m) == 1 {
		m = "0" + m
	}
	if len(d) == 1 {
		d = "0" + d
	}
	return y + "-" + m + "-" + d
}

// parseNaturalMatch consumes a FindAllStringSubmatchIndex result and
// resolves the captured month-name + day + year into a time.Time.
// Returns ok=false on impossible dates (eg. Feb 31).
func parseNaturalMatch(content string, idx []int) (time.Time, bool) {
	// FindAllStringSubmatchIndex returns 2*(1+nGroups) ints.
	// 0-1 = whole match, 2-3 = group 1, 4-5 = group 2, ... 12-13 = group 6.
	get := func(n int) string {
		start, end := idx[2*n], idx[2*n+1]
		if start < 0 || end < 0 {
			return ""
		}
		return content[start:end]
	}

	var monthName, dayS, yearS string
	if get(1) != "" {
		// month-first
		monthName, dayS, yearS = get(1), get(2), get(3)
	} else {
		// day-first
		dayS, monthName, yearS = get(4), get(5), get(6)
	}
	month, ok := englishMonths[strings.ToLower(monthName)]
	if !ok {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(dayS)
	if err != nil {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(yearS)
	if err != nil {
		return time.Time{}, false
	}
	// Reject impossible days (Feb 30 etc.) by constructing and
	// checking the round-trip.
	t := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if t.Month() != month || t.Day() != day {
		return time.Time{}, false
	}
	return t, true
}

// parseRelative resolves a lowercased phrase like "yesterday",
// "last monday", "2 days ago", "in 3 weeks" against `ref`. Returns
// a day-anchored UTC time and ok=false on unrecognised shapes.
func parseRelative(phrase string, ref time.Time) (time.Time, bool) {
	day := func(offset int) time.Time {
		return time.Date(ref.Year(), ref.Month(), ref.Day()+offset, 0, 0, 0, 0, time.UTC)
	}

	// Anchored single words.
	switch phrase {
	case "yesterday":
		return day(-1), true
	case "today":
		return day(0), true
	case "tomorrow":
		return day(1), true
	}

	// "<N> days|weeks|months|years ago" and "in <N> ..."
	if t, ok := parseQuantifiedRelative(phrase, ref); ok {
		return t, true
	}

	// "last|next|this <weekday|week|month|year>"
	if t, ok := parseWeekdayOrPeriodRelative(phrase, ref); ok {
		return t, true
	}

	return time.Time{}, false
}

var (
	agoRe = regexp.MustCompile(`^(\d+)\s+(day|week|month|year)s?\s+ago$`)
	inRe  = regexp.MustCompile(`^in\s+(\d+)\s+(day|week|month|year)s?$`)
)

func parseQuantifiedRelative(phrase string, ref time.Time) (time.Time, bool) {
	var m []string
	direction := 0
	switch {
	case agoRe.MatchString(phrase):
		m = agoRe.FindStringSubmatch(phrase)
		direction = -1
	case inRe.MatchString(phrase):
		m = inRe.FindStringSubmatch(phrase)
		direction = +1
	default:
		return time.Time{}, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, false
	}
	unit := m[2]
	y, mo, d := 0, 0, 0
	switch unit {
	case "day":
		d = direction * n
	case "week":
		d = direction * n * 7
	case "month":
		mo = direction * n
	case "year":
		y = direction * n
	default:
		return time.Time{}, false
	}
	t := time.Date(ref.Year()+y, ref.Month()+time.Month(mo), ref.Day()+d,
		0, 0, 0, 0, time.UTC)
	return t, true
}

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

func parseWeekdayOrPeriodRelative(phrase string, ref time.Time) (time.Time, bool) {
	fields := strings.Fields(phrase)
	if len(fields) != 2 {
		return time.Time{}, false
	}
	modifier, target := fields[0], fields[1]
	switch target {
	case "week":
		offset := 7
		if modifier == "last" {
			offset = -7
		} else if modifier == "this" {
			offset = 0
		}
		return time.Date(ref.Year(), ref.Month(), ref.Day()+offset, 0, 0, 0, 0, time.UTC), true
	case "month":
		offset := 1
		if modifier == "last" {
			offset = -1
		} else if modifier == "this" {
			offset = 0
		}
		return time.Date(ref.Year(), ref.Month()+time.Month(offset), ref.Day(), 0, 0, 0, 0, time.UTC), true
	case "year":
		offset := 1
		if modifier == "last" {
			offset = -1
		} else if modifier == "this" {
			offset = 0
		}
		return time.Date(ref.Year()+offset, ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC), true
	}
	// Weekday name
	wd, ok := weekdayNames[target]
	if !ok {
		return time.Time{}, false
	}
	diff := int(wd) - int(ref.Weekday())
	switch modifier {
	case "last":
		if diff >= 0 {
			diff -= 7
		}
	case "next":
		if diff <= 0 {
			diff += 7
		}
	case "this":
		// this Tuesday = current week's Tuesday (before or after today)
		// if today IS Tuesday, resolve to today.
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day()+diff, 0, 0, 0, 0, time.UTC), true
}

