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

// OLLAMA_URL must lift into cfg.Embedding.BaseURL during applyEnv so
// the embedder constructor can read a single source of truth.
func TestLoad_OllamaURLIntoBaseURL(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("OLLAMA_URL", "http://remote-ollama:11434")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.BaseURL != "http://remote-ollama:11434" {
		t.Errorf("BaseURL = %q, want lifted from OLLAMA_URL", cfg.Embedding.BaseURL)
	}
}

// OPENAI_BASE_URL must lift into cfg.Embedding.BaseURL only when the
// provider is openai. A stray value while provider=voyage / etc. must
// not propagate (prevents cross-provider URL pollution).
func TestLoad_OpenAIBaseURLIntoBaseURL(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-xyz")
	t.Setenv("OPENAI_BASE_URL", "https://azure.example.com/openai")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.BaseURL != "https://azure.example.com/openai" {
		t.Errorf("BaseURL = %q, want lifted from OPENAI_BASE_URL", cfg.Embedding.BaseURL)
	}
}

// OPENAI_BASE_URL set alongside a non-openai provider must NOT set
// BaseURL -- guards against a leftover Azure proxy env accidentally
// redirecting a Voyage or Mistral call.
func TestLoad_OpenAIBaseURLIgnoredForOtherProvider(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	t.Setenv("EMBEDDING_PROVIDER", "voyage")
	t.Setenv("VOYAGE_API_KEY", "vk-xyz")
	t.Setenv("OPENAI_BASE_URL", "https://azure.example.com/openai")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (OPENAI_BASE_URL should be ignored for voyage)", cfg.Embedding.BaseURL)
	}
}

// SidecarEnv must emit OPENAI_BASE_URL when provider is openai AND
// BaseURL is set. Must NOT emit it for non-openai providers.
func TestSidecarEnv_OpenAIBaseURL(t *testing.T) {
	cfg := &Config{
		Embedding: Embedding{
			Provider: "openai",
			APIKey:   "sk-xyz",
			BaseURL:  "https://azure.example.com/openai",
		},
	}
	got := map[string]string{}
	for _, kv := range cfg.SidecarEnv() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if got["OPENAI_BASE_URL"] != "https://azure.example.com/openai" {
		t.Errorf("OPENAI_BASE_URL not emitted: %+v", got)
	}

	// Non-openai provider with BaseURL set must not leak it as OPENAI_BASE_URL.
	cfg2 := &Config{
		Embedding: Embedding{
			Provider: "voyage",
			BaseURL:  "https://not-openai.example.com",
		},
	}
	got2 := map[string]string{}
	for _, kv := range cfg2.SidecarEnv() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got2[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if _, ok := got2["OPENAI_BASE_URL"]; ok {
		t.Errorf("OPENAI_BASE_URL should not be emitted for voyage provider: %+v", got2)
	}
}

// SidecarEnv must emit OLLAMA_URL when the provider is ollama and
// BaseURL is set so the Python sidecar sees the same endpoint. It
// must NOT emit OLLAMA_URL for non-ollama providers.
func TestSidecarEnv_OllamaURL(t *testing.T) {
	cfg := &Config{
		Embedding: Embedding{
			Provider: "ollama",
			BaseURL:  "http://remote-ollama:11434",
		},
	}
	got := map[string]string{}
	for _, kv := range cfg.SidecarEnv() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if got["OLLAMA_URL"] != "http://remote-ollama:11434" {
		t.Errorf("OLLAMA_URL not emitted: %+v", got)
	}

	// Non-ollama provider with a BaseURL set: must not leak as OLLAMA_URL.
	cfg2 := &Config{
		Embedding: Embedding{
			Provider: "openai",
			BaseURL:  "https://azure.example.com",
		},
	}
	got2 := map[string]string{}
	for _, kv := range cfg2.SidecarEnv() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				got2[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if _, ok := got2["OLLAMA_URL"]; ok {
		t.Errorf("OLLAMA_URL should not be emitted for non-ollama provider: %+v", got2)
	}
}

// EMBEDDING_DIM from env must override the TOML default. Parity with
// Python's pydantic-settings.
func TestLoad_EmbeddingDimFromEnv(t *testing.T) {
	isolateEnv(t)
	t.Setenv("EMBEDDING_DIM", "768")
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Dimension != 768 {
		t.Errorf("Dimension = %d, want 768 (from EMBEDDING_DIM env)", cfg.Embedding.Dimension)
	}
}

// EMBEDDING_DIM must override a value from config.toml too -- env wins
// over file for this knob, matching the other embedding env overrides.
func TestLoad_EmbeddingDimEnvOverridesToml(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
[embedding]
provider = "openai"
dimension = 512
`
	if err := os.WriteFile(path, []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBEDDING_DIM", "1024")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Dimension != 1024 {
		t.Errorf("Dimension = %d, want 1024 (env should win over TOML)", cfg.Embedding.Dimension)
	}
}

// Garbage EMBEDDING_DIM values fall through to the TOML/default rather
// than crashing on a bad integer parse.
func TestLoad_EmbeddingDimBadValueFallsThrough(t *testing.T) {
	isolateEnv(t)
	t.Setenv("EMBEDDING_DIM", "not-a-number")
	cfg, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Dimension != 512 {
		t.Errorf("Dimension = %d, want 512 (bad env should fall through to default)", cfg.Embedding.Dimension)
	}

	isolateEnv(t)
	t.Setenv("EMBEDDING_DIM", "-1")
	cfg2, err := Load("/nonexistent/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Embedding.Dimension != 512 {
		t.Errorf("negative EMBEDDING_DIM: Dimension = %d, want 512 (default)", cfg2.Embedding.Dimension)
	}
}
