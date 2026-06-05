package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// Manifest of expected SHA-256 hashes for the shared-data-v0.1.0
// tag. Bump alongside any new git subtree pull. A mismatch means the
// vendored copy was edited locally instead of upstream; revert and
// re-pull from the latest shared-data-vX.Y.Z tag.
const (
	expectedSHA256_hooksConfig = "40eecf6de03c8659bc4dd3f5225b448db8d0127d0528ca3ad526fee92829da14"
	expectedSHA256_schema      = "bd1aa1bec8f5e981041d673daf4fc3f16fa1c19251b6d0710cde61926a4aa0a9"
)

func TestLoadHooksConfigSmoke(t *testing.T) {
	cfg, err := LoadHooksConfig()
	if err != nil {
		t.Fatalf("LoadHooksConfig: %v", err)
	}
	if len(cfg.Signals.Errors) == 0 {
		t.Errorf("expected signals.errors to be populated, got 0 entries")
	}
	if len(cfg.NoiseCommands) == 0 {
		t.Errorf("expected noise_commands to be populated")
	}
	if len(cfg.Secrets.BareTokens) < 40 {
		t.Errorf("expected >=40 bare-token patterns, got %d", len(cfg.Secrets.BareTokens))
	}
	if len(cfg.Secrets.EnvKeys) == 0 {
		t.Errorf("expected secrets.env_keys to be populated")
	}
}

func TestLoadSchemaSmoke(t *testing.T) {
	s, err := LoadSchema()
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if s.Version == "" {
		t.Errorf("schema version missing")
	}
	if s.RegexDialect != "re2" {
		t.Errorf("regex_dialect = %q, want %q", s.RegexDialect, "re2")
	}
}

func TestSecretPatternsCompileAsRE2(t *testing.T) {
	cfg, err := LoadHooksConfig()
	if err != nil {
		t.Fatalf("LoadHooksConfig: %v", err)
	}
	for _, tok := range cfg.Secrets.BareTokens {
		if _, err := regexp.Compile(tok.Pattern); err != nil {
			t.Errorf("RE2 reject for %q: pattern %q -- %v", tok.Name, tok.Pattern, err)
		}
	}
}

func TestSHA256Parity(t *testing.T) {
	cases := []struct {
		name     string
		got      []byte
		expected string
	}{
		{"hooks_config.yaml", RawHooksConfig(), expectedSHA256_hooksConfig},
		{"schema.yaml", RawSchema(), expectedSHA256_schema},
	}
	for _, tc := range cases {
		sum := sha256.Sum256(tc.got)
		actual := hex.EncodeToString(sum[:])
		if actual != tc.expected {
			t.Errorf("%s SHA-256 drift\n  expected: %s\n  actual:   %s\n"+
				"  Vendored copy was edited locally. Revert and re-run "+
				"`git subtree pull --prefix=internal/native/shared "+
				"<public-repo> shared-data-vX.Y.Z --squash` against the "+
				"current shared-data tag, then bump the expected hash here.",
				tc.name, tc.expected, actual)
		}
	}
}

func TestKnownTokensDetected(t *testing.T) {
	cfg, err := LoadHooksConfig()
	if err != nil {
		t.Fatalf("LoadHooksConfig: %v", err)
	}
	patterns := make([]string, len(cfg.Secrets.BareTokens))
	for i, tok := range cfg.Secrets.BareTokens {
		patterns[i] = "(?:" + tok.Pattern + ")"
	}
	combined := regexp.MustCompile(strings.Join(patterns, "|"))

	cases := []struct {
		name   string
		sample string
	}{
		{"GitHub PAT", "ghp_" + strings.Repeat("A", 36)},
		{"Anthropic key", "sk-ant-" + strings.Repeat("A", 25)},
		{"Ogham API key", "ogham_live_" + strings.Repeat("a", 25)},
		{"Supabase secret", "sb_secret_" + strings.Repeat("X", 25)},
	}
	for _, tc := range cases {
		if !combined.MatchString(tc.sample) {
			t.Errorf("%s: combined pattern did not match %q", tc.name, tc.sample)
		}
	}
}
