// Package native holds the Go-side implementations of Ogham tools for the
// absorption path described in docs/plans/2026-04-16-go-cli-enterprise.md.
// A subcommand is "native" when it bypasses the Python sidecar and reads/
// writes the database directly via pgx.
//
// The default path for every subcommand is still the sidecar. Native is
// opt-in via --native until each tool has been dogfooded enough.
package native

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the native-mode view of ~/.ogham/config.toml. It deliberately
// does not re-declare fields from internal/config.Config -- sidecar-mode
// commands still go through that loader. Native-mode adds the extra keys
// a Go tool needs to talk to Postgres and an embedding provider directly.
type Config struct {
	Database  Database  `toml:"database"`
	Embedding Embedding `toml:"embedding"`
	Profile   string    `toml:"profile"`
}

type Database struct {
	// Backend is "postgres" (pgx direct) or "supabase" (PostgREST).
	// Auto-detected when empty: SUPABASE_URL => supabase, DATABASE_URL => postgres.
	Backend string `toml:"backend"`

	// URL is the Postgres connection string when Backend == "postgres".
	URL string `toml:"url"`

	// SupabaseURL is the project base URL (https://xxx.supabase.co) when
	// Backend == "supabase". REST endpoint derives as {URL}/rest/v1.
	SupabaseURL string `toml:"supabase_url"`
	SupabaseKey string `toml:"supabase_key"`
}

// ResolveBackend picks the backend with this precedence:
//  1. explicit cfg.Database.Backend if set
//  2. SupabaseURL populated => supabase
//  3. URL populated         => postgres
//  4. error asking the user to configure one
func (c *Config) ResolveBackend() (string, error) {
	if c.Database.Backend != "" {
		return c.Database.Backend, nil
	}
	if c.Database.SupabaseURL != "" && c.Database.SupabaseKey != "" {
		return "supabase", nil
	}
	if c.Database.URL != "" {
		return "postgres", nil
	}
	return "", fmt.Errorf("no database configured: set SUPABASE_URL+SUPABASE_KEY (Supabase) or DATABASE_URL (Postgres) in your .env or config.toml")
}

type Embedding struct {
	Provider  string `toml:"provider"`
	APIKey    string `toml:"api_key"`
	Model     string `toml:"model"`
	Dimension int    `toml:"dimension"`

	// BaseURL lets providers that can be self-hosted (Ollama today;
	// OpenAI via Azure / LocalAI proxies in v0.5+) point at a custom
	// endpoint instead of the provider default. Empty string means
	// "use provider default". Populated from OLLAMA_URL today; v0.5
	// embedders will add OPENAI_BASE_URL / MISTRAL_BASE_URL as they land.
	BaseURL string `toml:"base_url"`
}

// DefaultPath returns the standard config location. Same file as the
// sidecar-mode config -- just reads different sections.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ogham", "config.toml")
}

// Load reads TOML then layers env vars on top. Before reading env vars
// it auto-loads any dotenv files found at the standard locations
// (~/.ogham/config.env and ./.env) -- same search order Python uses,
// same behaviour the sidecar path already has via connectSidecar. A user
// whose Python sidecar works today will have a working native path
// without any extra configuration.
//
// Env precedence (lowest to highest): process env, ~/.ogham/config.env,
// project ./.env, then TOML overrides.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Profile:   "default",
		Embedding: Embedding{Dimension: 512},
	}

	// Merge env-file entries into the current process env so applyEnv
	// sees them alongside whatever the user set in their shell. Process
	// env always wins over dotenv (set-if-absent semantics).
	mergeEnvFilesIntoProcess()

	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse native config %s: %w", path, err)
		}
	}

	applyEnv(cfg)

	// Layer the sentinel file on top of env + TOML so MCP `switch_profile`
	// and CLI `ogham profile switch` persist across restarts without
	// mutating config.toml (which stays as the user's declared baseline).
	// ActiveProfile enforces env > sentinel > cfg.Profile > "default".
	cfg.Profile = ActiveProfile(cfg)
	return cfg, nil
}

// mergeEnvFilesIntoProcess sets env vars from the dotenv search path
// without overwriting anything already in os.Environ(). Precedence
// (highest to lowest): shell env > project ./.env > ~/.ogham/config.env.
//
// LoadEnvFiles returns the slice in append-order for exec (global first,
// project last, later wins). For set-if-absent we need the opposite --
// project must try to set first so it wins the merge when the shell env
// doesn't already have the key.
func mergeEnvFilesIntoProcess() {
	entries := LoadEnvFiles()
	for i := len(entries) - 1; i >= 0; i-- {
		kv := entries[i]
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		if _, present := os.LookupEnv(k); present {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

// applyEnv mirrors Python ogham's env var names so both readers agree.
func applyEnv(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("DATABASE_BACKEND")); v != "" {
		cfg.Database.Backend = v
	}
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		cfg.Database.URL = v
	}
	if v := strings.TrimSpace(os.Getenv("OGHAM_DATABASE_URL")); v != "" {
		cfg.Database.URL = v
	}
	if v := strings.TrimSpace(os.Getenv("SUPABASE_URL")); v != "" {
		cfg.Database.SupabaseURL = v
	}
	// Supabase has rotated key naming over the years: the current secret
	// prefix is sb_secret_, the old name was service_role_key. Python
	// reads both via SUPABASE_KEY. Honour the same precedence.
	for _, name := range []string{"SUPABASE_KEY", "SUPABASE_SECRET_KEY", "SUPABASE_SERVICE_ROLE_KEY"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			cfg.Database.SupabaseKey = v
			break
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_PROVIDER")); v != "" {
		cfg.Embedding.Provider = v
	}
	// EMBEDDING_DIM: Python's pydantic-settings reads this; Go should too.
	// Parity gap that previously required editing config.toml to change the
	// native pipeline's dimension. Silent bad parses fall through to the
	// TOML / default value rather than crashing.
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_DIM")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Embedding.Dimension = n
		}
	}
	// Provider-scoped base-URL overrides. Each provider reads the env
	// name it has historically used; only the one matching the selected
	// provider wins so a stray OPENAI_BASE_URL doesn't accidentally
	// redirect an Ollama client.
	if v := strings.TrimSpace(os.Getenv("OLLAMA_URL")); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); v != "" && cfg.Embedding.Provider == "openai" {
		cfg.Embedding.BaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("VOYAGE_BASE_URL")); v != "" && cfg.Embedding.Provider == "voyage" {
		cfg.Embedding.BaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("MISTRAL_BASE_URL")); v != "" && cfg.Embedding.Provider == "mistral" {
		cfg.Embedding.BaseURL = v
	}
	// Profile: honour both the Python name (DEFAULT_PROFILE) and the Go
	// name (OGHAM_PROFILE). OGHAM_PROFILE wins if both are set.
	if v := strings.TrimSpace(os.Getenv("DEFAULT_PROFILE")); v != "" {
		cfg.Profile = v
	}
	if v := strings.TrimSpace(os.Getenv("OGHAM_PROFILE")); v != "" {
		cfg.Profile = v
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" && cfg.Embedding.Provider == "openai" {
		cfg.Embedding.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("VOYAGE_API_KEY")); v != "" && cfg.Embedding.Provider == "voyage" {
		cfg.Embedding.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); v != "" && cfg.Embedding.Provider == "gemini" {
		cfg.Embedding.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")); v != "" && cfg.Embedding.Provider == "mistral" {
		cfg.Embedding.APIKey = v
	}
}

// SidecarEnv returns the env vars a spawned Python sidecar needs. Lets the
// Go CLI treat TOML as canonical and push values into the subprocess
// environment so Python sees them as it always has.
func (c *Config) SidecarEnv() []string {
	var env []string
	if c.Database.URL != "" {
		env = append(env, "DATABASE_URL="+c.Database.URL)
	}
	if c.Embedding.Provider != "" {
		env = append(env, "EMBEDDING_PROVIDER="+c.Embedding.Provider)
	}
	if c.Embedding.Model != "" {
		env = append(env, "EMBEDDING_MODEL="+c.Embedding.Model)
	}
	if c.Embedding.Dimension > 0 {
		env = append(env, fmt.Sprintf("EMBEDDING_DIM=%d", c.Embedding.Dimension))
	}
	// Provider-scoped base-URL: emit the env name the target provider
	// reads. Only the provider currently selected gets its URL passed
	// so a leftover BaseURL from a previous provider never leaks.
	if c.Embedding.BaseURL != "" {
		switch c.Embedding.Provider {
		case "ollama":
			env = append(env, "OLLAMA_URL="+c.Embedding.BaseURL)
		case "openai":
			env = append(env, "OPENAI_BASE_URL="+c.Embedding.BaseURL)
		case "voyage":
			env = append(env, "VOYAGE_BASE_URL="+c.Embedding.BaseURL)
		case "mistral":
			env = append(env, "MISTRAL_BASE_URL="+c.Embedding.BaseURL)
		}
	}
	if c.Profile != "" {
		// Python's pydantic-settings reads DEFAULT_PROFILE (the Settings
		// field is default_profile). Emitting OGHAM_PROFILE alone was a
		// silent no-op for Python -- subprocesses (sidecar, dashboard)
		// would fall back to Python's literal "default" profile.
		env = append(env, "DEFAULT_PROFILE="+c.Profile)
		// Also emit OGHAM_PROFILE so a child Go process (e.g. a nested
		// ogham invocation) observes the same value Python does.
		env = append(env, "OGHAM_PROFILE="+c.Profile)
	}
	// Provider-specific API keys -- only emit the one that matches the
	// configured provider to keep the subprocess env minimal.
	if c.Embedding.APIKey != "" {
		switch c.Embedding.Provider {
		case "openai":
			env = append(env, "OPENAI_API_KEY="+c.Embedding.APIKey)
		case "voyage":
			env = append(env, "VOYAGE_API_KEY="+c.Embedding.APIKey)
		case "gemini":
			env = append(env, "GEMINI_API_KEY="+c.Embedding.APIKey)
		case "mistral":
			env = append(env, "MISTRAL_API_KEY="+c.Embedding.APIKey)
		}
	}
	return env
}
