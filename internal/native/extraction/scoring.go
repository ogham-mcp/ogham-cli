package extraction

import (
	"log/slog"
	"strings"
)

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
// Backward-compatible entry point. Uses English rules, matching the
// pre-language-plumbing behaviour to keep parity tests stable.
func Importance(content string, tags []string) float64 {
	return ImportanceForLang(content, tags, "en")
}

// ImportanceForLang is the language-aware variant. lang is a 2-letter
// code ("en", "de", ...) or empty / unknown -- empty or unknown codes
// fall back to English and emit a single debug-level slog warning so
// operators notice config drift without failing requests.
//
// The Python reference (compute_importance) unions every language's
// signal words into one global set; we do the same via Union mode when
// lang == "all" or "*". Default is per-language because the Go call
// site knows the memory's language from metadata.
func ImportanceForLang(content string, tags []string, lang string) float64 {
	rules := resolveRules(lang)
	score := 0.2

	lower := strings.ToLower(content)

	decisionWords := rules.DecisionWords
	errorWords := rules.ErrorWords
	architectureWords := rules.ArchitectureWords

	if containsAnyWord(lower, decisionWords) {
		score += 0.3
	}
	if containsAnyWord(lower, errorWords) || errorTypeRe.MatchString(content) {
		score += 0.2
	}
	if containsAnyWord(lower, architectureWords) {
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

// resolveRules loads a LanguageRules or falls back to English on error.
// The fallback is silent at call rate (debug slog) because the caller
// shipped the request already; we don't want a per-store DB write to
// tank on a YAML load failure.
func resolveRules(lang string) *LanguageRules {
	rules, err := LoadLanguage(lang)
	if err == nil && rules != nil {
		return rules
	}
	// Fall back to English. English is shipped in the embedded FS and
	// has never failed to load; if it does, the process is broken
	// regardless and we return an empty ruleset to avoid a nil deref.
	slog.Debug("extraction: language load failed, falling back to english",
		"lang", lang, "err", err)
	en, enErr := LoadLanguage("en")
	if enErr != nil || en == nil {
		slog.Warn("extraction: english rules unavailable -- scoring degraded",
			"err", enErr)
		return &LanguageRules{}
	}
	return en
}

// containsAnyWord reports whether lowerContent contains any of the
// signal words as a substring. Matches Python compute_importance's
// `any(word in content_lower for word in word_set)` -- substring
// semantics, not word-boundary. Intentional: Python uses the same
// and the signal words are long enough that false positives are rare.
func containsAnyWord(lowerContent string, words []string) bool {
	for _, w := range words {
		if strings.Contains(lowerContent, strings.ToLower(w)) {
			return true
		}
	}
	return false
}
