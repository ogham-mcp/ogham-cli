package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// Argument-boundary tests for the v0.7 Batch A tool handlers. The
// handlers route into native.*, which needs a real DB; those paths are
// covered by the live-tagged smoke + CLI integration tests. What this
// file guards is the cheap boundary: missing/malformed args must surface
// as CallToolResult.IsError=true without a transport error.

func callHandler(t *testing.T, h mcp.ToolHandler, args string) *mcp.CallToolResult {
	t.Helper()
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Arguments: json.RawMessage(args),
	}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	return res
}

func errorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected IsError=true; got %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("error result has no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("error content not TextContent: %T", res.Content[0])
	}
	return tc.Text
}

func TestDeleteHandler_RequiresID(t *testing.T) {
	// Empty cfg is fine -- we short-circuit on the empty id before
	// touching the DB.
	h := BuildNativeDeleteHandler(&native.Config{})
	res := callHandler(t, h, `{}`)
	if txt := errorText(t, res); !strings.Contains(txt, "id is required") {
		t.Errorf("want 'id is required' in %q", txt)
	}
}

func TestDeleteHandler_MalformedArgs(t *testing.T) {
	h := BuildNativeDeleteHandler(&native.Config{})
	res := callHandler(t, h, `not json`)
	if txt := errorText(t, res); !strings.Contains(txt, "parse arguments") {
		t.Errorf("want parse error in %q", txt)
	}
}

func TestSetProfileTTLHandler_RequiresProfile(t *testing.T) {
	h := BuildNativeSetProfileTTLHandler(&native.Config{})
	res := callHandler(t, h, `{"ttl_days":7}`)
	if txt := errorText(t, res); !strings.Contains(txt, "profile is required") {
		t.Errorf("want 'profile is required' in %q", txt)
	}
}

func TestSetProfileTTLHandler_ClearOverridesTTL(t *testing.T) {
	// clear=true must win over ttl_days so callers toggling the flag
	// don't accidentally keep a stale ttl. We can't verify the DB side
	// without a live backend, but we can confirm the argument path
	// doesn't error out on the short-circuits before hitting native.
	//
	// The handler will hit ResolveBackend() with an empty config, which
	// returns an error -- that error surfaces as IsError=true. We just
	// verify it's NOT the "profile is required" error, proving that
	// clear + ttl_days path got through the argument validation.
	h := BuildNativeSetProfileTTLHandler(&native.Config{})
	res := callHandler(t, h, `{"profile":"work","ttl_days":7,"clear":true}`)
	txt := errorText(t, res)
	if strings.Contains(txt, "profile is required") {
		t.Errorf("should have passed arg validation; got %q", txt)
	}
}

func TestListProfilesHandler_EmptyArgsOK(t *testing.T) {
	// list_profiles takes no arguments. An empty payload must not fail
	// on argument parsing -- only on the downstream DB call.
	h := BuildNativeListProfilesHandler(&native.Config{})
	res := callHandler(t, h, ``)
	// Empty config -> native.ListProfiles returns a backend-resolution
	// error. We just verify it's not a parse error.
	txt := errorText(t, res)
	if strings.Contains(txt, "parse arguments") {
		t.Errorf("empty args should parse cleanly; got %q", txt)
	}
}

func TestCleanupHandler_EmptyArgsUsesConfigProfile(t *testing.T) {
	h := BuildNativeCleanupHandler(&native.Config{})
	res := callHandler(t, h, `{}`)
	txt := errorText(t, res)
	if strings.Contains(txt, "parse arguments") {
		t.Errorf("empty args should parse cleanly; got %q", txt)
	}
}
