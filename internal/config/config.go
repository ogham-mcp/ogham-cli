package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const DefaultGatewayURL = "https://api.ogham-mcp.dev"

type Config struct {
	APIKey     string `toml:"api_key"`
	GatewayURL string `toml:"gateway_url"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ogham", "config.toml")
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		GatewayURL: DefaultGatewayURL,
	}

	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	if v := os.Getenv("OGHAM_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("OGHAM_GATEWAY_URL"); v != "" {
		cfg.GatewayURL = v
	}

	return cfg, nil
}

func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	return toml.NewEncoder(f).Encode(cfg)
}

func CheckPermissions(path string) []string {
	var warnings []string
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		warnings = append(warnings, fmt.Sprintf(
			"config file %s has permissions %o (should be 0600) -- your API key may be exposed",
			path, perm,
		))
	}
	return warnings
}
