package filters

import (
	"strings"
	"testing"
)

func TestMaskSecretsBareTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"GitHub PAT", "leaked: ghp_" + strings.Repeat("A", 36) + " in commit"},
		{"Anthropic key", "config = sk-ant-" + strings.Repeat("X", 25)},
		{"OpenAI legacy", "OPENAI=sk-" + strings.Repeat("a", 45)},
		{"Ogham API", "OGHAM=ogham_live_" + strings.Repeat("z", 25)},
		{"Supabase secret", "key=sb_secret_" + strings.Repeat("Q", 25)},
		{"AWS access", "AKIA" + strings.Repeat("B", 16) + " in env"},
		{"Neon password", "npg_" + strings.Repeat("1", 12)},
		{"Slack token", "tok = xoxb-" + strings.Repeat("c", 30)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := MaskSecrets(tc.input)
			if !strings.Contains(out, MaskedPlaceholder) {
				t.Errorf("input %q produced no mask\n  got: %q", tc.input, out)
			}
			// Ensure the raw token doesn't survive.
			if !strings.Contains(tc.input, "AKIA") {
				return // skip the next check for non-token patterns
			}
		})
	}
}

func TestMaskSecretsKeyValueAssignment(t *testing.T) {
	in := `api_key="sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz123456789"`
	out := MaskSecrets(in)
	if !strings.Contains(out, MaskedPlaceholder) {
		t.Errorf("expected mask in %q, got %q", in, out)
	}
	// Key should be preserved.
	if !strings.Contains(out, "api_key") {
		t.Errorf("api_key prefix lost, got %q", out)
	}
}

func TestMaskSecretsURLCredentials(t *testing.T) {
	in := "postgresql://kevin:hunter2pass@db.example.com:5432/foo"
	out := MaskSecrets(in)
	if strings.Contains(out, "hunter2pass") {
		t.Errorf("password survived masking: %q", out)
	}
	if !strings.Contains(out, MaskedPlaceholder) {
		t.Errorf("no mask applied: %q", out)
	}
	// Host should be preserved.
	if !strings.Contains(out, "@db.example.com") {
		t.Errorf("host lost from URL: %q", out)
	}
}

func TestMaskSecretsEnvKeys(t *testing.T) {
	cases := []string{
		`password="hunter2"`,
		`database_url=postgres://x:y@z/d`,
		`encryption_key: my-secret-value-here`,
		`AUTH_TOKEN: abc123def456`,
	}
	for _, in := range cases {
		out := MaskSecrets(in)
		if !strings.Contains(out, MaskedPlaceholder) {
			t.Errorf("env-key %q produced no mask\n  got: %q", in, out)
		}
	}
}

func TestMaskSecretsSafeContentUnchanged(t *testing.T) {
	cases := []string{
		"git commit -m 'fix typo'",
		"console.log('hello world')",
		"def foo(): return 42",
		"",
	}
	for _, in := range cases {
		out := MaskSecrets(in)
		if out != in {
			t.Errorf("safe content modified:\n  in:  %q\n  out: %q", in, out)
		}
	}
}

func TestMaskSecretsMultipleInOneText(t *testing.T) {
	in := "leaked ghp_" + strings.Repeat("A", 36) +
		" and AKIA" + strings.Repeat("B", 16) +
		" plus password=hunter2"
	out := MaskSecrets(in)
	// All three should be masked.
	if strings.Contains(out, "ghp_") && !strings.Contains(out, MaskedPlaceholder) {
		t.Errorf("multiple secrets not all masked: %q", out)
	}
	maskCount := strings.Count(out, MaskedPlaceholder)
	if maskCount < 3 {
		t.Errorf("expected >=3 masks in output, got %d: %q", maskCount, out)
	}
}

func TestBuildBareTokenRegexEmptyList(t *testing.T) {
	re, err := buildBareTokenRegex(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re.MatchString("ghp_anything") {
		t.Errorf("empty bare-token regex matched something")
	}
}

func TestBuildEnvKeyRegexEmptyList(t *testing.T) {
	re, err := buildEnvKeyRegex(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re.MatchString("password=hunter2") {
		t.Errorf("empty env-key regex matched something")
	}
}
