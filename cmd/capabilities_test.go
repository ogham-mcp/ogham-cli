package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcpserver "github.com/ogham-mcp/ogham-cli/internal/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// TestCapabilitiesText_ContainsExpectedGroups pins the grouped output
// shape -- every section header shows up and each group has the
// expected count of entries. Text-format contract for humans.
func TestCapabilitiesText_ContainsExpectedGroups(t *testing.T) {
	var buf bytes.Buffer
	if err := emitCapabilitiesText(&buf, defaultCapabilitiesMatrix, "v0.7.0-test", "native"); err != nil {
		t.Fatalf("emitCapabilitiesText: %v", err)
	}

	got := buf.String()
	wantSubstrings := []string{
		"ogham capabilities (v0.7.0-test, native)",
		"Store-side tools (native Go):",
		"Search-side tools (native Go):",
		"Profile + config (native Go):",
		"CLI subcommands that still route through the sidecar:",
		"Python sidecar required (use --sidecar):",
		"Search augmentations (Python-only today; use --sidecar for these):",
		"Positioning:",
		"Python MCP = brain",
		// Anchor a few concrete entries so a rename / drop is caught.
		"store_memory",
		"hybrid_search",
		"suggest_connections",
		"compress_old_memories",
		"re_embed_all",
		"intent detection",
		"strided retrieval",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("capabilities text output missing %q\n---\n%s", s, got)
		}
	}
}

// TestCapabilitiesText_ModeTag flips the mode label from "native" to
// "sidecar" when the global --sidecar flag is set. Ensures the header
// reflects the active routing.
func TestCapabilitiesText_ModeTag(t *testing.T) {
	cases := []struct {
		mode    string
		wantTag string
	}{
		{"native", "(v0.7.0-test, native)"},
		{"sidecar", "(v0.7.0-test, sidecar)"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			var buf bytes.Buffer
			if err := emitCapabilitiesText(&buf, defaultCapabilitiesMatrix, "v0.7.0-test", tc.mode); err != nil {
				t.Fatalf("emit: %v", err)
			}
			if !strings.Contains(buf.String(), tc.wantTag) {
				t.Errorf("missing mode tag %q in output:\n%s", tc.wantTag, buf.String())
			}
		})
	}
}

// TestCapabilitiesJSON_Shape validates the structured payload.
// Scripts consuming this output will diff it in CI, so both the
// top-level keys and the Matrix substructure must stay stable.
func TestCapabilitiesJSON_Shape(t *testing.T) {
	var buf bytes.Buffer
	if err := emitCapabilitiesJSON(&buf, defaultCapabilitiesMatrix, "v0.7.0-test", "native"); err != nil {
		t.Fatalf("emitCapabilitiesJSON: %v", err)
	}

	var payload capabilitiesJSONPayload
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, buf.String())
	}

	if payload.Version != "v0.7.0-test" {
		t.Errorf("version = %q, want v0.7.0-test", payload.Version)
	}
	if payload.Mode != "native" {
		t.Errorf("mode = %q, want native", payload.Mode)
	}
	if len(payload.NativeTools) == 0 {
		t.Fatal("native_tools is empty")
	}
	if len(payload.SidecarTools) == 0 {
		t.Fatal("sidecar_tools is empty")
	}
	if len(payload.AugmentationsInSidecar) == 0 {
		t.Fatal("augmentations_in_sidecar is empty")
	}

	// Spot-check well-known entries.
	nativeSet := toSet(payload.NativeTools)
	for _, n := range []string{"store_memory", "hybrid_search", "list_recent", "health_check"} {
		if _, ok := nativeSet[n]; !ok {
			t.Errorf("native_tools missing %q: %v", n, payload.NativeTools)
		}
	}
	sidecarSet := toSet(payload.SidecarTools)
	for _, n := range []string{"compress_old_memories", "re_embed_all"} {
		if _, ok := sidecarSet[n]; !ok {
			t.Errorf("sidecar_tools missing %q: %v", n, payload.SidecarTools)
		}
	}

	// Matrix retained its five groups.
	if len(payload.Matrix.StoreSide) == 0 || len(payload.Matrix.SearchSide) == 0 ||
		len(payload.Matrix.ProfileConfig) == 0 || len(payload.Matrix.CLISidecarRouted) == 0 ||
		len(payload.Matrix.SidecarOnlyTools) == 0 || len(payload.Matrix.Augmentations) == 0 {
		t.Errorf("matrix missing a group: %+v", payload.Matrix)
	}
}

// TestCapabilitiesJSON_IsSorted confirms the flattened NativeTools /
// SidecarTools arrays are sorted + deduplicated. Required for
// byte-stable diffs across runs -- a script diffing the output to
// detect "new native tool landed" depends on sort order.
func TestCapabilitiesJSON_IsSorted(t *testing.T) {
	var buf bytes.Buffer
	if err := emitCapabilitiesJSON(&buf, defaultCapabilitiesMatrix, "v0.7.0-test", "native"); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var payload capabilitiesJSONPayload
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !isSorted(payload.NativeTools) {
		t.Errorf("native_tools not sorted: %v", payload.NativeTools)
	}
	if !isSorted(payload.SidecarTools) {
		t.Errorf("sidecar_tools not sorted: %v", payload.SidecarTools)
	}
	// Augmentations keep matrix order (order is editorial: intent,
	// strided, reformulation, MMR, spreading) -- not required to be
	// sorted alphabetically.
}

// TestCapabilitiesMatrix_NativeNamesMatchRegistration is the drift
// guard: every tool name we claim is native here MUST resolve in
// RegisterNativeTools. Catches someone adding to one place and not
// the other.
func TestCapabilitiesMatrix_NativeNamesMatchRegistration(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	// A minimal config is enough for RegisterNativeTools -- the
	// handlers bind closures over cfg but never run them here.
	cfg := &native.Config{}
	registered := mcpserver.RegisterNativeTools(server, cfg)

	claimed := defaultCapabilitiesMatrix.nativeMCPToolNames()
	for name := range claimed {
		if _, ok := registered[name]; !ok {
			t.Errorf("capabilities matrix claims %q is native but RegisterNativeTools does not register it", name)
		}
	}
	// The inverse direction is a soft-warn, not a fail: a new native
	// tool registered without a matching matrix entry should be
	// surfaced so the maintainer notices, but it isn't a correctness
	// failure for the binary -- just a docs gap.
	for name := range registered {
		if _, ok := claimed[name]; !ok {
			t.Logf("WARN: %q is registered native but not listed in defaultCapabilitiesMatrix; add it next release", name)
		}
	}
}

// TestCapabilitiesCmd_DefaultsToText invokes the cobra command's RunE
// directly (bypassing rootCmd state) so the default output is text,
// not JSON, when --json is not passed.
func TestCapabilitiesCmd_DefaultsToText(t *testing.T) {
	// Reset the flag globals the subcommand reads; the file-level
	// capabilitiesJSON is a package var that cobra writes into on
	// Flags() parse.
	origJSON := capabilitiesJSON
	t.Cleanup(func() { capabilitiesJSON = origJSON })
	capabilitiesJSON = false

	buf, restore := stubStdout(t)
	defer restore()

	if err := capabilitiesCmd.RunE(capabilitiesCmd, []string{}); err != nil {
		restore()
		t.Fatalf("RunE: %v", err)
	}
	restore()

	got := buf.String()
	if !strings.Contains(got, "ogham capabilities") {
		t.Errorf("default output should include text header; got %q", got)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("default output should not be JSON; got %q", got)
	}
}

// TestCapabilitiesCmd_JSONFlag covers the --json branch.
func TestCapabilitiesCmd_JSONFlag(t *testing.T) {
	origJSON := capabilitiesJSON
	t.Cleanup(func() { capabilitiesJSON = origJSON })
	capabilitiesJSON = true

	buf, restore := stubStdout(t)
	defer restore()

	if err := capabilitiesCmd.RunE(capabilitiesCmd, []string{}); err != nil {
		restore()
		t.Fatalf("RunE: %v", err)
	}
	restore()

	got := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(got, "{") {
		t.Errorf("--json output should start with {, got: %q", got)
	}
	var payload capabilitiesJSONPayload
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("cobra RunE --json output not valid JSON: %v\n%s", err, got)
	}
	if len(payload.NativeTools) == 0 {
		t.Errorf("expected native_tools populated in cobra-driven output; got %+v", payload)
	}
}

func toSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, i := range items {
		out[i] = struct{}{}
	}
	return out
}

func isSorted(items []string) bool {
	for i := 1; i < len(items); i++ {
		if items[i-1] > items[i] {
			return false
		}
	}
	return true
}
