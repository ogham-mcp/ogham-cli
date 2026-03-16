package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
api_key = "ogham_live_test123"
gateway_url = "https://custom.example.com"
`), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "ogham_live_test123" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "ogham_live_test123")
	}
	if cfg.GatewayURL != "https://custom.example.com" {
		t.Errorf("GatewayURL = %q, want %q", cfg.GatewayURL, "https://custom.example.com")
	}
}

func TestEnvVarsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
api_key = "from_file"
gateway_url = "https://from-file.com"
`), 0600)

	t.Setenv("OGHAM_API_KEY", "from_env")
	t.Setenv("OGHAM_GATEWAY_URL", "https://from-env.com")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "from_env" {
		t.Errorf("APIKey = %q, want %q (env override)", cfg.APIKey, "from_env")
	}
	if cfg.GatewayURL != "https://from-env.com" {
		t.Errorf("GatewayURL = %q, want %q (env override)", cfg.GatewayURL, "https://from-env.com")
	}
}

func TestDefaultGatewayURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`api_key = "test"`), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.GatewayURL != "https://api.ogham-mcp.dev" {
		t.Errorf("GatewayURL = %q, want default", cfg.GatewayURL)
	}
}

func TestMissingFileUsesEnvOnly(t *testing.T) {
	t.Setenv("OGHAM_API_KEY", "env_only")

	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.APIKey != "env_only" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "env_only")
	}
}

func TestPermissionCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`api_key = "test"`), 0644)

	warnings := CheckPermissions(path)
	if len(warnings) == 0 {
		t.Error("expected permission warning for 0644 file")
	}
}

func TestSaveConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &Config{APIKey: "ogham_live_saved", GatewayURL: "https://api.ogham-mcp.dev"}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
	}

	loaded, _ := Load(path)
	if loaded.APIKey != "ogham_live_saved" {
		t.Errorf("reloaded APIKey = %q, want %q", loaded.APIKey, "ogham_live_saved")
	}
}
