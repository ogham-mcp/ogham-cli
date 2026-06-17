package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildImportToolArgsSendsString is the regression test for
// ogham-cli issue #20: the Python import_memories_tool signature is
// `data: str`, and FastMCP's Pydantic validator rejects a parsed dict
// before the function body runs. We must send the JSON STRING, not the
// parsed object.
func TestBuildImportToolArgsSendsString(t *testing.T) {
	raw := []byte(`{"memories":[{"content":"hello","tags":["x"]}]}`)

	args, err := buildImportToolArgs(raw, 0.8)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dataField, ok := args["data"]
	if !ok {
		t.Fatal("toolArgs missing required 'data' field")
	}
	got, ok := dataField.(string)
	if !ok {
		t.Fatalf("data must be string (Python expects `data: str`); got %T", dataField)
	}
	if got != string(raw) {
		t.Errorf("data field should be the raw JSON string verbatim; got %q", got)
	}

	dedup, ok := args["dedup_threshold"].(float64)
	if !ok {
		t.Fatalf("dedup_threshold must be float64; got %T", args["dedup_threshold"])
	}
	if dedup != 0.8 {
		t.Errorf("dedup_threshold = %v; want 0.8", dedup)
	}

	// No phantom 'profile' field: Python's import_memories_tool doesn't
	// accept one. Profile selection is plumbed via OGHAM_PROFILE env on
	// the sidecar process, not the tool args.
	if _, present := args["profile"]; present {
		t.Error("toolArgs must not include 'profile' -- Python tool has no such parameter")
	}
}

// TestBuildImportToolArgsUnwrapsLegacyEnvelope covers the case where a
// user has a backup file produced by the pre-fix `ogham export -o`,
// which wrote the entire MCP envelope to disk instead of the inner
// payload. We tolerate both shapes so old backups still import.
func TestBuildImportToolArgsUnwrapsLegacyEnvelope(t *testing.T) {
	inner := `{"memories":[{"content":"from legacy backup"}]}`
	envelope := []byte(`{"status":"exported","profile":"default","format":"json","data":` +
		quoteJSON(inner) + `}`)

	args, err := buildImportToolArgs(envelope, 0.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := args["data"].(string)
	if got != inner {
		t.Errorf("expected unwrapped inner payload\n  got:  %q\n  want: %q", got, inner)
	}
}

// TestBuildImportToolArgsPassthroughNonEnvelope makes sure we don't
// accidentally unwrap files that happen to have a "data" key but
// aren't envelopes (e.g. a memory record whose content references
// "data").
func TestBuildImportToolArgsPassthroughNonEnvelope(t *testing.T) {
	raw := []byte(`{"data":"not-an-envelope","memories":[]}`)

	args, err := buildImportToolArgs(raw, 0.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := args["data"].(string)
	if got != string(raw) {
		t.Errorf("non-envelope JSON should pass through verbatim; got %q", got)
	}
}

func TestBuildImportToolArgsRejectsInvalidJSON(t *testing.T) {
	_, err := buildImportToolArgs([]byte("not json"), 0.0)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error should mention invalid JSON; got: %v", err)
	}
}

// TestUnwrapExportPayloadReturnsInner verifies that the export command
// writes the actual export blob to disk, not the MCP envelope wrapper.
// Without this, `ogham export -o f.json && ogham import f.json` would
// silently no-op (Python json.loads sees the envelope, finds no
// 'memories' key, returns imported=0).
func TestUnwrapExportPayloadReturnsInner(t *testing.T) {
	inner := `{"memories":[{"content":"a"},{"content":"b"}]}`
	envelope := map[string]any{
		"status":  "exported",
		"profile": "default",
		"format":  "json",
		"data":    inner,
	}

	got, err := unwrapExportPayload(envelope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != inner {
		t.Errorf("got %q; want %q", got, inner)
	}
}

func TestUnwrapExportPayloadHandlesMarkdownString(t *testing.T) {
	// Markdown format puts a markdown document in `data`, still a string.
	envelope := map[string]any{
		"status":  "exported",
		"profile": "default",
		"format":  "markdown",
		"data":    "# Memories\n\n- one\n- two\n",
	}
	got, err := unwrapExportPayload(envelope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "# Memories") {
		t.Errorf("expected markdown body; got %q", got)
	}
}

func TestUnwrapExportPayloadRejectsWrongShape(t *testing.T) {
	_, err := unwrapExportPayload("not a map")
	if err == nil {
		t.Fatal("expected error for non-map payload, got nil")
	}
}

func TestUnwrapExportPayloadRejectsMissingData(t *testing.T) {
	_, err := unwrapExportPayload(map[string]any{"status": "exported"})
	if err == nil {
		t.Fatal("expected error for envelope without data field, got nil")
	}
}

// quoteJSON returns s as a JSON string literal (escaped + double-quoted).
// Used to build envelope test fixtures whose 'data' field is a JSON
// string containing escaped JSON -- the actual on-disk shape pre-fix.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
