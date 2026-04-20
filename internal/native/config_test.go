package native

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateEnv shields a test from the developer's real ~/.ogham/config.env
// and any .env in the current directory, which would otherwise leak via
// mergeEnvFilesIntoProcess in Load. Sets HOME to a temp dir, changes
// cwd to a temp dir, and clears the env vars applyEnv reads.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	prev, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	for _, k := range []string{
		"DATABASE_URL", "OGHAM_DATABASE_URL", "DATABASE_BACKEND",
		"SUPABASE_URL", "SUPABASE_KEY", "SUPABASE_SECRET_KEY", "SUPABASE_SERVICE_ROLE_KEY",
		"EMBEDDING_PROVIDER", "EMBEDDING_MODEL", "EMBEDDING_DIM",
		"OPENAI_API_KEY", "VOYAGE_API_KEY", "GEMINI_API_KEY", "MISTRAL_API_KEY",
		"OGHAM_PROFILE", "DEFAULT_PROFILE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	isolateEnv(t)
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if cfg.Profile != "default" {
		t.Errorf("default profile = %q, want %q", cfg.Profile, "default")
	}
	if cfg.Embedding.Dimension != 512 {
		t.Errorf("default dimension = %d, want 512", cfg.Embedding.Dimension)
	}
}

func TestLoad_FromFile(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
profile = "work"
[database]
url = "postgres://u:p@localhost/ogham"
[embedding]
provider = "voyage"
api_key = "voy_abc"
dimension = 512
`
	if err := os.WriteFile(path, []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "work" {
		t.Errorf("profile = %q, want work", cfg.Profile)
	}
	if cfg.Database.URL != "postgres://u:p@localhost/ogham" {
		t.Errorf("db url = %q", cfg.Database.URL)
	}
	if cfg.Embedding.Provider != "voyage" || cfg.Embedding.APIKey != "voy_abc" {
		t.Errorf("embedding = %+v", cfg.Embedding)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
profile = "work"
[database]
url = "postgres://from-file"
[embedding]
provider = "voyage"
api_key = "file-key"
`
	if err := os.WriteFile(path, []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DATABASE_URL", "postgres://from-env")
	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-env")
	t.Setenv("OGHAM_PROFILE", "test")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://from-env" {
		t.Errorf("DATABASE_URL should win over file, got %q", cfg.Database.URL)
	}
	if cfg.Embedding.Provider != "openai" {
		t.Errorf("EMBEDDING_PROVIDER should win over file, got %q", cfg.Embedding.Provider)
	}
	if cfg.Embedding.APIKey != "sk-env" {
		t.Errorf("OPENAI_API_KEY should have applied, got %q", cfg.Embedding.APIKey)
	}
	if cfg.Profile != "test" {
		t.Errorf("OGHAM_PROFILE should win over file, got %q", cfg.Profile)
	}
}

func TestSidecarEnv(t *testing.T) {
	cfg := &Config{
		Profile:  "work",
		Database: Database{URL: "postgres://x"},
		Embedding: Embedding{
			Provider:  "voyage",
			APIKey:    "voy_abc",
			Model:     "voyage-3-lite",
			Dimension: 512,
		},
	}
	env := cfg.SidecarEnv()
	want := map[string]string{
		"DATABASE_URL":       "postgres://x",
		"EMBEDDING_PROVIDER": "voyage",
		"EMBEDDING_MODEL":    "voyage-3-lite",
		"EMBEDDING_DIM":      "512",
		// Python's pydantic-settings reads DEFAULT_PROFILE; the Go side
		// also emits OGHAM_PROFILE as a courtesy to nested Go calls.
		"DEFAULT_PROFILE": "work",
		"OGHAM_PROFILE":   "work",
		"VOYAGE_API_KEY":  "voy_abc",
	}
	got := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("SidecarEnv[%s] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["OPENAI_API_KEY"]; ok {
		t.Errorf("SidecarEnv should not emit OPENAI_API_KEY for voyage provider: %+v", got)
	}
}
