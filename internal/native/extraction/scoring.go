package extraction

import "strings"

// Importance scores content on a 0.0-1.0 scale. Mirrors
// src/ogham/extraction.py::compute_importance:
//
//	base 0.2
//	+ 0.3 if content contains a DECISION_WORDS signal
//	+ 0.2 if ERROR_WORDS signal or an ...Error/...Exception regex match
//	+ 0.2 if ARCHITECTURE_WORDS signal
//	+ 0.1 if a file path appears
//	+ 0.1 if a code fence (```) or inline code (`) marker appears
//	+ 0.1 if len(content) > 500
//	+ 0.1 if len(tags) >= 3
//	capped at 1.0
//
// English-only for v0.5; v0.6 will switch to YAML-loaded multilingual
// word sets matching the Python ogham.data.loader contract.
func Importance(content string, tags []string) float64 {
	score := 0.2

	lower := strings.ToLower(content)

	if containsAnyWord(lower, decisionWordsEN) {
		score += 0.3
	}
	if containsAnyWord(lower, errorWordsEN) || errorTypeRe.MatchString(content) {
		score += 0.2
	}
	if containsAnyWord(lower, architectureWordsEN) {
		score += 0.2
	}
	if filePathRe.MatchString(content) {
		score += 0.1
	}
	if strings.Contains(content, "```") || strings.Contains(content, "`") {
		score += 0.1
	}
	if len(content) > 500 {
		score += 0.1
	}
	if len(tags) >= 3 {
		score += 0.1
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

// containsAnyWord reports whether lowerContent contains any of the
// signal words as a substring. Matches Python compute_importance's
// `any(word in content_lower for word in word_set)` -- substring
// semantics, not word-boundary. Intentional: Python uses the same
// and the signal words are long enough that false positives are rare.
func containsAnyWord(lowerContent string, words []string) bool {
	for _, w := range words {
		if strings.Contains(lowerContent, w) {
			return true
		}
	}
	return false
}

// --- English signal word sets ----------------------------------------
//
// Sourced from src/ogham/data/languages/en.yaml (keys decision_words /
// error_words / architecture_words). Kept in sync by hand at v0.5;
// v0.6 switches to a YAML loader with multi-language support.

var decisionWordsEN = []string{
	"decided", "chose", "choosing", "switched", "migrated",
	"selected", "picked", "opted", "replaced", "adopted",
}

var errorWordsEN = []string{
	"error", "exception", "failed", "failure", "bug",
	"crash", "broken", "traceback", "timeout", "denied",
}

var architectureWordsEN = []string{
	"design", "pattern", "refactor", "architecture", "restructure",
	"modular", "decouple", "abstract", "interface", "migrate",
}
