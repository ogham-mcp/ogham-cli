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

// --- reinforce / contradict strength range tests --------------------------
//
// Parity with Python (src/ogham/tools/memory.py):
//   reinforce: 0 < strength <= 1.0   (exclusive lower, inclusive upper)
//   contradict: 0 <= strength < 1.0  (inclusive lower, exclusive upper)
//
// The boundaries matter: a caller passing strength=0 to reinforce or
// strength=1.0 to contradict would silently coerce in Python. We reject
// with a clear message instead.

func TestReinforceHandler_RequiresID(t *testing.T) {
	h := BuildNativeReinforceHandler(&native.Config{})
	res := callHandler(t, h, `{}`)
	if txt := errorText(t, res); !strings.Contains(txt, "memory_id is required") {
		t.Errorf("want 'memory_id is required' in %q", txt)
	}
}

func TestReinforceHandler_RejectsStrengthOutOfRange(t *testing.T) {
	h := BuildNativeReinforceHandler(&native.Config{})
	// strength=1.1 is above the inclusive upper bound.
	res := callHandler(t, h, `{"memory_id":"abc","strength":1.1}`)
	if txt := errorText(t, res); !strings.Contains(txt, "strength must be in (0.0, 1.0]") {
		t.Errorf("want strength-range error; got %q", txt)
	}
	// strength=-0.5 is below the exclusive lower bound.
	res = callHandler(t, h, `{"memory_id":"abc","strength":-0.5}`)
	if txt := errorText(t, res); !strings.Contains(txt, "strength must be in (0.0, 1.0]") {
		t.Errorf("want strength-range error; got %q", txt)
	}
}

func TestReinforceHandler_DefaultStrengthSkipsValidation(t *testing.T) {
	// strength omitted -> should default to 0.85 and proceed into the DB
	// path (which errors because empty cfg has no backend). We just verify
	// we don't block on strength validation.
	h := BuildNativeReinforceHandler(&native.Config{})
	res := callHandler(t, h, `{"memory_id":"abc"}`)
	txt := errorText(t, res)
	if strings.Contains(txt, "strength must be in") {
		t.Errorf("default strength should pass validation; got %q", txt)
	}
}

func TestContradictHandler_RequiresID(t *testing.T) {
	h := BuildNativeContradictHandler(&native.Config{})
	res := callHandler(t, h, `{}`)
	if txt := errorText(t, res); !strings.Contains(txt, "memory_id is required") {
		t.Errorf("want 'memory_id is required' in %q", txt)
	}
}

func TestContradictHandler_RejectsStrengthAtUpperBound(t *testing.T) {
	// 1.0 is the exclusive upper bound for contradict (Python rejects it).
	h := BuildNativeContradictHandler(&native.Config{})
	res := callHandler(t, h, `{"memory_id":"abc","strength":1.0}`)
	if txt := errorText(t, res); !strings.Contains(txt, "strength must be in [0.0, 1.0)") {
		t.Errorf("want strength-range error; got %q", txt)
	}
}

func TestContradictHandler_AcceptsZeroStrengthExplicitly(t *testing.T) {
	// strength=0.0 is the inclusive lower bound. With the JSON default
	// zero-value we can't distinguish "unset" from "explicit 0", so we
	// treat a zero as "use default" -- Python's behaviour (Python uses
	// None default; Go's zero-value pattern collapses to the same thing).
	// This test confirms that doesn't spuriously fail with an out-of-range
	// message. The downstream DB call will error on empty cfg, but the
	// strength path must be clean.
	h := BuildNativeContradictHandler(&native.Config{})
	res := callHandler(t, h, `{"memory_id":"abc","strength":0.0}`)
	txt := errorText(t, res)
	if strings.Contains(txt, "strength must be in") {
		t.Errorf("zero strength should default, not fail validation; got %q", txt)
	}
}
