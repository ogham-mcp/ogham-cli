// Package shared vendors the cross-stack data artifact that
// ogham-mcp publishes under the shared-data-vX.Y.Z tag stream. The
// YAML files in this directory are mirrored byte-for-byte from
// public ogham-mcp's shared/ tree via git subtree (see
// ../../../README.md for the subtree-pull command), and the schema
// lock + RE2 dialect promise is documented in shared/README.md
// upstream.
//
// Do NOT edit hooks_config.yaml, schema.yaml, README.md, or
// CHANGELOG.md by hand: bump the upstream artifact, retag, and run
// git subtree pull. The CI parity gate (see TestParity in
// shared_test.go) will reject any local-only divergence.
package shared

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed hooks_config.yaml
var hooksConfigYAML []byte

//go:embed schema.yaml
var schemaYAML []byte

// HooksConfig mirrors the top-level keys of shared/hooks_config.yaml.
// Only the surface needed by ogham-cli's native filtering engine is
// modelled here; unknown YAML keys are accepted silently for forward
// compatibility with upstream additions.
type HooksConfig struct {
	Signals struct {
		Errors       []string `yaml:"errors"`
		Decisions    []string `yaml:"decisions"`
		Architecture []string `yaml:"architecture"`
		Annotations  []string `yaml:"annotations"`
	} `yaml:"signals"`
	NoiseCommands     []string `yaml:"noise_commands"`
	AlwaysSkipTools   []string `yaml:"always_skip_tools"`
	ResponseGatedTools []string `yaml:"response_gated_tools"`
	RoutineTools      []string `yaml:"routine_tools"`
	GitSignal         []string `yaml:"git_signal"`
	GitNoise          []string `yaml:"git_noise"`
	Secrets           struct {
		BareTokens []BareToken `yaml:"bare_tokens"`
		EnvKeys    []string    `yaml:"env_keys"`
	} `yaml:"secrets"`
}

// BareToken pairs a human-readable name with a RE2-compatible regex
// pattern for secret detection.
type BareToken struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

// Schema mirrors shared/schema.yaml's version + regex_dialect fields.
type Schema struct {
	Version      string `yaml:"version"`
	RegexDialect string `yaml:"regex_dialect"`
}

// LoadHooksConfig parses the embedded hooks_config.yaml.
func LoadHooksConfig() (*HooksConfig, error) {
	var cfg HooksConfig
	if err := yaml.Unmarshal(hooksConfigYAML, &cfg); err != nil {
		return nil, fmt.Errorf("parse hooks_config.yaml: %w", err)
	}
	return &cfg, nil
}

// LoadSchema parses the embedded schema.yaml.
func LoadSchema() (*Schema, error) {
	var s Schema
	if err := yaml.Unmarshal(schemaYAML, &s); err != nil {
		return nil, fmt.Errorf("parse schema.yaml: %w", err)
	}
	return &s, nil
}

// RawHooksConfig returns the embedded hooks_config.yaml bytes
// unmodified. Used by the parity hash gate.
func RawHooksConfig() []byte { return hooksConfigYAML }

// RawSchema returns the embedded schema.yaml bytes unmodified.
// Used by the parity hash gate.
func RawSchema() []byte { return schemaYAML }
