// Package extraction implements the regex-driven entity + date extraction
// that runs at store_memory time. This is the Go-side absorption of what
// lives in src/ogham/extraction.py on the Python side.
//
// v0.5 Day 1 scope is English-only and covers the four pattern-based
// categories that need no external data files:
//
//   - CamelCase identifiers  -> entity:
//   - file paths             -> file:
//   - error / exception types -> error:
//   - person names           -> person:
//
// Multi-language support (de/fr/es/zh) and the richer enrichment entities
// (events, emotions, relationships, quantities, preferences, locations via
// GeoText) are v0.6 scope. Do NOT extend this file with YAML word lists
// until then.
package extraction

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// MaxEntities is the cap the Python implementation enforces on the final
// sorted result. Mirrored here so parity tests don't diverge.
const MaxEntities = 20

// filePathCap mirrors Python's `if i >= 5: break` in _FILE_PATH.finditer.
// File paths dominate noisy diffs / logs; limiting them early stops one
// content chunk from burning the entire 20-entity budget.
const filePathCap = 5

// Regexes mirror src/ogham/extraction.py:
//
//	_CAMEL_CASE = re.compile(r"\b[A-Z][a-z]+(?:[A-Z][a-zA-Z]*)+\b")
//	_FILE_PATH  = re.compile(r"(?:\.{0,2}/)?(?:[\w@.-]+/)+[\w@.-]+\.\w+")
//	_ERROR_TYPE = re.compile(r"\b\w*(?:Error|Exception)\b")
//
// Go's regexp (RE2) has no lookahead, but none of these need it. Python's
// \b and \w use the same definitions Go does (word chars = [A-Za-z0-9_]).
var (
	camelCaseRe = regexp.MustCompile(`\b[A-Z][a-z]+(?:[A-Z][a-zA-Z]*)+\b`)
	filePathRe  = regexp.MustCompile(`(?:\.{0,2}/)?(?:[\w@.\-]+/)+[\w@.\-]+\.\w+`)
	errorTypeRe = regexp.MustCompile(`\b\w*(?:Error|Exception)\b`)
)

// personPunct is the Python _PUNCT translation table (".,!?:;\"'()"),
// applied word-by-word before person-name matching so "Kevin," and
// "Burns." match "Kevin Burns" across sentence boundaries.
const personPunct = ".,!?:;\"'()"

// Entities extracts typed tag strings from content and returns a sorted,
// deduplicated, length-capped slice. Output shape parity with Python's
// extract_entities(): each element is prefix:value.
func Entities(content string) []string {
	seen := make(map[string]struct{}, 16)

	// entity:CamelCase -- programming identifiers, product names, acronyms
	// expanded into camel form ("PostgreSQL", "KubernetesAPI").
	for _, m := range camelCaseRe.FindAllString(content, -1) {
		seen["entity:"+m] = struct{}{}
	}

	// file:path -- capped at 5 in source order, mirroring Python.
	filesAdded := 0
	for _, m := range filePathRe.FindAllString(content, -1) {
		if filesAdded >= filePathCap {
			break
		}
		seen["file:"+m] = struct{}{}
		filesAdded++
	}

	// error:TypeError / error:SomeException
	for _, m := range errorTypeRe.FindAllString(content, -1) {
		seen["error:"+m] = struct{}{}
	}

	// person:First Last -- two consecutive capitalised alphabetic words,
	// stopword-filtered. Uses whitespace-split like Python's content.split().
	words := strings.Fields(content)
	for i := 0; i < len(words)-1; i++ {
		w1 := stripPersonPunct(words[i])
		w2 := stripPersonPunct(words[i+1])
		if !isLikelyPersonNamePart(w1) || !isLikelyPersonNamePart(w2) {
			continue
		}
		seen["person:"+w1+" "+w2] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > MaxEntities {
		out = out[:MaxEntities]
	}
	return out
}

// stripPersonPunct removes trailing/leading punctuation that Python's
// str.translate(_PUNCT) strips. Characters are the Python set verbatim
// so the same "Burns." -> "Burns" normalisation happens.
func stripPersonPunct(w string) string {
	if w == "" {
		return w
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(personPunct, r) {
			return -1 // drop
		}
		return r
	}, w)
}

// isLikelyPersonNamePart applies the Python heuristic: length > 1, all
// letters, leading letter uppercase, lowercase form not in the English
// stopwords set. Conservative enough that "The Company" / "Of Course"
// / "And She" don't produce person: tags.
func isLikelyPersonNamePart(w string) bool {
	if len(w) <= 1 {
		return false
	}
	runes := []rune(w)
	if !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return !isEnglishStopword(strings.ToLower(w))
}
