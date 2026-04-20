package native

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Save writes cfg to path as TOML with 0600 perms (API keys live here).
// Creates parent directories as needed. Existing keys not known to
// native.Config are preserved by reading the file first, merging on top
// of the parsed structure, and re-emitting; this avoids clobbering
// fields like api_key + gateway_url that the sidecar-mode loader owns.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("save config: mkdir: %w", err)
	}

	// Read existing so we can merge. When the file doesn't exist, start
	// from an empty map.
	existing := make(map[string]any)
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &existing) // best-effort
	}

	// Overlay our known fields.
	if cfg.Profile != "" {
		existing["profile"] = cfg.Profile
	}
	db := map[string]any{}
	if existingDB, ok := existing["database"].(map[string]any); ok {
		db = existingDB
	}
	if cfg.Database.Backend != "" {
		db["backend"] = cfg.Database.Backend
	}
	if cfg.Database.URL != "" {
		db["url"] = cfg.Database.URL
	}
	if cfg.Database.SupabaseURL != "" {
		db["supabase_url"] = cfg.Database.SupabaseURL
	}
	if cfg.Database.SupabaseKey != "" {
		db["supabase_key"] = cfg.Database.SupabaseKey
	}
	if len(db) > 0 {
		existing["database"] = db
	}

	emb := map[string]any{}
	if existingEmb, ok := existing["embedding"].(map[string]any); ok {
		emb = existingEmb
	}
	if cfg.Embedding.Provider != "" {
		emb["provider"] = cfg.Embedding.Provider
	}
	if cfg.Embedding.APIKey != "" {
		emb["api_key"] = cfg.Embedding.APIKey
	}
	if cfg.Embedding.Model != "" {
		emb["model"] = cfg.Embedding.Model
	}
	if cfg.Embedding.Dimension > 0 {
		emb["dimension"] = cfg.Embedding.Dimension
	}
	if len(emb) > 0 {
		existing["embedding"] = emb
	}

	// Write with 0600 perms; open with O_TRUNC to avoid leftover bytes
	// if the new content is shorter than the old.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("save config: open: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := toml.NewEncoder(f).Encode(existing); err != nil {
		return fmt.Errorf("save config: encode: %w", err)
	}
	return nil
}

// DeriveSidecarExtras computes the correct `uv tool run --from ogham-mcp[...]`
// extras spec from a config. Combines the backend extra (postgres for the
// direct-DB Python backend; nothing for the Supabase PostgREST backend
// which is the base install) with the embedding provider extra (gemini,
// voyage, mistral -- openai and ollama are in the base).
//
// Returns a comma-separated string ready to be placed inside the [...]
// brackets of a ogham-mcp install spec, or "" if no extras are needed.
func DeriveSidecarExtras(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	seen := map[string]bool{}
	add := func(extra string) {
		if extra != "" && !seen[extra] {
			seen[extra] = true
		}
	}
	// Python backend extras -- postgres is the only one the Python CLI
	// exposes; Supabase is baked into the base install.
	backend := cfg.Database.Backend
	if backend == "" {
		if cfg.Database.URL != "" {
			backend = "postgres"
		} else if cfg.Database.SupabaseURL != "" {
			backend = "supabase"
		}
	}
	if backend == "postgres" {
		add("postgres")
	}
	// Embedding provider SDK extras. openai + ollama don't need extras.
	switch cfg.Embedding.Provider {
	case "gemini":
		add("gemini")
	case "voyage":
		add("voyage")
	case "mistral":
		add("mistral")
	}

	if len(seen) == 0 {
		return ""
	}
	// Stable order so tests and round-trips are deterministic.
	order := []string{"postgres", "gemini", "voyage", "mistral"}
	out := make([]string, 0, len(seen))
	for _, k := range order {
		if seen[k] {
			out = append(out, k)
		}
	}
	return strings.Join(out, ",")
}

// SaveEnvFile writes an env-file (KEY=VALUE, one per line) mirror of the
// TOML config's Python-relevant variables. Python ogham reads this file
// either directly (its config loader checks ~/.ogham/config.env) or via
// shell sourcing. Keeping it in sync means Python + Go agree on config
// without either having to parse the other's format.
func SaveEnvFile(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("save env: mkdir: %w", err)
	}

	// Use a map so writes are deterministic regardless of cfg field order.
	entries := map[string]string{}
	if cfg.Database.URL != "" {
		entries["DATABASE_URL"] = cfg.Database.URL
	}
	if cfg.Database.SupabaseURL != "" {
		entries["SUPABASE_URL"] = cfg.Database.SupabaseURL
	}
	if cfg.Database.SupabaseKey != "" {
		entries["SUPABASE_KEY"] = cfg.Database.SupabaseKey
	}
	if cfg.Database.Backend != "" {
		entries["DATABASE_BACKEND"] = cfg.Database.Backend
	}
	if cfg.Embedding.Provider != "" {
		entries["EMBEDDING_PROVIDER"] = cfg.Embedding.Provider
	}
	if cfg.Embedding.Dimension > 0 {
		entries["EMBEDDING_DIM"] = fmt.Sprintf("%d", cfg.Embedding.Dimension)
	}
	if cfg.Embedding.Model != "" {
		entries["EMBEDDING_MODEL"] = cfg.Embedding.Model
	}
	if cfg.Embedding.APIKey != "" {
		switch cfg.Embedding.Provider {
		case "openai":
			entries["OPENAI_API_KEY"] = cfg.Embedding.APIKey
		case "voyage":
			entries["VOYAGE_API_KEY"] = cfg.Embedding.APIKey
		case "gemini":
			entries["GEMINI_API_KEY"] = cfg.Embedding.APIKey
		case "mistral":
			entries["MISTRAL_API_KEY"] = cfg.Embedding.APIKey
		}
	}
	// Ollama has no API key but does honour OLLAMA_URL for custom hosts
	// (Docker container, remote box, etc.). Preserve whatever is in the
	// process env so the sidecar inherits it.
	if cfg.Embedding.Provider == "ollama" {
		if v := strings.TrimSpace(os.Getenv("OLLAMA_URL")); v != "" {
			entries["OLLAMA_URL"] = v
		}
	}
	if cfg.Profile != "" {
		entries["DEFAULT_PROFILE"] = cfg.Profile
	}
	// Write OGHAM_SIDECAR_EXTRAS so any sidecar spawn (ogham dashboard,
	// sidecar-backed health/list/search) installs the right Python extras.
	// Without this, `uv tool run --from ogham-mcp` gets bare ogham-mcp with
	// no provider-specific SDKs -- google-genai missing, voyageai missing,
	// etc. -- and the sidecar crashes on the first embed call.
	if extras := DeriveSidecarExtras(cfg); extras != "" {
		entries["OGHAM_SIDECAR_EXTRAS"] = extras
	}

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("# Generated by `ogham init`. Safe to edit manually.\n")
	sb.WriteString("# Python MCP (ogham-mcp) reads this file; Go CLI reads via the\n")
	sb.WriteString("# same dotenv loader that powers --native mode.\n\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, entries[k])
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("save env: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(sb.String()); err != nil {
		return fmt.Errorf("save env: write: %w", err)
	}
	return nil
}
