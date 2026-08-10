// Package filters is the post-tool-use filtering engine that
// classifies, dedupes, and secret-masks PostToolUse hook events
// natively in Go before they reach the memory store. Replaces the
// Railway-shutdown-stranded gateway pass-through path.
//
// Classify and MaskSecrets are pure: input in, output out, no side
// effects. IsDuplicate is not -- it reads and writes marker files
// under a directory the caller supplies, because the hook runs as a
// fresh process per tool call and in-process state cannot survive
// that (#26 finding 4). It is a method on a Deduper so tests can
// inject their own clock and directory.
//
// Regex patterns are compiled once at package init from the
// hooks_config.yaml embedded by internal/native/shared. A
// compile-time failure here would surface immediately at binary
// start, not on the first hook event.
package filters

import (
	"fmt"
	"regexp"

	"github.com/ogham-mcp/ogham-cli/internal/native/shared"
)

// loadedConfig holds the pre-parsed hooks_config.yaml plus the regex
// objects derived from it. Populated once at init.
type loadedConfig struct {
	cfg                *shared.HooksConfig
	bareTokens         *regexp.Regexp   // OR-joined patterns from secrets.bare_tokens
	envKeyPattern      *regexp.Regexp   // KEY=value for env_keys list
	keyValueAssignment *regexp.Regexp   // Generic KEY=VALUE for service prefixes
	urlCredentials     *regexp.Regexp   // ://user:pass@host
	alwaysSkip         map[string]bool  // O(1) lookup for always_skip_tools
	routine            map[string]bool  // O(1) lookup for routine_tools
	responseGated      map[string]bool  // O(1) lookup for response_gated_tools
}

var config *loadedConfig

func init() {
	cfg, err := shared.LoadHooksConfig()
	if err != nil {
		panic(fmt.Sprintf("filters: cannot load shared/hooks_config.yaml: %v", err))
	}
	loaded, err := newLoadedConfig(cfg)
	if err != nil {
		panic(fmt.Sprintf("filters: cannot compile patterns: %v", err))
	}
	config = loaded
}

// newLoadedConfig is split out from init so tests can build a config
// from an alternative shared.HooksConfig (e.g. golden-fixture
// inputs) without touching the package-global one.
func newLoadedConfig(cfg *shared.HooksConfig) (*loadedConfig, error) {
	bareTokens, err := buildBareTokenRegex(cfg.Secrets.BareTokens)
	if err != nil {
		return nil, fmt.Errorf("bare_tokens: %w", err)
	}
	envKeyPattern, err := buildEnvKeyRegex(cfg.Secrets.EnvKeys)
	if err != nil {
		return nil, fmt.Errorf("env_keys: %w", err)
	}
	return &loadedConfig{
		cfg:                cfg,
		bareTokens:         bareTokens,
		envKeyPattern:      envKeyPattern,
		keyValueAssignment: keyValueAssignmentRegex,
		urlCredentials:     urlCredentialsRegex,
		alwaysSkip:         toSet(cfg.AlwaysSkipTools),
		routine:            toSet(cfg.RoutineTools),
		responseGated:      toSet(cfg.ResponseGatedTools),
	}, nil
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}
