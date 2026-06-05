package filters

import (
	"strings"
	"testing"
)

// BenchmarkClassify -- target: O(1), backed by map lookup. Should
// stay sub-microsecond per call so it never shows up in hook latency
// budget (council perf seat ceiling: 100ms total per hook event).
func BenchmarkClassify(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Classify("Bash")
		_ = Classify("Edit")
		_ = Classify("Read")
		_ = Classify("UnknownThing")
	}
}

// BenchmarkMaskSecretsSafeContent -- no matches, just walks the
// pre-compiled regexes. Lower bound for masking cost.
func BenchmarkMaskSecretsSafeContent(b *testing.B) {
	input := "git commit -m 'fix typo in handler'"
	for i := 0; i < b.N; i++ {
		_ = MaskSecrets(input)
	}
}

// BenchmarkMaskSecretsWithBareToken -- representative hot case.
func BenchmarkMaskSecretsWithBareToken(b *testing.B) {
	input := "leaked ghp_" + strings.Repeat("A", 36) + " in commit"
	for i := 0; i < b.N; i++ {
		_ = MaskSecrets(input)
	}
}

// BenchmarkMaskSecretsLongInput -- soft upper bound, 2KB string with
// one token at the tail. Mirrors the tool-response truncation cap in
// hooks.py (tool_response[:2000]).
func BenchmarkMaskSecretsLongInput(b *testing.B) {
	prefix := strings.Repeat("benign log line 0123456789 ", 70) // ~1.9KB
	input := prefix + " AKIA" + strings.Repeat("B", 16)
	for i := 0; i < b.N; i++ {
		_ = MaskSecrets(input)
	}
}

// BenchmarkIsDuplicateNew -- first-hit path (mostly map insert).
func BenchmarkIsDuplicateNew(b *testing.B) {
	d := NewDeduper()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.IsDuplicate("session", "Bash", string(rune(i%1024)))
	}
}
