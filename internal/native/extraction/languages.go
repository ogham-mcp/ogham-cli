package extraction

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed languages/*.yaml
var languageFS embed.FS

// LanguageRules holds every word list + keyword map a language file
// ships. The Python YAML keys map 1:1 onto field tags so parity stays
// honest: when the Python YAML adds a new section, the Go struct gets
// a compile error until we add the field.
//
// Words are NOT lowercased at load time on purpose -- some languages
// have case-significant stopwords (German nouns, for example). Callers
// that want case-insensitive lookup should fold to lowercase at the
// lookup site. Use the Set() helper on each list to get a
// map[string]struct{} for O(1) lookups.
type LanguageRules struct {
	// Named-day lookup: "monday" -> 1 etc. 0-indexed, Sunday=0 matching
	// Python's datetime.weekday() convention shifted by one. Preserve
	// the exact mapping or recurrence detection drifts across languages.
	DayNames map[string]int `yaml:"day_names"`

	// Keywords that signal a recurring event (every, each, weekly, ...).
	EveryWords []string `yaml:"every_words"`

	// Low-recall temporal markers -- when, date, time, ago, last, ...
	TemporalKeywords []string `yaml:"temporal_keywords"`

	// Direction markers, split by direction (after / before / around).
	// The YAML file nests: direction_words: { after: [...], before: [...] }
	DirectionWords map[string][]string `yaml:"direction_words"`

	// Scoring signal classes. importance += 0.3 if any decision word
	// matches, +0.2 for error, +0.2 for architecture, etc.
	DecisionWords            []string `yaml:"decision_words"`
	ErrorWords               []string `yaml:"error_words"`
	ArchitectureWords        []string `yaml:"architecture_words"`
	EventWords               []string `yaml:"event_words"`
	ActivityWords            []string `yaml:"activity_words"`
	EmotionWords             []string `yaml:"emotion_words"`
	RelationshipWords        []string `yaml:"relationship_words"`
	PossessiveTriggers       []string `yaml:"possessive_triggers"`
	QuantityUnits            []string `yaml:"quantity_units"`
	PreferenceWords          []string `yaml:"preference_words"`
	NegationMarkers          []string `yaml:"negation_markers"`
	CompressionDecisionWords []string `yaml:"compression_decision_words"`

	// Month name -> month number (1-12). Parse-assist for "15 March" etc.
	MonthNames map[string]int `yaml:"month_names"`

	// Numeric spelling: "one" -> 1, "fifty" -> 50. Needed for
	// "in two weeks" style relative-date parsing.
	WordNumbers map[string]int `yaml:"word_numbers"`

	// QueryFiller holds low-information words we strip from queries
	// before hitting the FTS index ("how do I X?" -> "X").
	QueryFiller []string `yaml:"query_filler"`

	// QueryHints is a nested map: { multi_hop: [...], ordering: [...],
	// summary: [...] }. Each bucket signals a different intent-detection
	// gate; the keys are stable across languages but the values are
	// localised.
	QueryHints map[string][]string `yaml:"query_hints"`

	// --- Date anchors + modifiers (v0.7) --------------------------------
	// Today / tomorrow / yesterday equivalents. Single-token phrases --
	// multi-word anchors like "the day after tomorrow" are out of scope
	// (handled by parsedatetime on the Python side, not ported).
	TodayWords     []string `yaml:"today_words"`
	TomorrowWords  []string `yaml:"tomorrow_words"`
	YesterdayWords []string `yaml:"yesterday_words"`

	// Modifiers for "last/next/this <weekday|period>". English has one
	// word per bucket; German has several (letzten/letzte/letzter).
	ModifierLast []string `yaml:"modifier_last"`
	ModifierNext []string `yaml:"modifier_next"`
	ModifierThis []string `yaml:"modifier_this"`

	// Periods: "week" / "month" / "year" equivalents.
	PeriodWeek  []string `yaml:"period_week"`
	PeriodMonth []string `yaml:"period_month"`
	PeriodYear  []string `yaml:"period_year"`

	// Units for "N <unit> ago" / "in N <unit>". English includes both
	// singular + plural forms ("day", "days"); German inflects
	// differently so YAML spells out every surface form.
	UnitDay   []string `yaml:"unit_day"`
	UnitWeek  []string `yaml:"unit_week"`
	UnitMonth []string `yaml:"unit_month"`
	UnitYear  []string `yaml:"unit_year"`

	// Direction markers: "ago" = past, "in" = future.
	AgoMarkers []string `yaml:"ago_markers"`
	InMarkers  []string `yaml:"in_markers"`

	// --- Recurrence (v0.7) ---------------------------------------------
	// Additional recurrence anchor phrases beyond EveryWords+DayNames.
	// Each entry: { pattern: regex, normalised: weekly|daily|... }.
	// Regex is anchored at word boundaries by the recurrence matcher.
	RecurrencePatterns []RecurrencePattern `yaml:"recurrence_patterns"`

	// --- Person-name regex tightening (v0.7) ---------------------------
	// PersonNameDenylist is the set of Capitalised bigrams / unigrams
	// that SHOULD NOT be tagged as person: even if they pass the basic
	// shape check. Case-insensitive match at lookup site.
	PersonNameDenylist []string `yaml:"person_name_denylist"`

	// PersonNameContextWords is the set of preceding-context tokens that
	// license a standalone Capitalised bigram (e.g. "by Kevin Burns",
	// "from John Doe"). Without a matching context word in the 3-token
	// window before the candidate, the candidate is rejected. English-
	// seed: by/from/user/person/with/met/said/told/asked.
	PersonNameContextWords []string `yaml:"person_name_context_words"`
}

// RecurrencePattern is one entry in a language's recurrence_patterns
// block. The regex is compiled once per pattern and cached inside the
// recurrence package-level cache. normalised is the canonical form
// exposed as metadata.recurrence + a recurrence:<normalised> tag.
type RecurrencePattern struct {
	Pattern    string `yaml:"pattern"`
	Normalised string `yaml:"normalised"`
}

// Set returns a string set (map[string]struct{}) for O(1) membership
// checks. Uses the input slice as-is -- caller handles case folding if
// needed. Safe on a nil slice (returns an empty set).
func Set(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, w := range s {
		out[w] = struct{}{}
	}
	return out
}

// SetLower is the common case: case-fold to lowercase + deduplicate.
// Use Set() when case-sensitivity matters (German noun stopwords, etc.).
func SetLower(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, w := range s {
		out[strings.ToLower(w)] = struct{}{}
	}
	return out
}

// -----------------------------------------------------------------------
// Registry + loader

var (
	registryOnce sync.Once
	registry     map[string]*LanguageRules
	registryErr  error
)

// bootRegistry parses every YAML in the embedded FS exactly once. Any
// parse failure bubbles up to the first caller and becomes sticky --
// a bad YAML is never hidden by a fallback.
func bootRegistry() {
	entries, err := languageFS.ReadDir("languages")
	if err != nil {
		registryErr = fmt.Errorf("languages: read embedded dir: %w", err)
		return
	}
	reg := make(map[string]*LanguageRules, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		code := strings.TrimSuffix(name, ".yaml")
		raw, err := languageFS.ReadFile("languages/" + name)
		if err != nil {
			registryErr = fmt.Errorf("languages: read %s: %w", name, err)
			return
		}
		var rules LanguageRules
		if err := yaml.Unmarshal(raw, &rules); err != nil {
			registryErr = fmt.Errorf("languages: parse %s: %w", name, err)
			return
		}
		reg[code] = &rules
	}
	registry = reg
}

// LoadLanguage returns the parsed rules for a 2-letter code (or
// "pt-br" for regional variants). Returns a nil-safe result + ErrLanguageNotFound
// if the code isn't in the embedded set -- callers can fall back to
// English rather than crashing.
//
// The registry is built once per process and memoised. Subsequent calls
// are a map lookup; cheap enough to call on every Importance() invocation.
func LoadLanguage(code string) (*LanguageRules, error) {
	registryOnce.Do(bootRegistry)
	if registryErr != nil {
		return nil, registryErr
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = "en"
	}
	if rules, ok := registry[code]; ok {
		return rules, nil
	}
	return nil, fmt.Errorf("%w: %q (available: %s)",
		ErrLanguageNotFound, code, strings.Join(ListLanguages(), ", "))
}

// ListLanguages returns the sorted list of available 2-letter codes.
// Useful for CLI flag validation + error messages.
func ListLanguages() []string {
	registryOnce.Do(bootRegistry)
	if registryErr != nil {
		return nil
	}
	out := make([]string, 0, len(registry))
	for code := range registry {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// ErrLanguageNotFound is the sentinel error LoadLanguage returns when
// a code isn't in the embedded set. Exported so callers can errors.Is()
// against it rather than string-matching.
var ErrLanguageNotFound = errLanguageNotFound{}

type errLanguageNotFound struct{}

func (errLanguageNotFound) Error() string { return "language not found" }
