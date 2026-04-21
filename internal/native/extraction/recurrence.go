package extraction

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Recurrence detects recurring-event signals in content and returns a
// normalised pattern string + list of prefixed tags suitable for the
// store-time metadata merge. Mirrors Python's extract_recurrence in
// src/ogham/extraction.py but returns a pattern+tags shape so the
// Go store pipeline can emit a `recurrence:<normalised>` tag without
// a separate transform.
//
// Detection runs in two stages:
//
//  1. YAML-driven explicit phrases (recurrence_patterns block):
//     "daily", "weekly", "biweekly", "monthly", "yearly", German
//     equivalents ("wöchentlich", "monatlich", ...). Each pattern
//     maps to a canonical normalised category.
//
//  2. every_words + day_names -- Python's original path. "every
//     monday" / "jeden Dienstag" / adverbial "montags" all fire this
//     branch. Multiple day hits collapse to a single "weekly"
//     pattern + one recurrence:<dayname> tag per matched day.
//
// Returns (pattern, tags, true) on a hit, (_, nil, false) otherwise.
// Tags are sorted + deduplicated; the canonical pattern is the coarse
// category ("daily" / "weekly" / "biweekly" / "monthly" / "quarterly"
// / "yearly"). Callers should:
//
//	metadata["recurrence"] = pattern
//	tags = append(tags, recurrenceTags...)
//
// The pattern is safe to store as-is in JSONB.
func Recurrence(content, lang string) (pattern string, tags []string, ok bool) {
	rules := resolveRules(lang)
	content = strings.ToLower(content)

	tagSet := map[string]struct{}{}
	var canonical string

	// Stage 1: explicit recurrence_patterns from the YAML. Compiled
	// once per language (Getenv-safe; see recurrencePackFor cache).
	// Patterns are iterated longest-first so "bi-weekly" wins over
	// "weekly" on the same substring. Once a normalised category is
	// claimed we stop adding lesser tags; otherwise every overlapping
	// pattern ("daily"/"weekly"/...) would pollute the tag set.
	pack := recurrencePackFor(lang)
	for _, pat := range pack.Patterns {
		if pat.Re == nil {
			continue
		}
		if pat.Re.MatchString(content) {
			if canonical == "" {
				canonical = pat.Normalised
				tagSet["recurrence:"+pat.Normalised] = struct{}{}
			}
			// Stop after the first (longest) explicit match. Multiple
			// distinct-category matches in one memo are rare and the
			// first one wins by specificity.
			break
		}
	}

	// Stage 2: every_words × day_names. Tracks every matched day so a
	// "every Monday and Wednesday" memo tags both days. An "every" hit
	// without a day still emits the pattern (generic recurring) --
	// Python returns None in that case, but the downstream consumer
	// wants the signal regardless (future: resolve to whatever the
	// following noun is).
	everyHit := anyPhraseMatch(content, rules.EveryWords)
	dayHits := matchDayNames(content, rules.DayNames, lang)

	if everyHit || len(dayHits) > 0 {
		// German adverbial forms ("montags") imply recurrence without an
		// every_word. Matches Python's `name.endswith("s")` shortcut.
		// If we reach this branch with !everyHit, dayHits is guaranteed
		// non-empty already (the outer `if` wouldn't have fired otherwise),
		// so the adverbial check is additive rather than a fallback.
		if !everyHit && lang == "de" {
			adverbial := matchGermanAdverbialDays(content, rules.DayNames)
			if len(adverbial) > 0 {
				dayHits = mergeDayIdx(dayHits, adverbial)
			}
		}
		if len(dayHits) > 0 {
			if canonical == "" {
				canonical = "weekly"
			}
			for _, idx := range dayHits {
				tagSet["recurrence:"+dayIndexToName(idx)] = struct{}{}
			}
			tagSet["recurrence:weekly"] = struct{}{}
		} else if everyHit && canonical == "" {
			// "every X" where X is not a day -- keep the weekly default
			// (matches human-intent "every release"/"every standup").
			canonical = "weekly"
			tagSet["recurrence:weekly"] = struct{}{}
		}
	}

	if canonical == "" {
		return "", nil, false
	}

	out := make([]string, 0, len(tagSet))
	for t := range tagSet {
		out = append(out, t)
	}
	sort.Strings(out)
	return canonical, out, true
}

// --- recurrence pattern cache ---------------------------------------

type recurrencePack struct {
	Lang     string
	Patterns []compiledRecurrence
}

type compiledRecurrence struct {
	Re         *regexp.Regexp
	Normalised string
}

var (
	recurrencePackMu    sync.Mutex
	recurrencePackCache = map[string]*recurrencePack{}
)

// recurrencePackFor compiles the YAML-declared recurrence_patterns for
// lang and memoises the result. On YAML load failure returns an empty
// pack so callers can always iterate pack.Patterns without a nil check.
//
// Patterns are sorted longest-raw-string first so more specific anchors
// win over generic ones. "bi-weekly" contains "weekly" as a substring;
// without the sort, "weekly" would match first and mis-normalise the
// memo as weekly.
func recurrencePackFor(lang string) *recurrencePack {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = "en"
	}
	recurrencePackMu.Lock()
	defer recurrencePackMu.Unlock()
	if p, ok := recurrencePackCache[lang]; ok {
		return p
	}
	rules := resolveRules(lang)
	pack := &recurrencePack{Lang: lang}

	// Copy + sort by raw pattern length (descending) before compiling
	// so the longest anchor wins on overlap.
	sorted := make([]RecurrencePattern, len(rules.RecurrencePatterns))
	copy(sorted, rules.RecurrencePatterns)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].Pattern) > len(sorted[j].Pattern)
	})

	for _, rp := range sorted {
		if rp.Pattern == "" || rp.Normalised == "" {
			continue
		}
		// Build a case-insensitive, word-boundary-anchored regex. If the
		// pattern contains spaces or non-ASCII, let it match as-is since
		// \b between non-word characters is ambiguous in RE2.
		var re *regexp.Regexp
		var err error
		if strings.ContainsAny(rp.Pattern, " \t") || !isASCII(rp.Pattern) {
			re, err = regexp.Compile(`(?i)` + rp.Pattern)
		} else {
			re, err = regexp.Compile(`(?i)\b(?:` + rp.Pattern + `)\b`)
		}
		if err != nil {
			// Skip malformed patterns instead of failing the whole pack.
			// The language YAML review catches these in the parse test.
			continue
		}
		pack.Patterns = append(pack.Patterns, compiledRecurrence{
			Re:         re,
			Normalised: rp.Normalised,
		})
	}
	recurrencePackCache[lang] = pack
	return pack
}

// --- helpers ---------------------------------------------------------

// anyPhraseMatch reports true if any phrase in needles appears in
// haystack as a word-bounded match (substring for CJK / non-ASCII).
// Case-insensitive; caller should pre-lowercase haystack.
func anyPhraseMatch(haystack string, needles []string) bool {
	for _, n := range needles {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if !isASCII(n) || strings.ContainsAny(n, " \t") {
			if strings.Contains(haystack, n) {
				return true
			}
			continue
		}
		if wordBoundaryContains(haystack, n) {
			return true
		}
	}
	return false
}

// wordBoundaryContains reports true if needle appears in haystack with
// ASCII word-boundary characters on both sides.
func wordBoundaryContains(haystack, needle string) bool {
	idx := 0
	for {
		next := strings.Index(haystack[idx:], needle)
		if next < 0 {
			return false
		}
		start := idx + next
		end := start + len(needle)
		leftOK := start == 0 || !isWordByte(haystack[start-1])
		rightOK := end == len(haystack) || !isWordByte(haystack[end])
		if leftOK && rightOK {
			return true
		}
		idx = start + 1
		if idx >= len(haystack) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_'
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// matchDayNames returns the sorted unique day indices present in
// content. Longer names match first to avoid partial matches ("vendredi"
// is not tagged as "di"). CJK-style names short-circuit the word
// boundary check.
func matchDayNames(content string, dayNames map[string]int, _ string) []int {
	if len(dayNames) == 0 {
		return nil
	}
	type pair struct {
		name string
		idx  int
	}
	ordered := make([]pair, 0, len(dayNames))
	for name, idx := range dayNames {
		if len(name) < 2 {
			continue
		}
		ordered = append(ordered, pair{strings.ToLower(name), idx})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i].name) != len(ordered[j].name) {
			return len(ordered[i].name) > len(ordered[j].name)
		}
		return ordered[i].name < ordered[j].name
	})

	hit := map[int]struct{}{}
	for _, p := range ordered {
		if !isASCII(p.name) {
			if strings.Contains(content, p.name) {
				hit[p.idx] = struct{}{}
			}
			continue
		}
		if wordBoundaryContains(content, p.name) {
			hit[p.idx] = struct{}{}
		}
	}
	if len(hit) == 0 {
		return nil
	}
	out := make([]int, 0, len(hit))
	for i := range hit {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// matchGermanAdverbialDays finds the "-s" adverbial forms (montags,
// dienstags, ...) and returns their day indices. Mirrors Python's
// `name.endswith("s") and name in get_day_names("de")` shortcut which
// lets German signal recurrence without requiring "jeden".
func matchGermanAdverbialDays(content string, dayNames map[string]int) []int {
	hit := map[int]struct{}{}
	for name, idx := range dayNames {
		if !strings.HasSuffix(strings.ToLower(name), "s") {
			continue
		}
		if wordBoundaryContains(content, strings.ToLower(name)) {
			hit[idx] = struct{}{}
		}
	}
	if len(hit) == 0 {
		return nil
	}
	out := make([]int, 0, len(hit))
	for i := range hit {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

func mergeDayIdx(a, b []int) []int {
	seen := map[int]struct{}{}
	for _, i := range a {
		seen[i] = struct{}{}
	}
	for _, i := range b {
		seen[i] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// dayIndexToName maps a Sunday=0 index to its English day name. Used
// for the `recurrence:<day>` tag so the output is language-independent
// even when the memory itself is German / French / etc. Callers that
// want localised tags can post-process from the canonical English name.
func dayIndexToName(idx int) string {
	switch idx {
	case 0:
		return "sunday"
	case 1:
		return "monday"
	case 2:
		return "tuesday"
	case 3:
		return "wednesday"
	case 4:
		return "thursday"
	case 5:
		return "friday"
	case 6:
		return "saturday"
	default:
		return "unknown"
	}
}
