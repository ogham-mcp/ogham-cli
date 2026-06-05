package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- resolveInscribeContent -----------------------------------------------

// resetInscribeFlags clears module-level flag state between tests so
// they can't leak into each other.
func resetInscribeFlags() {
	inscribeFile = ""
	inscribeStdin = false
	inscribeTranscriptPath = ""
	inscribeProfile = ""
	inscribeTags = ""
	inscribeSummary = ""
	inscribeSource = ""
	inscribeDryRun = false
}

func TestResolveInscribeContentFromPositionalArgs(t *testing.T) {
	resetInscribeFlags()
	got, err := resolveInscribeContent([]string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q; want %q", got, "hello world")
	}
}

func TestResolveInscribeContentFromFile(t *testing.T) {
	resetInscribeFlags()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "prepared.md")
	if err := os.WriteFile(path, []byte("# Distilled notes\n\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	inscribeFile = path

	got, err := resolveInscribeContent(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Distilled notes") {
		t.Errorf("file content not read; got %q", got)
	}
}

func TestResolveInscribeContentRejectsMultipleSources(t *testing.T) {
	resetInscribeFlags()
	inscribeFile = "/some/file"
	inscribeStdin = true

	_, err := resolveInscribeContent([]string{"positional"})
	if err == nil {
		t.Error("expected error when multiple sources are set, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error message should explain mutual exclusion; got %v", err)
	}
}

func TestResolveInscribeContentErrorsWhenFileMissing(t *testing.T) {
	resetInscribeFlags()
	inscribeFile = "/definitely/does/not/exist/file.md"
	_, err := resolveInscribeContent(nil)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---- extractTranscriptText ------------------------------------------------

func TestExtractTranscriptTextHandlesStringContent(t *testing.T) {
	got := extractTranscriptText("plain text message")
	if got != "plain text message" {
		t.Errorf("got %q; want %q", got, "plain text message")
	}
}

func TestExtractTranscriptTextHandlesTypedBlocks(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "first block"},
		map[string]any{"type": "tool_use", "name": "Bash"},
		map[string]any{"type": "text", "text": "second block"},
	}
	got := extractTranscriptText(content)
	if !strings.Contains(got, "first block") || !strings.Contains(got, "second block") {
		t.Errorf("missing text blocks; got %q", got)
	}
	if strings.Contains(got, "Bash") {
		t.Errorf("tool_use blocks must be skipped; got %q", got)
	}
}

func TestExtractTranscriptTextHandlesUnknownShapes(t *testing.T) {
	got := extractTranscriptText(42)
	if got != "" {
		t.Errorf("unknown shape should return empty string; got %q", got)
	}
}

// ---- readClaudeCodeTranscript --------------------------------------------

func TestReadClaudeCodeTranscriptExtractsUserAssistant(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.jsonl")
	// Realistic-ish Claude Code transcript shape: one JSON object per
	// line, each with a discriminator type and a message payload.
	transcript := `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi back"},{"type":"tool_use","name":"Bash"}]}}
{"type":"system","message":{"role":"system","content":"don't include me"}}
`
	if err := os.WriteFile(path, []byte(transcript), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := readClaudeCodeTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("user content missing; got %q", got)
	}
	if !strings.Contains(got, "hi back") {
		t.Errorf("assistant text block missing; got %q", got)
	}
	if strings.Contains(got, "don't include me") {
		t.Errorf("system role must be excluded; got %q", got)
	}
	if strings.Contains(got, "Bash") {
		t.Errorf("tool_use blocks must be skipped; got %q", got)
	}
	if !strings.Contains(got, "## user") || !strings.Contains(got, "## assistant") {
		t.Errorf("role headers missing; got %q", got)
	}
}

func TestReadClaudeCodeTranscriptSkipsMalformedLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "messy.jsonl")
	// Malformed lines must not abort the whole read -- partial content
	// beats no content.
	content := `{"type":"user","message":{"role":"user","content":"good line"}}
not actually json
{"missing":"message field"}
{"type":"assistant","message":{"role":"assistant","content":"also good"}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := readClaudeCodeTranscript(path)
	if err != nil {
		t.Fatalf("malformed lines must not abort read; got error %v", err)
	}
	if !strings.Contains(got, "good line") || !strings.Contains(got, "also good") {
		t.Errorf("valid lines missing; got %q", got)
	}
}

func TestReadClaudeCodeTranscriptErrorsOnMissingFile(t *testing.T) {
	_, err := readClaudeCodeTranscript("/definitely/missing/transcript.jsonl")
	if err == nil {
		t.Error("expected error for missing transcript path")
	}
}
