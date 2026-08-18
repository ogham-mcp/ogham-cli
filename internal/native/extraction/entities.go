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
//
// A person: class existed until 2026-08-18 and was removed: it was the
// only category with no unambiguous syntactic marker, and no shape-only
// rule separated names from Title Case noun phrases across both the
// technical-prose and hand-written genres. See ogham-cli#44/#45.
//
// Multi-language support (de/fr/es/zh) and the richer enrichment entities
// (events, emotions, relationships, quantities, preferences, locations via
// GeoText) are v0.6 scope. Do NOT extend this file with YAML word lists
// until then.
package extraction

import (
	"regexp"
	"sort"
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

// Entities extracts typed tag strings from content and returns a sorted,
// deduplicated, length-capped slice. Output shape parity with Python's
// extract_entities(): each element is prefix:value.
//
// Uses English person-name rules. For localised content, callers should
// use EntitiesForLang so the denylist vocab swaps to the memory's
// language.
func Entities(content string) []string {
	return EntitiesForLang(content, "en")
}

// EntitiesForLang is the language-aware entity extractor. Only the
// person-name classifier is language-sensitive today -- the CamelCase,
// file-path, and error-type regexes are universal because their
// anchors (A-Z, dot segments, Error/Exception suffix) don't vary
// by locale.
func EntitiesForLang(content, lang string) []string {
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
