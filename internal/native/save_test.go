package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSave_CreatesFileWithSectionedTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &Config{
		Profile: "work",
		Database: Database{
			Backend:     "supabase",
			SupabaseURL: "https://x.supabase.co",
			SupabaseKey: "sb_secret_test",
		},
		Embedding: Embedding{
			Provider:  "gemini",
			APIKey:    "gm_test",
			Model:     "gemini-embedding-2-preview",
			Dimension: 512,
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, want := range []string{
		`profile = "work"`,
		`[database]`,
		`backend = "supabase"`,
		`supabase_url = "https://x.supabase.co"`,
		`[embedding]`,
		`provider = "gemini"`,
		`dimension = 512`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("TOML missing %q\n---full content---\n%s", want, s)
		}
	}
}

func TestSave_PreservesUnknownKeys(t *testing.T) {
	// A user whose TOML already has sidecar-mode api_key + gateway_url
	// (from the older init) must not lose those when native Save runs.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := `api_key = "ogham_live_abc"
gateway_url = "https://api.ogham-mcp.dev"
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Profile:  "work",
		Database: Database{Backend: "supabase", SupabaseURL: "https://x.supabase.co", SupabaseKey: "k"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	blob, _ := os.ReadFile(path)
	s := string(blob)
	if !strings.Contains(s, "ogham_live_abc") {
		t.Errorf("existing api_key dropped on save:\n%s", s)
	}
	if !strings.Contains(s, "api.ogham-mcp.dev") {
		t.Errorf("existing gateway_url dropped on save:\n%s", s)
	}
	if !strings.Contains(s, `profile = "work"`) {
		t.Errorf("new profile missing:\n%s", s)
	}
}

func TestSave_FilePerms0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Save(path, &Config{Profile: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %o, want 0600", info.Mode().Perm())
	}
}

func TestSaveEnvFile_WritesProviderSpecificKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	cfg := &Config{
		Profile:   "work",
		Database:  Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "sb"},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm_key", Dimension: 512},
	}
	if err := SaveEnvFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(path)
	s := string(blob)

	// Provider-specific key name is chosen on save.
	if !strings.Contains(s, "GEMINI_API_KEY=gm_key") {
		t.Errorf("missing GEMINI_API_KEY:\n%s", s)
	}
	// Unused provider keys must NOT appear.
	for _, k := range []string{"OPENAI_API_KEY", "VOYAGE_API_KEY", "MISTRAL_API_KEY"} {
		if strings.Contains(s, k+"=") {
			t.Errorf("unexpected key %s in env file:\n%s", k, s)
		}
	}
	// Core Python-side vars must appear.
	for _, want := range []string{
		"SUPABASE_URL=https://x.supabase.co",
		"SUPABASE_KEY=sb",
		"EMBEDDING_PROVIDER=gemini",
		"EMBEDDING_DIM=512",
		"DEFAULT_PROFILE=work",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in env file:\n%s", want, s)
		}
	}
}

func TestDeriveSidecarExtras(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			"supabase + gemini => gemini only",
			&Config{
				Database:  Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "k"},
				Embedding: Embedding{Provider: "gemini"},
			},
			"gemini",
		},
		{
			"postgres + voyage => postgres,voyage",
			&Config{
				Database:  Database{URL: "postgres://x"},
				Embedding: Embedding{Provider: "voyage"},
			},
			"postgres,voyage",
		},
		{
			"postgres + openai => postgres only (openai is base)",
			&Config{
				Database:  Database{URL: "postgres://x"},
				Embedding: Embedding{Provider: "openai"},
			},
			"postgres",
		},
		{
			"supabase + ollama => '' (both in base install)",
			&Config{
				Database:  Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "k"},
				Embedding: Embedding{Provider: "ollama"},
			},
			"",
		},
		{
			"explicit backend wins over URL inference",
			&Config{
				Database:  Database{Backend: "supabase", URL: "postgres://x", SupabaseURL: "https://y.supabase.co"},
				Embedding: Embedding{Provider: "gemini"},
			},
			"gemini",
		},
		{
			"nil config => ''",
			nil,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveSidecarExtras(tc.cfg); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveEnvFile_WritesOghamSidecarExtras(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	cfg := &Config{
		Profile:   "work",
		Database:  Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "sb"},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm", Dimension: 512},
	}
	if err := SaveEnvFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(path)
	if !strings.Contains(string(blob), "OGHAM_SIDECAR_EXTRAS=gemini") {
		t.Errorf("env file missing OGHAM_SIDECAR_EXTRAS=gemini:\n%s", blob)
	}
}

func TestSaveEnvFile_Deterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	cfg := &Config{
		Profile:   "work",
		Database:  Database{SupabaseURL: "https://x.supabase.co", SupabaseKey: "sb"},
		Embedding: Embedding{Provider: "gemini", APIKey: "gm", Dimension: 512},
	}
	_ = SaveEnvFile(path, cfg)
	first, _ := os.ReadFile(path)
	_ = SaveEnvFile(path, cfg)
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("SaveEnvFile output differs between runs (keys should be sorted)")
	}
}
