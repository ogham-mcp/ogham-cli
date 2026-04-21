package extraction

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dates extracts sorted deduplicated ISO-format (YYYY-MM-DD) dates from
// content. Recognises three families and mirrors src/ogham/extraction.py::
// extract_dates:
//
//   - ISO machine dates:   2026-04-20 or 2026/04/20 (slash normalised)
//   - Natural English:     "April 20, 2026" / "20 April 2026"
//     case-insensitive, optional ordinal suffix
//   - Relative phrases:    "yesterday" / "today" / "tomorrow" /
//     "last|next|this <weekday|week|month|year>" /
//     "N days|weeks|months|years ago" /
//     "in N days|weeks|months|years"
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
// PICT matrix asserts deterministic expected dates. English-only --
// a language-aware variant is available via DatesAtForLang.
func DatesAt(content string, ref time.Time) []string {
	return DatesAtForLang(content, ref, "en")
}

// DatesAtForLang resolves dates using the specified language's
// month names, weekday names, and relative-phrase anchors
// (today/tomorrow/yesterday equivalents). Unknown language codes
// fall back to English; see resolveRules for the logging policy.
func DatesAtForLang(content string, ref time.Time, lang string) []string {
	seen := make(map[string]struct{}, 4)
	pack := datePackFor(lang)

	// ISO (slash or dash separator). Normalise slashes to dashes on
	// output so all tokens share one canonical form.
	for _, m := range isoDateMatcher.FindAllString(content, -1) {
		seen[normaliseISOSeparators(m)] = struct{}{}
	}

	// Natural dates (e.g. "15 März 2026"). Each returns a parsed
	// time.Time already anchored to the detected year/month/day.
	if pack.naturalRe != nil {
		for _, m := range pack.naturalRe.FindAllStringSubmatchIndex(content, -1) {
			if d, ok := parseNaturalMatchPack(content, m, pack); ok {
				seen[d.Format("2006-01-02")] = struct{}{}
			}
		}
	}

	// Relative phrases only fire if no absolute date was detected.
	// Python: `if not dates:` guards this branch.
	if len(seen) == 0 && pack.relativeRe != nil {
		for _, m := range pack.relativeRe.FindAllString(content, -1) {
			if d, ok := parseRelativePack(strings.ToLower(m), ref, pack); ok {
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

// --- compiled patterns (shared across languages) ----------------------

var (
	// \b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b -- mirrors Python _ISO_DATE.
	isoDateMatcher = regexp.MustCompile(`\b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b`)
)

// datePack is the compiled per-language date vocabulary. Cached by
// language code so the (regex-heavy) compile happens once per process.
type datePack struct {
	// Language code this pack was built from. Empty means English.
	Lang string

	// Month name (lowercase) -> time.Month. Includes any abbreviations
	// and umlaut variants defined in the YAML.
	Months map[string]time.Month

	// Weekday name (lowercase) -> time.Weekday. Adverbial forms
	// ("montags") share the weekday index with their base name.
	Weekdays map[string]time.Weekday

	// Today/tomorrow/yesterday anchors as YAML-driven phrases. Maps
	// to day offsets relative to ref.
	Anchors map[string]int

	// Lookup for modifier words ("last", "next", "this"). Each bucket
	// resolves to a signed offset applied to a weekday / period target.
	Modifiers map[string]string // raw word -> canonical "last"|"next"|"this"

	// Period target names ("week", "month", "year"), mapped to their
	// canonical English form.
	Periods map[string]string // raw word -> "week"|"month"|"year"

	// Unit names used in "N days ago" / "in N months" patterns, mapped
	// to their canonical English singular ("day", "week", "month", "year").
	Units map[string]string

	// "ago" marker words and "in" markers (direction).
	AgoMarkers []string
	InMarkers  []string

	// naturalRe matches ordered natural-date shapes for this language.
	// group 1 = month name (month-first) or empty
	// group 2 = day number (month-first) or empty
	// group 3 = year (month-first) or empty
	// group 4 = day number (day-first) or empty
	// group 5 = month name (day-first) or empty
	// group 6 = year (day-first) or empty
	naturalRe *regexp.Regexp

	// relativeRe matches "today", "yesterday", "in N days", "last Monday",
	// etc. Alternation is YAML-driven so German "gestern" / "morgen"
	// work without hand-coded English fallbacks.
	relativeRe *regexp.Regexp
}

var (
	datePackCacheMu sync.Mutex
	datePackCache   = map[string]*datePack{}
)

// datePackFor returns the compiled date vocabulary for lang, falling back
// to English on load failure. Cached; callers can invoke per-store
// without re-compiling regexes.
func datePackFor(lang string) *datePack {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = "en"
	}
	datePackCacheMu.Lock()
	defer datePackCacheMu.Unlock()
	if p, ok := datePackCache[lang]; ok {
		return p
	}
	p := buildDatePack(lang)
	datePackCache[lang] = p
	return p
}

// buildDatePack assembles the regex + maps for a language. Separated
// from datePackFor so tests can force rebuilds via ClearDatePackCache.
func buildDatePack(lang string) *datePack {
	rules := resolveRules(lang)
	// English anchors / modifiers / periods / units are always present
	// so the "lang was unknown, fell back to English but content is
	// English" case Just Works. For non-English packs we STILL include
	// English anchors so mixed-language content (a German memo that
	// mentions "tomorrow") is not silently missed.
	pack := &datePack{
		Lang:      lang,
		Months:    map[string]time.Month{},
		Weekdays:  map[string]time.Weekday{},
		Anchors:   map[string]int{},
		Modifiers: map[string]string{},
		Periods:   map[string]string{},
		Units:     map[string]string{},
	}

	for name, num := range rules.MonthNames {
		if num < 1 || num > 12 {
			continue
		}
		pack.Months[strings.ToLower(name)] = time.Month(num)
	}
	for name, idx := range rules.DayNames {
		pack.Weekdays[strings.ToLower(name)] = time.Weekday(idx)
	}

	// Resolve language-specific anchors + directions from the YAML-
	// loaded vocab: today/tomorrow/yesterday, last/next/this, week/
	// month/year, day/week/month/year, ago/in markers. For languages
	// whose YAML lacks these fields yet, fall back to English forms
	// via the base pack.
	pack.Anchors = mergeStringInt(pack.Anchors, rules.TodayWords, 0)
	pack.Anchors = mergeStringInt(pack.Anchors, rules.TomorrowWords, +1)
	pack.Anchors = mergeStringInt(pack.Anchors, rules.YesterdayWords, -1)

	// Modifier / period / unit / ago / in vocab come from the YAML if
	// present, otherwise English defaults keep behaviour identical.
	pack.Modifiers = mergeStringString(pack.Modifiers, rules.ModifierLast, "last")
	pack.Modifiers = mergeStringString(pack.Modifiers, rules.ModifierNext, "next")
	pack.Modifiers = mergeStringString(pack.Modifiers, rules.ModifierThis, "this")

	pack.Periods = mergeStringString(pack.Periods, rules.PeriodWeek, "week")
	pack.Periods = mergeStringString(pack.Periods, rules.PeriodMonth, "month")
	pack.Periods = mergeStringString(pack.Periods, rules.PeriodYear, "year")

	pack.Units = mergeStringString(pack.Units, rules.UnitDay, "day")
	pack.Units = mergeStringString(pack.Units, rules.UnitWeek, "week")
	pack.Units = mergeStringString(pack.Units, rules.UnitMonth, "month")
	pack.Units = mergeStringString(pack.Units, rules.UnitYear, "year")

	pack.AgoMarkers = lowerAll(rules.AgoMarkers)
	pack.InMarkers = lowerAll(rules.InMarkers)

	// For any language that doesn't define the new locked anchor lists,
	// mix in English so we don't regress behaviour.
	if lang != "en" {
		englishPack := englishBasePack()
		mergeDateFallbacks(pack, englishPack)
	}

	pack.naturalRe = buildNaturalRe(pack)
	pack.relativeRe = buildRelativeRe(pack)
	return pack
}

// englishBasePack is the hardcoded fallback for languages that haven't
// populated the new anchor YAML blocks yet. Matches the pre-plumbing
// English vocabulary so swapping Dates -> DatesAtForLang("en") leaves
// observable output unchanged.
var (
	englishBasePackOnce sync.Once
	englishBasePackVal  *datePack
)

func englishBasePack() *datePack {
	englishBasePackOnce.Do(func() {
		p := &datePack{
			Lang:   "en",
			Months: englishMonths,
			Weekdays: map[string]time.Weekday{
				"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
				"wednesday": time.Wednesday, "thursday": time.Thursday,
				"friday": time.Friday, "saturday": time.Saturday,
			},
			Anchors: map[string]int{
				"yesterday": -1,
				"today":     0,
				"tomorrow":  +1,
			},
			Modifiers: map[string]string{
				"last": "last", "next": "next", "this": "this",
			},
			Periods: map[string]string{
				"week": "week", "month": "month", "year": "year",
			},
			Units: map[string]string{
				"day": "day", "days": "day",
				"week": "week", "weeks": "week",
				"month": "month", "months": "month",
				"year": "year", "years": "year",
			},
			AgoMarkers: []string{"ago"},
			InMarkers:  []string{"in"},
		}
		p.naturalRe = buildNaturalRe(p)
		p.relativeRe = buildRelativeRe(p)
		englishBasePackVal = p
	})
	return englishBasePackVal
}

// mergeDateFallbacks copies English anchor/modifier/period/unit entries
// into the target pack ONLY for keys the target doesn't already define.
// Preserves language-specific overrides while keeping English as a
// baseline for mixed-language content.
func mergeDateFallbacks(dst, src *datePack) {
	for k, v := range src.Anchors {
		if _, ok := dst.Anchors[k]; !ok {
			dst.Anchors[k] = v
		}
	}
	for k, v := range src.Modifiers {
		if _, ok := dst.Modifiers[k]; !ok {
			dst.Modifiers[k] = v
		}
	}
	for k, v := range src.Periods {
		if _, ok := dst.Periods[k]; !ok {
			dst.Periods[k] = v
		}
	}
	for k, v := range src.Units {
		if _, ok := dst.Units[k]; !ok {
			dst.Units[k] = v
		}
	}
	if len(dst.AgoMarkers) == 0 {
		dst.AgoMarkers = append(dst.AgoMarkers, src.AgoMarkers...)
	}
	if len(dst.InMarkers) == 0 {
		dst.InMarkers = append(dst.InMarkers, src.InMarkers...)
	}
	for k, v := range src.Weekdays {
		if _, ok := dst.Weekdays[k]; !ok {
			dst.Weekdays[k] = v
		}
	}
	for k, v := range src.Months {
		if _, ok := dst.Months[k]; !ok {
			dst.Months[k] = v
		}
	}
}

// buildNaturalRe assembles the case-insensitive natural-date regex for
// pack.Months. Mirrors the original English pattern:
//
//	month-first: Month day(st|nd|rd|th)? (,)? year
//	day-first:   day Month year
func buildNaturalRe(pack *datePack) *regexp.Regexp {
	if len(pack.Months) == 0 {
		return nil
	}
	// Sort longest-first so "September" matches before "Sep".
	names := make([]string, 0, len(pack.Months))
	for name := range pack.Months {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	alt := make([]string, 0, len(names))
	for _, n := range names {
		alt = append(alt, regexp.QuoteMeta(n))
	}
	monthAlt := strings.Join(alt, "|")
	pattern := `(?i)` +
		`\b(` + monthAlt + `)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s*(\d{4})\b` +
		`|` +
		`\b(\d{1,2})\.?\s+(` + monthAlt + `)\s+(\d{4})\b`
	return regexp.MustCompile(pattern)
}

// buildRelativeRe assembles the case-insensitive relative-phrase regex.
// Covers: anchor words, modifier+weekday|period, "N units ago", "in N units".
func buildRelativeRe(pack *datePack) *regexp.Regexp {
	anchorsAlt := altFromMap(pack.Anchors)
	weekdaysAlt := altFromWeekdays(pack.Weekdays)
	modifiersAlt := altFromStringMap(pack.Modifiers)
	periodsAlt := altFromStringMap(pack.Periods)
	unitsAlt := altFromStringMap(pack.Units)
	agoAlt := altFromSlice(pack.AgoMarkers)
	inAlt := altFromSlice(pack.InMarkers)

	parts := []string{}
	if modifiersAlt != "" && (weekdaysAlt != "" || periodsAlt != "") {
		inner := []string{}
		if weekdaysAlt != "" {
			inner = append(inner, weekdaysAlt)
		}
		if periodsAlt != "" {
			inner = append(inner, periodsAlt)
		}
		parts = append(parts, `(?:`+modifiersAlt+`)\s+(?:`+strings.Join(inner, "|")+`)`)
	}
	if anchorsAlt != "" {
		parts = append(parts, anchorsAlt)
	}
	if unitsAlt != "" && agoAlt != "" {
		// Postfix: "2 weeks ago" (English).
		parts = append(parts, `\d+\s+(?:`+unitsAlt+`)\s+(?:`+agoAlt+`)`)
		// Prefix: "vor 2 Wochen" (German). Harmless for English because
		// English doesn't ship "ago" in sentence-initial position.
		parts = append(parts, `(?:`+agoAlt+`)\s+\d+\s+(?:`+unitsAlt+`)`)
	}
	if unitsAlt != "" && inAlt != "" {
		parts = append(parts, `(?:`+inAlt+`)\s+\d+\s+(?:`+unitsAlt+`)`)
	}

	if len(parts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(parts, "|") + `)\b`)
}

func altFromMap(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, regexp.QuoteMeta(k))
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

func altFromStringMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, regexp.QuoteMeta(k))
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

func altFromSlice(s []string) string {
	if len(s) == 0 {
		return ""
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(s))
	for _, w := range s {
		w = strings.ToLower(w)
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, regexp.QuoteMeta(w))
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return strings.Join(out, "|")
}

func altFromWeekdays(m map[string]time.Weekday) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, regexp.QuoteMeta(k))
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

func mergeStringInt(dst map[string]int, src []string, val int) map[string]int {
	for _, s := range src {
		dst[strings.ToLower(s)] = val
	}
	return dst
}

func mergeStringString(dst map[string]string, src []string, val string) map[string]string {
	for _, s := range src {
		dst[strings.ToLower(s)] = val
	}
	return dst
}

func lowerAll(s []string) []string {
	out := make([]string, 0, len(s))
	for _, w := range s {
		out = append(out, strings.ToLower(w))
	}
	return out
}

// englishMonths is the hardcoded English month map the
// englishBasePack() reads at boot. Kept as the canonical source rather
// than re-deriving from en.yaml at each build so the base pack is
// immune to YAML edits that briefly break the lookup.
var (
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

// parseNaturalMatchPack consumes a FindAllStringSubmatchIndex result and
// resolves the captured month-name + day + year into a time.Time using
// the pack's month lookup. Returns ok=false on impossible dates
// (eg. Feb 31) OR if the month-name is not in the pack (which shouldn't
// happen because the regex was built from the same map, but defensive).
func parseNaturalMatchPack(content string, idx []int, pack *datePack) (time.Time, bool) {
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
	month, ok := pack.Months[strings.ToLower(monthName)]
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
	t := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if t.Month() != month || t.Day() != day {
		return time.Time{}, false
	}
	return t, true
}

// parseRelativePack resolves a lowercased phrase like "yesterday",
// "gestern", "last monday", "letzten Montag", "2 days ago", "in 3 weeks"
// against `ref`. Unrecognised shapes return ok=false so the caller can
// skip without ambiguity.
func parseRelativePack(phrase string, ref time.Time, pack *datePack) (time.Time, bool) {
	day := func(offset int) time.Time {
		return time.Date(ref.Year(), ref.Month(), ref.Day()+offset, 0, 0, 0, 0, time.UTC)
	}

	// Anchored single words (today/yesterday/tomorrow + localised forms).
	if off, ok := pack.Anchors[phrase]; ok {
		return day(off), true
	}

	if t, ok := parseQuantifiedRelativePack(phrase, ref, pack); ok {
		return t, true
	}

	if t, ok := parseWeekdayOrPeriodRelativePack(phrase, ref, pack); ok {
		return t, true
	}

	return time.Time{}, false
}

func parseQuantifiedRelativePack(phrase string, ref time.Time, pack *datePack) (time.Time, bool) {
	fields := strings.Fields(phrase)
	if len(fields) != 3 {
		return time.Time{}, false
	}
	// Three shapes we care about:
	//   <N> <unit> <ago-marker>      English "2 weeks ago"
	//   <in-marker> <N> <unit>       English "in 3 weeks"
	//   <ago-marker> <N> <unit>      German  "vor 2 Wochen"
	//
	// German "vor" is syntactically prefix (like English "in") but
	// semantically past, so we check both positions before falling
	// through. in-markers take precedence at position 0 to preserve
	// English "in" behaviour -- avoids a false-positive if a language
	// ever ships "in" as both marker types.
	direction := 0
	var nStr, unitRaw string

	switch {
	case containsCI(pack.AgoMarkers, fields[2]):
		nStr, unitRaw = fields[0], fields[1]
		direction = -1
	case containsCI(pack.InMarkers, fields[0]):
		nStr, unitRaw = fields[1], fields[2]
		direction = +1
	case containsCI(pack.AgoMarkers, fields[0]):
		nStr, unitRaw = fields[1], fields[2]
		direction = -1
	default:
		return time.Time{}, false
	}
	n, err := strconv.Atoi(nStr)
	if err != nil {
		return time.Time{}, false
	}
	unit, ok := pack.Units[strings.ToLower(unitRaw)]
	if !ok {
		return time.Time{}, false
	}
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
	return time.Date(ref.Year()+y, ref.Month()+time.Month(mo), ref.Day()+d,
		0, 0, 0, 0, time.UTC), true
}

func parseWeekdayOrPeriodRelativePack(phrase string, ref time.Time, pack *datePack) (time.Time, bool) {
	fields := strings.Fields(phrase)
	if len(fields) != 2 {
		return time.Time{}, false
	}
	modifierRaw, targetRaw := fields[0], fields[1]
	modifier, ok := pack.Modifiers[strings.ToLower(modifierRaw)]
	if !ok {
		return time.Time{}, false
	}
	target := strings.ToLower(targetRaw)

	// Period path: week / month / year (or localised alias).
	if period, ok := pack.Periods[target]; ok {
		switch period {
		case "week":
			offset := modifierOffset(modifier, 7)
			return time.Date(ref.Year(), ref.Month(), ref.Day()+offset, 0, 0, 0, 0, time.UTC), true
		case "month":
			offset := modifierOffset(modifier, 1)
			return time.Date(ref.Year(), ref.Month()+time.Month(offset), ref.Day(), 0, 0, 0, 0, time.UTC), true
		case "year":
			offset := modifierOffset(modifier, 1)
			return time.Date(ref.Year()+offset, ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC), true
		}
	}

	// Weekday path.
	wd, ok := pack.Weekdays[target]
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
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day()+diff, 0, 0, 0, 0, time.UTC), true
}

// modifierOffset maps canonical modifier words to signed period
// offsets. base is the "next" value; "last" flips the sign, "this"
// zeros the offset. Keeps the per-period switch short + lint-clean.
func modifierOffset(modifier string, base int) int {
	switch modifier {
	case "last":
		return -base
	case "this":
		return 0
	default:
		return base
	}
}

func containsCI(haystack []string, needle string) bool {
	n := strings.ToLower(needle)
	for _, h := range haystack {
		if strings.ToLower(h) == n {
			return true
		}
	}
	return false
}
