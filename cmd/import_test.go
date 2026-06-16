package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUnwrapImportPayload_BareExport ensures that a bare export object
// (the raw shape that ogham-mcp's export_memories() produces internally)
// round-trips through unwrapImportPayload as a JSON string preserving
// the memories list. This is the shape callers get if they hand-craft
// an import file or write the inner JSON directly.
func TestUnwrapImportPayload_BareExport(t *testing.T) {
	t.Parallel()
	input := []byte(`{
  "profile": "work",
  "exported_at": "2026-06-15T14:45:01.965327+00:00",
  "count": 2,
  "memories": [
    {"id": "a", "content": "alpha"},
    {"id": "b", "content": "bravo"}
  ]
}`)
	got, err := unwrapImportPayload(input)
	if err != nil {
		t.Fatalf("unwrapImportPayload: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput=%s", err, got)
	}
	mems, ok := parsed["memories"].([]any)
	if !ok || len(mems) != 2 {
		t.Fatalf("expected 2 memories, got %v", parsed["memories"])
	}
}

// TestUnwrapImportPayload_WrappedEnvelope is the regression test for the
// bug fixed in this change. ogham export writes the export_profile tool
// result (which has data as a JSON string) verbatim to disk, producing
// a {"data": "<json-string>", ...} envelope. Importing such a file used
// to send the whole envelope as an unmarshalled dict to the sidecar,
// which then failed import_memories_tool's data: str validation. The
// unwrapper must peel the inner string out so the value handed to MCP
// is the JSON the sidecar actually wants.
func TestUnwrapImportPayload_WrappedEnvelope(t *testing.T) {
	t.Parallel()
	inner := `{"profile":"work","count":1,"memories":[{"id":"x","content":"hello"}]}`
	wrappedBytes, err := json.Marshal(map[string]any{
		"status":  "exported",
		"profile": "work",
		"format":  "json",
		"data":    inner,
	})
	if err != nil {
		t.Fatalf("build wrapped envelope: %v", err)
	}

	got, err := unwrapImportPayload(wrappedBytes)
	if err != nil {
		t.Fatalf("unwrapImportPayload: %v", err)
	}
	if got != inner {
		t.Fatalf("expected unwrapped inner JSON.\n got: %s\nwant: %s", got, inner)
	}
}

// TestUnwrapImportPayload_InvalidJSON ensures we surface a clear error
// rather than panicking when the file isn't JSON at all.
func TestUnwrapImportPayload_InvalidJSON(t *testing.T) {
	t.Parallel()
	if _, err := unwrapImportPayload([]byte("not json at all")); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	} else if !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected 'not valid JSON' error, got: %v", err)
	}
}

// TestUnwrapImportPayload_EmptyDataField verifies that an envelope with
// data="" is treated as a bare export object rather than a wrapped one.
// The data field is a hint, not a guarantee; if it's empty, fall back
// to re-emitting the whole object as JSON so the sidecar at least sees
// well-formed input.
func TestUnwrapImportPayload_EmptyDataField(t *testing.T) {
	t.Parallel()
	input := []byte(`{"data": "", "memories": []}`)
	got, err := unwrapImportPayload(input)
	if err != nil {
		t.Fatalf("unwrapImportPayload: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := parsed["memories"]; !ok {
		t.Fatalf("expected re-emitted object to retain memories field, got %v", parsed)
	}
}
