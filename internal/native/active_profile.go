package native

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sentinelFileName is the profile-state file the Go CLI writes to.
// Lives next to config.toml so everything Ogham-ish is under ~/.ogham/.
// Not in config.env on purpose -- env files are shell-sourced and the
// profile state shouldn't leak into arbitrary processes.
const sentinelFileName = "active_profile"

// activeProfileEnv is the hot-swap override. A caller who exports
// OGHAM_PROFIL=xyz in their shell always gets xyz for the lifetime of
// that shell, regardless of what's in the sentinel or the TOML. Matches
// the established env-wins-over-disk precedence in this repo.
const activeProfileEnv = "OGHAM_PROFILE"

// SentinelPath returns ~/.ogham/active_profile. Exposed so tests can
// override the HOME env to isolate the sentinel from the user's real
// profile without polluting it.
func SentinelPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// UserHomeDir returns an error only when HOME is unset on Unix.
		// In that edge case we fall back to the current directory -- any
		// later read/write will fail with a clear message.
		return filepath.Join(".", ".ogham", sentinelFileName)
	}
	return filepath.Join(home, ".ogham", sentinelFileName)
}

// ActiveProfile returns the profile to use for tool calls. Precedence,
// highest first:
//
//  1. $OGHAM_PROFILE env var
//  2. ~/.ogham/active_profile sentinel file
//  3. cfg.Profile (loaded from TOML / applyEnv)
//  4. "default"
//
// A cfg of nil is tolerated for callers that only have a HOME-scoped
// view (the CLI `ogham profile current` command, for example, doesn't
// need the full config to answer the question).
func ActiveProfile(cfg *Config) string {
	if env := strings.TrimSpace(os.Getenv(activeProfileEnv)); env != "" {
		return env
	}
	if s := readSentinel(); s != "" {
		return s
	}
	if cfg != nil && cfg.Profile != "" {
		return cfg.Profile
	}
	return "default"
}

// readSentinel returns the trimmed contents of the sentinel file, or
// "" if the file is absent / empty / unreadable. Errors are swallowed
// on purpose: a corrupt or missing sentinel must not break ActiveProfile,
// which is called on every tool invocation.
func readSentinel() string {
	raw, err := os.ReadFile(SentinelPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// SwitchProfile writes profile to the sentinel file atomically (temp file
// + rename) so a crash mid-write cannot leave a half-written or empty
// sentinel for the next read.
//
// Refuses empty or whitespace-only names -- the caller should use
// ClearActiveProfile() to explicitly fall back to the TOML baseline.
func SwitchProfile(profile string) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return fmt.Errorf("active_profile: profile name must be non-empty (use ClearActiveProfile to reset)")
	}

	dest := SentinelPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("active_profile: mkdir: %w", err)
	}

	// Write to a sibling temp file first, then rename. rename(2) is
	// atomic on POSIX when src + dst live on the same filesystem, which
	// they do here (both under ~/.ogham/).
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".active_profile.*.tmp")
	if err != nil {
		return fmt.Errorf("active_profile: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up the temp file on any failure path.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(profile + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("active_profile: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("active_profile: close: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("active_profile: rename: %w", err)
	}
	return nil
}

// ClearActiveProfile deletes the sentinel file. Subsequent ActiveProfile
// calls fall through to cfg.Profile (the TOML baseline). Missing-file
// is a no-op (not an error) so callers can treat it as idempotent.
func ClearActiveProfile() error {
	err := os.Remove(SentinelPath())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("active_profile: remove: %w", err)
	}
	return nil
}
