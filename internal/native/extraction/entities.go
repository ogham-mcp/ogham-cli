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
	_ "embed"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// MaxEntities is the cap the Python implementation enforces on the final
// sorted result. Mirrored here so parity tests don't diverge.
const MaxEntities = 20

// filePathCap mirrors Python's `if i >= 5: break` in _FILE_PATH.finditer.
// File paths dominate noisy diffs / logs; limiting them early stops one
// content chunk from burning the entire 20-entity budget.
const filePathCap = 5

// personContextWindow is the number of tokens before a candidate bigram
// the classifier scans for a licensing context word ("by", "from", ...).
// Required by rule 4 of addPersonNames whenever a bigram is not plain
// Titlecase -- the case the multi-lang stopword filter lets through and
// a language's denylist hasn't been populated for.
const personContextWindow = 3

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

//go:embed languages/multilang_stopwords.txt
var multilangStopwordsRaw string

// multilangStopwords is the union of 34-language stopwords baked at
// extraction time by Python's stop_words package and vendored here as
// a committed text file. Python filters person-name candidates against
// this union; without it, method-name bigrams like "Clear Stats" slip
// through. The dataset changes rarely (refresh by regenerating the
// .txt from the Python loader) so the vendored copy is low-maintenance.
var multilangStopwords = buildMultilangStopwordSet(multilangStopwordsRaw)

func buildMultilangStopwordSet(raw string) map[string]struct{} {
	set := make(map[string]struct{}, 9500)
	for _, line := range strings.Split(raw, "\n") {
		w := strings.TrimSpace(line)
		if w == "" {
			continue
		}
		set[w] = struct{}{}
	}
	return set
}

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

	// person:First Last -- multi-stage classifier. See addPersonNames
	// for the rule ordering.
	addPersonNames(content, lang, seen)

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
// letters, leading letter uppercase, lowercase form not in the
// multi-lang stopwords union. Conservative enough that "The Company" /
// "Of Course" / "And She" don't produce person: tags.
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
	lower := strings.ToLower(w)
	if _, stop := multilangStopwords[lower]; stop {
		return false
	}
	// Legacy English-only path kept for backward compat; multilang
	// union already covers the English set so this is redundant but
	// harmless.
	return !isEnglishStopword(lower)
}

// --- Person-name classifier (v0.7 tightening) ------------------------

// personGate holds the per-language rules used by addPersonNames. Cached
// by language code so YAML lookups are per-process, not per-memory.
type personGate struct {
	Lang         string
	Denylist     map[string]struct{} // lowercased unigrams AND multi-word bigrams
	ContextWords map[string]struct{} // lowercased preceding-context words
	WeakContext  map[string]struct{} // subset licensing only at distance 1
}

var (
	personGateMu    sync.Mutex
	personGateCache = map[string]*personGate{}
)

// personGateFor returns the compiled denylist + context set for a
// language, falling back to English on load failure. The english gate
// is the baseline for every language -- localised entries augment
// rather than replace.
func personGateFor(lang string) *personGate {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = "en"
	}
	personGateMu.Lock()
	defer personGateMu.Unlock()
	if g, ok := personGateCache[lang]; ok {
		return g
	}
	rules := resolveRules(lang)
	g := &personGate{
		Lang:         lang,
		Denylist:     make(map[string]struct{}, len(rules.PersonNameDenylist)*2),
		ContextWords: make(map[string]struct{}, len(rules.PersonNameContextWords)),
		WeakContext:  make(map[string]struct{}, len(rules.PersonNameWeakContextWords)),
	}
	for _, w := range rules.PersonNameWeakContextWords {
		g.WeakContext[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	for _, d := range rules.PersonNameDenylist {
		g.Denylist[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
	}
	for _, c := range rules.PersonNameContextWords {
		g.ContextWords[strings.ToLower(strings.TrimSpace(c))] = struct{}{}
	}
	// Always mix in the English seed so mixed-language memos pick up
	// "by Kevin" even when the memory's declared locale is German.
	if lang != "en" {
		enRules := resolveRules("en")
		for _, c := range enRules.PersonNameContextWords {
			g.ContextWords[strings.ToLower(strings.TrimSpace(c))] = struct{}{}
		}
		for _, w := range enRules.PersonNameWeakContextWords {
			g.WeakContext[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
		}
		for _, d := range enRules.PersonNameDenylist {
			g.Denylist[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
		}
	}
	personGateCache[lang] = g
	return g
}

// addPersonNames runs the classifier over content and emits
// `person:First Last` tags into out. Order of checks:
//
//  1. Punctuation gate: reject tokens with interior "." or "("
//     (Docker.Postgres, foo()).
//  2. Basic shape: uppercase initial, alpha-only, len > 1, not a
//     multi-lang stopword (covers "Of Course", "The Company",
//     "Clear Stats", "Open Get").
//  3. Denylist: reject known tech-term bigrams from the language's
//     person_name_denylist (Docker, Postgres, Next, CLI, MCP, ...).
//     Handles both unigram and joined-bigram forms.
//  4. Context gate: a bigram is accepted unconditionally only when both
//     tokens are Titlecase. If either carries interior uppercase --
//     ALL-CAPS ("SECURITY DEFINER") or CamelCase ("Native PostToolUse",
//     "VoyageEmbedder MistralEmbedder") -- it additionally needs a
//     licensing context word within personContextWindow preceding
//     tokens. See hasContextWordBefore.
//
// Rules 1-3 match Python extract_entities() within the corpus the
// multilang stopwords covers. Rule 4 is a deliberate Go-side
// divergence: it is the contextual-cue mitigation TBU-232 asks for,
// wired in ahead of the Python change (ogham-cli#26 finding 5).
//
// Effect on the parity corpus: entities shared-subset exact-match falls
// from 96.9% to 89.7% against a 75% floor. All 16 newly-dropped tags
// are junk -- class names, error types and product pairs such as
// person:EmbeddingCache EmbeddingCache, person:ValueError Embedding and
// person:Mistral Day. No real name in the corpus is lost. When Python
// lands the equivalent gate, regenerate testdata/parity/parity.json and
// the rate should climb back.
func addPersonNames(content, lang string, out map[string]struct{}) {
	gate := personGateFor(lang)
	words := strings.Fields(content)

	for i := 0; i < len(words)-1; i++ {
		rawW1 := words[i]
		rawW2 := words[i+1]

		// Rule 1: punct gate.
		if hasCodePunctImmediate(rawW1) || hasCodePunctImmediate(rawW2) {
			continue
		}

		w1 := stripPersonPunct(rawW1)
		w2 := stripPersonPunct(rawW2)

		// Rule 2: shape check (including multilang stopword filter).
		if !isLikelyPersonNamePart(w1) || !isLikelyPersonNamePart(w2) {
			continue
		}

		// Rule 3: denylist.
		l1 := strings.ToLower(w1)
		l2 := strings.ToLower(w2)
		if _, bad := gate.Denylist[l1]; bad {
			continue
		}
		if _, bad := gate.Denylist[l2]; bad {
			continue
		}
		if _, bad := gate.Denylist[l1+" "+l2]; bad {
			continue
		}

		// Rule 3b: an all-caps bigram is never a name -- acronym pair,
		// SQL/config keyword, or emphasis. Rejected on shape before the
		// context gate, so no cue word can license it (#33). This is the
		// gate that "SECURITY DEFINER" needed: Rule 4 alone let it back
		// in via "Grant EXECUTE to SECURITY DEFINER".
		if isAllCapsToken(w1) && isAllCapsToken(w2) {
			continue
		}

		// Rule 4: context gate for non-Titlecase bigrams.
		if !isTitlecaseNameToken(w1) || !isTitlecaseNameToken(w2) {
			if !hasLicensingCueBefore(words, i, gate) {
				continue
			}
		}

		out["person:"+w1+" "+w2] = struct{}{}
	}
}

// hasCodePunctImmediate reports whether the raw word contains "." or
// "(" as an interior character (not just trailing), indicating a code
// identifier rather than a sentence token. "Docker.Postgres" has the
// dot mid-word; "foo()" has the paren. Trailing "." (end of sentence)
// is NOT caught here -- stripPersonPunct handles that case.
func hasCodePunctImmediate(w string) bool {
	for i, r := range w {
		if r == '.' || r == '(' {
			// Skip sentence-terminal dot ("Burns." is fine).
			if i == len(w)-1 {
				continue
			}
			// Interior "." with a letter following is a code identifier.
			// Walk the UTF-8 prefix past the dot to inspect the first
			// real rune without a loop (RE2-free, byte-index safe).
			if r == '.' {
				if firstLetterAfter(w, i+1) {
					return true
				}
			}
			// Any "(" with content after it is code (foo(args)).
			if r == '(' {
				return true
			}
		}
	}
	return false
}

// firstLetterAfter reports whether the substring starting at byte
// offset i contains a letter as its first rune. Used by the code-
// identifier gate to distinguish "Docker.Postgres" (letter after dot)
// from "3.14" or "end. " (digit / space / end after dot).
func firstLetterAfter(w string, i int) bool {
	if i >= len(w) {
		return false
	}
	for _, r := range w[i:] {
		return unicode.IsLetter(r)
	}
	return false
}

// isTitlecaseNameToken reports whether w has the shape a person-name
// token actually takes: uppercase initial, no interior uppercase.
// "Kevin" and "Tanaka" qualify; "SECURITY", "PostToolUse" and "FastMCP"
// do not. Used by rule 4 to decide which bigrams need a licensing
// context word. Assumes isLikelyPersonNamePart already passed, so the
// token is non-empty and all letters.
func isTitlecaseNameToken(w string) bool {
	runes := []rune(w)
	if len(runes) == 0 {
		return false
	}
	if !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// isAllCapsToken reports whether w is two or more letters with no
// lowercase -- "SECURITY", "BUILD", "ROOT". Assumes
// isLikelyPersonNamePart already passed, so the token is all letters.
func isAllCapsToken(w string) bool {
	n := 0
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
		n++
	}
	return n > 1
}

// hasLicensingCueBefore is Rule 4's gate. Strong cues (verbs and role
// nouns -- met, said, wrote, author, cc) license anywhere in the
// personContextWindow. Weak cues (generic prepositions -- by, to, from,
// with) license only at distance 1.
//
// The split exists because the two behave nothing alike in prose. "met
// ... Hiroshi Tanaka" is an attribution wherever "met" sits in the
// window; "unset by default. THE BUILD BELONGS" is a preposition that
// happens to be nearby. Before #33 both counted equally, and the weak
// four supplied 85% of all cue matches on the parity corpus.
func hasLicensingCueBefore(words []string, idx int, gate *personGate) bool {
	start := idx - personContextWindow
	if start < 0 {
		start = 0
	}
	for j := start; j < idx; j++ {
		token := strings.ToLower(stripPersonPunct(words[j]))
		if _, ok := gate.ContextWords[token]; !ok {
			continue
		}
		if _, weak := gate.WeakContext[token]; weak && j != idx-1 {
			continue // generic preposition, too far away to be a byline
		}
		return true
	}
	return false
}

// hasContextWordBefore walks the personContextWindow tokens preceding
// idx and reports whether any is a context word in contextSet. Rule 4
// of addPersonNames calls this for bigrams that aren't plain Titlecase;
// it is also reachable directly by callers wanting a stricter gate.
func hasContextWordBefore(words []string, idx int, contextSet map[string]struct{}) bool {
	start := idx - personContextWindow
	if start < 0 {
		start = 0
	}
	for j := start; j < idx; j++ {
		token := strings.ToLower(stripPersonPunct(words[j]))
		if _, ok := contextSet[token]; ok {
			return true
		}
	}
	return false
}
