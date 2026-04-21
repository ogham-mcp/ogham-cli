package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHome swings HOME to t.TempDir() for the lifetime of the test so
// we can exercise sentinel read/write without touching the user's real
// ~/.ogham/active_profile. t.Setenv restores the original on cleanup.
func fakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestActiveProfile_PrecedenceOrder(t *testing.T) {
	fakeHome(t)
	cfg := &Config{Profile: "toml-baseline"}

	// With no env + no sentinel, TOML baseline wins.
	t.Setenv(activeProfileEnv, "")
	if got := ActiveProfile(cfg); got != "toml-baseline" {
		t.Errorf("no env + no sentinel: want toml-baseline, got %q", got)
	}

	// Write sentinel -- now it wins over TOML.
	if err := SwitchProfile("sentinel-value"); err != nil {
		t.Fatalf("SwitchProfile: %v", err)
	}
	if got := ActiveProfile(cfg); got != "sentinel-value" {
		t.Errorf("sentinel present: want sentinel-value, got %q", got)
	}

	// Env var beats sentinel.
	t.Setenv(activeProfileEnv, "env-override")
	if got := ActiveProfile(cfg); got != "env-override" {
		t.Errorf("env set: want env-override, got %q", got)
	}
}

func TestActiveProfile_FallbackWhenAllUnset(t *testing.T) {
	fakeHome(t)
	t.Setenv(activeProfileEnv, "")
	// nil cfg exercises the defensive fallback path.
	if got := ActiveProfile(nil); got != "default" {
		t.Errorf("nil cfg fallback: want 'default', got %q", got)
	}
	// cfg with empty Profile also falls back.
	if got := ActiveProfile(&Config{}); got != "default" {
		t.Errorf("empty cfg.Profile fallback: want 'default', got %q", got)
	}
}

func TestActiveProfile_EnvWhitespaceIgnored(t *testing.T) {
	fakeHome(t)
	cfg := &Config{Profile: "toml"}
	// Whitespace-only env must not pin to "" -- we fall through.
	t.Setenv(activeProfileEnv, "   ")
	if got := ActiveProfile(cfg); got != "toml" {
		t.Errorf("whitespace env should fall through: want toml, got %q", got)
	}
}

func TestSwitchProfile_WritesTrimmedName(t *testing.T) {
	home := fakeHome(t)
	if err := SwitchProfile("  work  "); err != nil {
		t.Fatalf("SwitchProfile: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".ogham", sentinelFileName))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	// File contains trimmed name + trailing newline for tool-friendly cat.
	if got := string(raw); got != "work\n" {
		t.Errorf("sentinel contents: want %q, got %q", "work\n", got)
	}
}

func TestSwitchProfile_RejectsEmpty(t *testing.T) {
	fakeHome(t)
	err := SwitchProfile("")
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("want non-empty error, got %v", err)
	}
	// Whitespace collapses to empty after trim.
	err = SwitchProfile("   ")
	if err == nil {
		t.Errorf("whitespace-only profile must be rejected")
	}
}

func TestSwitchProfile_AtomicRename(t *testing.T) {
	home := fakeHome(t)
	// Pre-populate with a value. The atomic rename must replace it
	// cleanly without leaving a stray .tmp file in ~/.ogham/.
	if err := SwitchProfile("first"); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	if err := SwitchProfile("second"); err != nil {
		t.Fatalf("second switch: %v", err)
	}
	// Exactly one file in ~/.ogham/: the sentinel. No dangling tmp.
	entries, err := os.ReadDir(filepath.Join(home, ".ogham"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != sentinelFileName {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("want exactly [%s], got %v", sentinelFileName, names)
	}
}

func TestClearActiveProfile_RemovesSentinel(t *testing.T) {
	fakeHome(t)
	if err := SwitchProfile("ephemeral"); err != nil {
		t.Fatalf("SwitchProfile: %v", err)
	}
	if err := ClearActiveProfile(); err != nil {
		t.Fatalf("ClearActiveProfile: %v", err)
	}
	if got := readSentinel(); got != "" {
		t.Errorf("after clear, sentinel should be empty; got %q", got)
	}
}

func TestClearActiveProfile_MissingFileIsNoop(t *testing.T) {
	fakeHome(t)
	// No sentinel exists. Clear must not return an error.
	if err := ClearActiveProfile(); err != nil {
		t.Errorf("clear on missing file: want nil, got %v", err)
	}
}

func TestActiveProfile_CorruptSentinelFallsThrough(t *testing.T) {
	// If the sentinel file is unreadable (mode 000, for example) or
	// contains only whitespace, ActiveProfile must fall through to cfg
	// rather than returning "" (which would later become "default"
	// silently -- confusing).
	home := fakeHome(t)
	sentinelDir := filepath.Join(home, ".ogham")
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sentinel := filepath.Join(sentinelDir, sentinelFileName)
	if err := os.WriteFile(sentinel, []byte("\n\n   \n"), 0o644); err != nil {
		t.Fatalf("write whitespace sentinel: %v", err)
	}
	t.Setenv(activeProfileEnv, "")
	cfg := &Config{Profile: "toml-baseline"}
	if got := ActiveProfile(cfg); got != "toml-baseline" {
		t.Errorf("whitespace-only sentinel: want toml-baseline, got %q", got)
	}
}
