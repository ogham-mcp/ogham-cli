package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestOghamBinaryPathIsAbsolute checks that the binary path resolution
// returns an absolute path under normal conditions (os.Executable() is
// available on darwin/linux/windows). Load-bearing: the whole point of
// the #7 fix is that hook commands embed an absolute path, not a $PATH
// lookup target.
func TestOghamBinaryPathIsAbsolute(t *testing.T) {
	got := oghamBinaryPath()
	if got == "" {
		t.Fatal("oghamBinaryPath() returned empty string")
	}
	if got == "ogham" {
		t.Errorf("oghamBinaryPath() fell through to the bare-name fallback; "+
			"expected an absolute path from os.Executable() (got %q)", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("oghamBinaryPath() = %q; want absolute path", got)
	}
}

// TestOghamHookCommandShape checks the formatted command string is
// `<abs-path> hooks run <verb>` -- the three-token verb shape that
// distinguishes Go-side commands from Python `ogham hooks <verb>`.
func TestOghamHookCommandShape(t *testing.T) {
	got := oghamHookCommand("session-start")
	parts := strings.Fields(got)
	if len(parts) != 4 {
		t.Fatalf("oghamHookCommand returned %q; want 4 tokens (path, hooks, run, verb)", got)
	}
	if parts[1] != "hooks" || parts[2] != "run" || parts[3] != "session-start" {
		t.Errorf("oghamHookCommand tokens = %v; want [<path> hooks run session-start]", parts)
	}
	if !filepath.IsAbs(parts[0]) {
		t.Errorf("oghamHookCommand path component = %q; want absolute", parts[0])
	}
}

// TestOghamGoHookCommandRegex is the load-bearing detection check:
// must match all forms of Go-side hook commands AND must NOT match
// Python ogham-mcp commands. Failure here means the v0.7.4 idempotent
// install pre-pass or `hooks uninstall` could either miss broken
// entries (false negative) or strip a user's working Python config
// (false positive).
func TestOghamGoHookCommandRegex(t *testing.T) {
	cases := []struct {
		command string
		want    bool
		reason  string
	}{
		// Go-side: broken pre-v0.7.4 form
		{"ogham-cli hooks run session-start", true, "broken pre-v0.7.4 bare-name form"},
		{"ogham-cli hooks run post-tool", true, "broken pre-v0.7.4 bare-name form"},
		{"ogham-cli hooks run inscribe", true, "broken pre-v0.7.4 bare-name form"},
		{"ogham-cli hooks run recall", true, "broken pre-v0.7.4 bare-name form"},

		// Go-side: fixed v0.7.4+ absolute-path form
		{"/usr/local/bin/ogham hooks run session-start", true, "v0.7.4+ absolute-path form"},
		{"/Users/kevin/.local/bin/ogham hooks run recall", true, "v0.7.4+ absolute-path form"},
		{"/home/dev/go/bin/ogham hooks run inscribe", true, "v0.7.4+ absolute-path form"},

		// Python ogham-mcp: two-token verb shape, must NOT match
		{"/path/to/.venv/bin/ogham hooks recall", false, "Python verb shape"},
		{"/path/to/.venv/bin/ogham hooks inscribe", false, "Python verb shape"},
		{"ogham hooks recall", false, "Python verb shape (bare name)"},

		// Unrelated commands must NOT match
		{"node /path/to/some-other-tool hooks run x", false, "unrelated tool"},
		{"echo ogham hooks run nothing-here-malicious", false, "echo, not actual ogham"},
		{"", false, "empty command"},
		{"some random text", false, "no shape match"},
	}

	for _, tc := range cases {
		got := oghamGoHookCommandRegex.MatchString(tc.command)
		if got != tc.want {
			t.Errorf("regex.MatchString(%q) = %v; want %v (%s)", tc.command, got, tc.want, tc.reason)
		}
	}
}

// TestPruneOghamGoHooksStripsOnlyGo gives the prune function a settings
// blob containing Go-broken, Go-fixed, Python, and unrelated hooks across
// multiple events. After pruning: only Go entries removed.
func TestPruneOghamGoHooksStripsOnlyGo(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						// Go broken (pre-v0.7.4) -- should be removed
						map[string]any{"type": "command", "command": "ogham-cli hooks run session-start"},
					},
				},
				map[string]any{
					"matcher": "",
					"hooks": []any{
						// Python -- should be KEPT
						map[string]any{"type": "command", "command": "/path/to/.venv/bin/ogham hooks recall"},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						// Go fixed (v0.7.4+) -- should be removed
						map[string]any{"type": "command", "command": "/usr/local/bin/ogham hooks run post-tool"},
					},
				},
			},
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						// Unrelated -- should be kept
						map[string]any{"type": "command", "command": "/some/other/tool --flag"},
					},
				},
			},
		},
		"otherKey": "should be untouched",
	}

	removed := pruneOghamGoHooks(settings)
	if removed != 2 {
		t.Errorf("pruneOghamGoHooks removed %d entries; want 2", removed)
	}

	hooks := settings["hooks"].(map[string]any)

	// SessionStart should still exist (Python entry kept) with 1 entry
	ss, ok := hooks["SessionStart"].([]any)
	if !ok {
		t.Fatal("SessionStart event was dropped; should still exist with the Python entry")
	}
	if len(ss) != 1 {
		t.Errorf("SessionStart has %d entries; want 1 (the Python one)", len(ss))
	}
	// Verify the surviving SessionStart entry is the Python one
	if ssEntry, ok := ss[0].(map[string]any); ok {
		if innerHooks, ok := ssEntry["hooks"].([]any); ok && len(innerHooks) > 0 {
			if hm, ok := innerHooks[0].(map[string]any); ok {
				cmd, _ := hm["command"].(string)
				if !strings.Contains(cmd, ".venv/bin/ogham hooks recall") {
					t.Errorf("surviving SessionStart entry has unexpected command: %q", cmd)
				}
			}
		}
	}

	// PostToolUse should be GONE -- only had the Go entry, which was removed
	if _, exists := hooks["PostToolUse"]; exists {
		t.Error("PostToolUse event should have been dropped after its only entry was pruned")
	}

	// UserPromptSubmit should still exist with the unrelated entry intact
	ups, ok := hooks["UserPromptSubmit"].([]any)
	if !ok || len(ups) != 1 {
		t.Error("UserPromptSubmit should still exist with the unrelated entry")
	}

	// Top-level unrelated key untouched
	if settings["otherKey"] != "should be untouched" {
		t.Error("top-level unrelated settings key was modified")
	}
}

// TestPruneOghamGoHooksIdempotent: calling prune twice on the same input
// produces the same result the second time as the first did on the
// already-pruned state. (Second call should remove zero entries.)
func TestPruneOghamGoHooksIdempotent(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "ogham-cli hooks run session-start"},
					},
				},
			},
		},
	}

	first := pruneOghamGoHooks(settings)
	second := pruneOghamGoHooks(settings)
	if first != 1 {
		t.Errorf("first prune removed %d; want 1", first)
	}
	if second != 0 {
		t.Errorf("second prune removed %d; want 0 (idempotent)", second)
	}
}

// TestPruneOghamGoHooksHandlesNoHooksKey: settings without a "hooks" key
// (or with a non-map value) should not panic. Returns 0.
func TestPruneOghamGoHooksHandlesNoHooksKey(t *testing.T) {
	cases := []map[string]any{
		{},
		{"otherKey": "value"},
		{"hooks": "not a map"},
		{"hooks": nil},
	}
	for i, s := range cases {
		removed := pruneOghamGoHooks(s)
		if removed != 0 {
			t.Errorf("case %d: removed %d; want 0", i, removed)
		}
	}
}
