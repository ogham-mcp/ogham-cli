package shared

import (
	"regexp"
	"strings"
	"testing"
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
