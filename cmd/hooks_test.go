package cmd

import (
	"bytes"
	"os"
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

// ---------------- #10 gateway-aware install -----------------------------

// TestBuildOghamHookSetSkipsPostToolWhenKeyEmpty: with no gateway api_key
// configured, PostToolUse must NOT appear in the emitted hook set. The
// three native-capable events (SessionStart, PreCompact, PostCompact)
// must still be present.
func TestBuildOghamHookSetSkipsPostToolWhenKeyEmpty(t *testing.T) {
	hooks := buildOghamHookSet("", "/usr/local/bin/ogham")

	if _, exists := hooks["PostToolUse"]; exists {
		t.Errorf("PostToolUse should NOT be wired when apiKey is empty (got %v)", hooks["PostToolUse"])
	}
	// #11: PreCompact -> inscribe dropped from defaults. SessionStart
	// and PostCompact (recall) remain.
	for _, required := range []string{"SessionStart", "PostCompact"} {
		if _, exists := hooks[required]; !exists {
			t.Errorf("event %s missing from native-only hook set; should be present regardless of apiKey", required)
		}
	}
	if _, exists := hooks["PreCompact"]; exists {
		t.Error("PreCompact must NOT be in the default scaffold (#11: native inscribe writes low-signal stubs; use the explicit `ogham inscribe` verb)")
	}
}

// TestBuildOghamHookSetIncludesPostToolWithMatcherWhenKeyPresent: with a
// gateway key configured, PostToolUse must appear AND use the scoped
// defaultPostToolMatcher rather than "" (which fires on every tool call).
func TestBuildOghamHookSetIncludesPostToolWithMatcherWhenKeyPresent(t *testing.T) {
	hooks := buildOghamHookSet("sk_test_anything_nonempty", "/usr/local/bin/ogham")

	pt, ok := hooks["PostToolUse"]
	if !ok {
		t.Fatal("PostToolUse should be wired when apiKey is non-empty")
	}
	matcher, _ := pt["matcher"].(string)
	if matcher != defaultPostToolMatcher {
		t.Errorf("PostToolUse matcher = %q; want %q (scoped to write-class tools)", matcher, defaultPostToolMatcher)
	}
	if matcher == "" {
		t.Error("PostToolUse matcher must NOT be empty (would fire on every tool call -- the pre-v0.8 noise problem)")
	}
}

// TestBuildOghamHookSetEmbedsAbsoluteBinaryPath: every emitted hook
// command must start with the binPath argument. This is the load-bearing
// link between #7's os.Executable() fix and #10's install-time behavior.
func TestBuildOghamHookSetEmbedsAbsoluteBinaryPath(t *testing.T) {
	const binPath = "/Users/test/.local/bin/ogham"
	hooks := buildOghamHookSet("sk_test_key", binPath)

	for event, entry := range hooks {
		innerHooks, ok := entry["hooks"].([]map[string]string)
		if !ok || len(innerHooks) == 0 {
			t.Errorf("event %s: missing inner hooks slice", event)
			continue
		}
		cmd := innerHooks[0]["command"]
		if !strings.HasPrefix(cmd, binPath+" ") {
			t.Errorf("event %s: command = %q; want prefix %q", event, cmd, binPath+" ")
		}
	}
}

// TestNoticePostToolUnconfiguredOnceIsIdempotent: first call writes the
// stash marker AND returns true; second call returns false without
// writing more noise.
func TestNoticePostToolUnconfiguredOnceIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ogham", "post-tool-unconfigured-notice")

	var first, second bytes.Buffer
	gotFirst := noticePostToolUnconfiguredOnce(marker, &first)
	gotSecond := noticePostToolUnconfiguredOnce(marker, &second)

	if !gotFirst {
		t.Error("first call should return true (notice was emitted)")
	}
	if gotSecond {
		t.Error("second call should return false (marker exists, notice suppressed)")
	}
	if first.Len() == 0 {
		t.Error("first call should write diagnostic to writer")
	}
	if second.Len() != 0 {
		t.Errorf("second call should write nothing; got %q", second.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker file should exist after first call; stat err: %v", err)
	}
}

// TestNoticePostToolUnconfiguredOnceWritesUsefulDiagnostic: the stderr
// output must mention both remediation paths (install + auth login) so a
// confused user has a clear next step.
func TestNoticePostToolUnconfiguredOnceWritesUsefulDiagnostic(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ogham", "notice")

	var buf bytes.Buffer
	noticePostToolUnconfiguredOnce(marker, &buf)

	out := buf.String()
	wantSubstrings := []string{
		"ogham hooks install",
		"ogham auth login",
		"exit 0",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("notice output missing %q; got: %s", s, out)
		}
	}
}

// TestNoticePostToolUnconfiguredOnceHandlesEmptyMarker: when UserCacheDir
// errors and we have no marker path, the function still emits the notice
// (best-effort diagnostic) and returns true.
func TestNoticePostToolUnconfiguredOnceHandlesEmptyMarker(t *testing.T) {
	var buf bytes.Buffer
	got := noticePostToolUnconfiguredOnce("", &buf)
	if !got {
		t.Error("empty marker path should still emit the notice (returns true)")
	}
	if buf.Len() == 0 {
		t.Error("notice should still write to writer when marker path is empty")
	}
}
