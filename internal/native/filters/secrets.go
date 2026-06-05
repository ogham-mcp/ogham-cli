package filters

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ogham-mcp/ogham-cli/internal/native/shared"
)

// MaskedPlaceholder is the literal string substituted for any
// matched secret. Mirrors src/ogham/hooks.py.
const MaskedPlaceholder = "***MASKED***"

// keyValueAssignmentRegex matches KEY[separator]VALUE for the curated
// list of secret-key prefixes from hooks.py. Group 1 is the value;
// MaskSecrets replaces only that group so the key stays readable.
//
// Mirrors src/ogham/hooks.py _SECRET_PATTERNS verbatim.
var keyValueAssignmentRegex = regexp.MustCompile(
	`(?i)` +
		// Generic key=value patterns
		`(?:api[_-]?key|secret[_-]?key|access[_-]?key|access[_-]?token|auth[_-]?token` +
		`|password|passwd|bearer|token` +
		// Cloud provider prefixes
		`|sk[_-]live|sk[_-]proj|pk[_-]live|sk[_-]test|pk[_-]test` +
		// Service-specific prefixes
		`|ghp_|gho_|github_pat_|glpat-|xoxb-|xoxp-|whsec_` +
		`|sb_secret_|ogham_live_` +
		`|SG\.[A-Za-z0-9_-]{20}` + // SendGrid
		`|npm_[A-Za-z0-9]{20}` + // NPM
		`|pypi-[A-Za-z0-9]{20}` + // PyPI
		// Voyage/Neon/custom
		`|pa-[A-Za-z0-9_-]{20}` +
		`|npg_[A-Za-z0-9]{10}` +
		// AWS
		`|AKIA[A-Z0-9]{16}` +
		// JWT
		`|eyJ[A-Za-z0-9_-]{20,})` +
		`[=:\s]+\s*['"]?([A-Za-z0-9_\-./+=]{8,})['"]?`,
)

// urlCredentialsRegex matches basic auth in URLs (://user:pass@host).
// Mirrors src/ogham/hooks.py _URL_CREDENTIALS.
var urlCredentialsRegex = regexp.MustCompile(`://([^:]+):([^@]{3,})@`)

// buildBareTokenRegex joins every pattern in secrets.bare_tokens
// into a single OR-alternated expression, wrapped in non-capturing
// groups so the combined regex compiles in RE2.
func buildBareTokenRegex(tokens []shared.BareToken) (*regexp.Regexp, error) {
	if len(tokens) == 0 {
		return regexp.MustCompile(`$.^`), nil // matches nothing
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = "(?:" + t.Pattern + ")"
	}
	combined := strings.Join(parts, "|")
	re, err := regexp.Compile(combined)
	if err != nil {
		return nil, fmt.Errorf("compile bare-token regex: %w", err)
	}
	return re, nil
}

// buildEnvKeyRegex constructs a case-insensitive KEY[=:]VALUE matcher
// for the list of generic env-var names in secrets.env_keys. The
// captured group is VALUE.
func buildEnvKeyRegex(keys []string) (*regexp.Regexp, error) {
	if len(keys) == 0 {
		return regexp.MustCompile(`$.^`), nil
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = regexp.QuoteMeta(k)
	}
	combined := `(?i)(?:` + strings.Join(parts, "|") + `)\s*[=:]\s*['"]?([^\s'"]+)['"]?`
	re, err := regexp.Compile(combined)
	if err != nil {
		return nil, fmt.Errorf("compile env-key regex: %w", err)
	}
	return re, nil
}

// MaskSecrets replaces anything that looks like a secret in text
// with MaskedPlaceholder, preserving the surrounding key/url
// scaffolding so the resulting memory still says "set API key for X"
// without leaking the value.
//
// Four layers, applied in order:
//  1. Bare tokens (ghp_, AKIA, sk-ant-, ogham_live_, ...) -- whole match
//     replaced.
//  2. KEY=value for curated service prefixes -- value (group 1)
//     replaced, key preserved.
//  3. URL credentials (://user:pass@host) -- both user + pass replaced.
//  4. Generic env-key=value (api_key=, password=, database_url=, ...)
//     -- value replaced.
//
// Mirrors src/ogham/hooks.py _mask_secrets.
func MaskSecrets(text string) string {
	if text == "" {
		return text
	}
	// Layer 1: bare tokens (no KEY= prefix).
	masked := config.bareTokens.ReplaceAllString(text, MaskedPlaceholder)
	// Layer 2: KEY=value for service prefixes -- preserve key.
	masked = replaceCaptureGroup(config.keyValueAssignment, masked, 1, MaskedPlaceholder)
	// Layer 3: URL credentials.
	masked = config.urlCredentials.ReplaceAllString(
		masked,
		"://"+MaskedPlaceholder+":"+MaskedPlaceholder+"@",
	)
	// Layer 4: generic env-key=value.
	masked = replaceCaptureGroup(config.envKeyPattern, masked, 1, MaskedPlaceholder)
	return masked
}

// replaceCaptureGroup walks every match of re in text and substitutes
// the n-th capture group with replacement, leaving the rest of the
// match (typically the key + separator) intact. Equivalent to
// Python's re.sub with a lambda that slices m.group(0) on group(n).
func replaceCaptureGroup(re *regexp.Regexp, text string, n int, replacement string) string {
	return re.ReplaceAllStringFunc(text, func(match string) string {
		idx := re.FindStringSubmatchIndex(match)
		if idx == nil || len(idx) < 2*(n+1) {
			return match
		}
		start := idx[2*n] // submatch n start, relative to original text
		end := idx[2*n+1]
		// FindStringSubmatchIndex on a single match still returns offsets
		// into that match string, so we can slice match directly.
		if start < 0 || end < 0 || start > len(match) || end > len(match) {
			return match
		}
		return match[:start] + replacement + match[end:]
	})
}
