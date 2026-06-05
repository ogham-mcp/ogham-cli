package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- buildPluginManifest --------------------------------------------------

func TestBuildPluginManifestHasRequiredFields(t *testing.T) {
	m := buildPluginManifest("0.8.0")

	// Required per Anthropic Plugins reference.
	if name, _ := m["name"].(string); name != "ogham" {
		t.Errorf("name = %q; want %q", name, "ogham")
	}

	// Recommended for plugins that mutate user data (v2.1.154+).
	enabled, ok := m["defaultEnabled"].(bool)
	if !ok || enabled {
		t.Errorf("defaultEnabled = %v; want false (plugin touches user data)", m["defaultEnabled"])
	}

	// Version threads through from the running binary's Version var --
	// must be present and match what was passed in (not hard-coded).
	if v, _ := m["version"].(string); v != "0.8.0" {
		t.Errorf("version = %q; want %q (must thread from caller, not hard-code)", v, "0.8.0")
	}

	for _, k := range []string{"description", "author", "displayName"} {
		if _, ok := m[k]; !ok {
			t.Errorf("manifest missing recommended key %q", k)
		}
	}
}

// ---- buildPluginHooksFile -------------------------------------------------

// TestBuildPluginHooksFileUsesPluginRootEnvVar is the load-bearing
// #9 check: plugin-scoped hooks MUST use ${CLAUDE_PLUGIN_ROOT} and
// MUST NOT bake an absolute path (os.Executable). Plugin install dirs
// change on update.
func TestBuildPluginHooksFileUsesPluginRootEnvVar(t *testing.T) {
	hooksFile := buildPluginHooksFile("sk_test_key")
	hooks, ok := hooksFile["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks key missing or wrong type")
	}

	for event, entriesRaw := range hooks {
		entries, ok := entriesRaw.([]map[string]any)
		if !ok || len(entries) == 0 {
			t.Errorf("%s: entries slice missing", event)
			continue
		}
		innerHooks, ok := entries[0]["hooks"].([]map[string]any)
		if !ok || len(innerHooks) == 0 {
			t.Errorf("%s: inner hooks slice missing", event)
			continue
		}
		cmd, _ := innerHooks[0]["command"].(string)
		if !strings.HasPrefix(cmd, "${CLAUDE_PLUGIN_ROOT}/") {
			t.Errorf("%s: command = %q; want prefix ${CLAUDE_PLUGIN_ROOT}/ (Anthropic-prescribed pattern for plugin-scoped hooks)", event, cmd)
		}
		// Absolute paths would mean someone baked os.Executable() --
		// that is correct for `hooks install` but wrong for plugins.
		if strings.HasPrefix(cmd, "/") {
			t.Errorf("%s: command starts with /; plugin hooks MUST use ${CLAUDE_PLUGIN_ROOT}, not an absolute path (plugin install dir changes on update)", event)
		}
	}
}

func TestBuildPluginHooksFileUsesExecForm(t *testing.T) {
	hooksFile := buildPluginHooksFile("sk_test_key")
	hooks := hooksFile["hooks"].(map[string]any)

	// SessionStart is always present; verb-args check is sufficient.
	entries := hooks["SessionStart"].([]map[string]any)
	innerHooks := entries[0]["hooks"].([]map[string]any)
	first := innerHooks[0]

	args, ok := first["args"].([]string)
	if !ok {
		t.Fatalf("SessionStart hook missing `args` slice; got %T", first["args"])
	}
	want := []string{"hooks", "run", "session-start"}
	if len(args) != len(want) || args[0] != want[0] || args[1] != want[1] || args[2] != want[2] {
		t.Errorf("SessionStart args = %v; want %v (exec form: command + args, NOT a single shell string)", args, want)
	}

	if cmd, _ := first["command"].(string); strings.Contains(cmd, " ") {
		t.Errorf("SessionStart command %q contains spaces; should be just the binary path, with verb in args", cmd)
	}
}

func TestBuildPluginHooksFileSkipsPostToolWhenKeyEmpty(t *testing.T) {
	hooksFile := buildPluginHooksFile("")
	hooks := hooksFile["hooks"].(map[string]any)

	if _, exists := hooks["PostToolUse"]; exists {
		t.Error("PostToolUse should NOT be wired when apiKey is empty (#10 composition: same logic as hooks install)")
	}
	for _, required := range []string{"SessionStart", "PreCompact", "PostCompact"} {
		if _, exists := hooks[required]; !exists {
			t.Errorf("event %s missing -- should be present even on native-only setups", required)
		}
	}
}

func TestBuildPluginHooksFileIncludesPostToolWithScopedMatcher(t *testing.T) {
	hooksFile := buildPluginHooksFile("sk_test_key")
	hooks := hooksFile["hooks"].(map[string]any)

	entries, ok := hooks["PostToolUse"].([]map[string]any)
	if !ok {
		t.Fatal("PostToolUse should be wired when apiKey is non-empty")
	}
	matcher, _ := entries[0]["matcher"].(string)
	if matcher != defaultPostToolMatcher {
		t.Errorf("PostToolUse matcher = %q; want %q (scoped to write-class tools, matches #10)", matcher, defaultPostToolMatcher)
	}
}

func TestBuildPluginHooksFileCompactMatchersAreManualAuto(t *testing.T) {
	hooksFile := buildPluginHooksFile("")
	hooks := hooksFile["hooks"].(map[string]any)

	for _, event := range []string{"PreCompact", "PostCompact"} {
		entries, ok := hooks[event].([]map[string]any)
		if !ok || len(entries) == 0 {
			t.Errorf("%s entries missing", event)
			continue
		}
		matcher, _ := entries[0]["matcher"].(string)
		if matcher != "manual|auto" {
			t.Errorf("%s matcher = %q; want %q (avoids double-firing on /compact)", event, matcher, "manual|auto")
		}
	}
}

// ---- buildMcpManifest -----------------------------------------------------

func TestBuildMcpManifestShape(t *testing.T) {
	m := buildMcpManifest()
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers missing")
	}
	ogham, ok := servers["ogham"].(map[string]any)
	if !ok {
		t.Fatal("ogham server entry missing")
	}
	cmd, _ := ogham["command"].(string)
	if !strings.HasPrefix(cmd, "${CLAUDE_PLUGIN_ROOT}/") {
		t.Errorf("mcp command = %q; want ${CLAUDE_PLUGIN_ROOT}/ prefix", cmd)
	}
	args, _ := ogham["args"].([]string)
	if len(args) != 1 || args[0] != "serve" {
		t.Errorf("mcp args = %v; want [serve]", args)
	}
}

// ---- buildPluginScaffold (full plan shape) --------------------------------

func TestBuildPluginScaffoldExcludesMcpByDefault(t *testing.T) {
	plan := buildPluginScaffold("/tmp/test-dir", "0.8.0", "sk_test_key", false /* withMcp */)
	if _, exists := plan.Files[".mcp.json"]; exists {
		t.Error(".mcp.json must NOT be in the scaffold unless --with-mcp is explicitly passed (subagent-scope rule)")
	}
	if plan.WithMcp {
		t.Error("plan.WithMcp should be false when withMcp=false")
	}
	// plugin.json + hooks.json must always be present.
	for _, required := range []string{
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join("hooks", "hooks.json"),
	} {
		if _, exists := plan.Files[required]; !exists {
			t.Errorf("scaffold missing required file %s", required)
		}
	}
}

func TestBuildPluginScaffoldIncludesMcpWhenRequested(t *testing.T) {
	plan := buildPluginScaffold("/tmp/test-dir", "0.8.0", "sk_test_key", true /* withMcp */)
	if _, exists := plan.Files[".mcp.json"]; !exists {
		t.Error(".mcp.json must be in the scaffold when --with-mcp=true")
	}
	if !plan.WithMcp {
		t.Error("plan.WithMcp should be true when withMcp=true")
	}
}

func TestBuildPluginScaffoldReportsApiKeyAwareness(t *testing.T) {
	planEmpty := buildPluginScaffold("/tmp/d", "0.8.0", "", false)
	planKeyed := buildPluginScaffold("/tmp/d", "0.8.0", "sk_test", false)

	if planEmpty.ApiKeyKnown {
		t.Error("empty key should report ApiKeyKnown=false")
	}
	if !planKeyed.ApiKeyKnown {
		t.Error("non-empty key should report ApiKeyKnown=true")
	}
}

// ---- claudePluginTargetDir ------------------------------------------------

func TestClaudePluginTargetDirUserScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := claudePluginTargetDir("user", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".claude", "skills", "ogham")
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestClaudePluginTargetDirProjectScope(t *testing.T) {
	tmp := t.TempDir()
	old, _ := os.Getwd()
	defer func() { _ = os.Chdir(old) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	got, err := claudePluginTargetDir("project", "")
	if err != nil {
		t.Fatal(err)
	}
	// macOS resolves /var/... -> /private/var/... via symlink when
	// Getwd() runs but not on the test's `tmp` string. Assert on the
	// invariant: project-scope must end with .claude/skills/ogham and
	// match what Getwd() actually returned at call time.
	wantSuffix := filepath.Join(".claude", "skills", "ogham")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("got %q; want suffix %q", got, wantSuffix)
	}
	cwd, _ := os.Getwd()
	wantPath := filepath.Join(cwd, wantSuffix)
	if got != wantPath {
		t.Errorf("got %q; want %q", got, wantPath)
	}
}

func TestClaudePluginTargetDirOutputOverride(t *testing.T) {
	got, err := claudePluginTargetDir("user", "/explicit/override/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/override/path" {
		t.Errorf("--output override ignored; got %q", got)
	}
}

func TestClaudePluginTargetDirRejectsUnknownScope(t *testing.T) {
	_, err := claudePluginTargetDir("system", "")
	if err == nil {
		t.Error("expected error for unknown scope, got nil")
	}
}

// ---- writeJSONFile --------------------------------------------------------

func TestWriteJSONFileRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.json")
	if err := os.WriteFile(path, []byte(`{"hand": "edited"}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeJSONFile(path, map[string]any{"replaced": true}, false /* force */)
	if err == nil {
		t.Error("expected refusal to overwrite; got nil")
	}

	// Verify the original was untouched.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "hand") {
		t.Errorf("original file was modified despite refusal; got: %s", data)
	}
}

func TestWriteJSONFileOverwritesWithForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(`{"stale": true}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeJSONFile(path, map[string]any{"fresh": true}, true /* force */)
	if err != nil {
		t.Fatalf("force overwrite failed: %v", err)
	}

	var got map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["fresh"] != true {
		t.Errorf("expected fresh content; got %v", got)
	}
}

func TestWriteJSONFileCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "plugin.json")

	err := writeJSONFile(path, map[string]any{"created": true}, false)
	if err != nil {
		t.Fatalf("expected success; got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created at %s: %v", path, err)
	}
}
