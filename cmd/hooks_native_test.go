package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildPostToolContentBash covers the Bash branch -- target is
// the command string itself; content includes the command + truncated
// response.
func TestBuildPostToolContentBash(t *testing.T) {
	input := map[string]any{"command": "git commit -m 'fix'"}
	content, target := buildPostToolContent("Bash", input, "[main abc1234] fix")
	if !strings.Contains(content, "Bash: git commit") {
		t.Errorf("content missing Bash prefix + command, got %q", content)
	}
	if !strings.Contains(content, "[main abc1234]") {
		t.Errorf("content missing response, got %q", content)
	}
	if target != "git commit -m 'fix'" {
		t.Errorf("target = %q, want full command", target)
	}
}

func TestBuildPostToolContentBashEmptyCommand(t *testing.T) {
	content, target := buildPostToolContent("Bash", map[string]any{}, "")
	if content != "" || target != "" {
		t.Errorf("empty Bash should produce empty content/target, got %q/%q", content, target)
	}
}

func TestBuildPostToolContentEdit(t *testing.T) {
	input := map[string]any{"file_path": "/repo/foo.go"}
	content, target := buildPostToolContent("Edit", input, "")
	if !strings.HasPrefix(content, "Edit: ") {
		t.Errorf("content = %q, want 'Edit: ...' prefix", content)
	}
	if target != "/repo/foo.go" {
		t.Errorf("target = %q, want file path", target)
	}
}

func TestBuildPostToolContentWrite(t *testing.T) {
	input := map[string]any{"file_path": "/repo/new.go"}
	content, target := buildPostToolContent("Write", input, "")
	if !strings.HasPrefix(content, "Write: ") {
		t.Errorf("content = %q, want 'Write: ...' prefix", content)
	}
	if target != "/repo/new.go" {
		t.Errorf("target = %q, want file path", target)
	}
}

func TestBuildPostToolContentUnknownTool(t *testing.T) {
	content, target := buildPostToolContent("Unknown", map[string]any{}, "")
	if content != "" || target != "" {
		t.Errorf("unknown tool should produce empty content/target, got %q/%q", content, target)
	}
}

// TestReadToolResponseTriesMultipleFieldNames documents the Claude
// Code field-name drift across versions: tool_response, response,
// tool_output, output. First non-empty wins.
func TestReadToolResponseTriesMultipleFieldNames(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"tool_response", map[string]any{"tool_response": "A"}, "A"},
		{"response", map[string]any{"response": "B"}, "B"},
		{"tool_output", map[string]any{"tool_output": "C"}, "C"},
		{"output", map[string]any{"output": "D"}, "D"},
		{"none", map[string]any{}, ""},
		{
			"first wins",
			map[string]any{"tool_response": "first", "response": "second"},
			"first",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readToolResponse(tc.input)
			if got != tc.want {
				t.Errorf("readToolResponse(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadToolResponseTruncates(t *testing.T) {
	large := strings.Repeat("x", 3000)
	out := readToolResponse(map[string]any{"tool_response": large})
	if len(out) != 2000 {
		t.Errorf("expected truncation to 2000 chars, got %d", len(out))
	}
}

// TestRunNativePostToolQueuesBashEvent end-to-end: invokes
// runNativePostTool against a temp-dir outbox and verifies the
// queued .jsonl file deserialises into a Record with the expected
// shape (content masked, tags set, session/tool/cwd preserved).
func TestRunNativePostToolQueuesBashEvent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)

	input := map[string]any{
		"tool_name":  "Bash",
		"session_id": "session-xyz",
		"cwd":        "/tmp/proj",
		"tool_input": map[string]any{
			"command": "git push origin main",
		},
		"tool_response": "Everything up-to-date",
	}

	if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
		t.Fatalf("runNativePostTool: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 queued file, got %d", len(files))
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rec struct {
		Content   string   `json:"content"`
		Profile   string   `json:"profile"`
		Source    string   `json:"source"`
		Tags      []string `json:"tags"`
		SessionID string   `json:"session_id"`
		ToolName  string   `json:"tool_name"`
		Cwd       string   `json:"cwd"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(rec.Content, "git push origin main") {
		t.Errorf("content missing command, got %q", rec.Content)
	}
	if rec.Profile != "work" {
		t.Errorf("Profile = %q, want work", rec.Profile)
	}
	if rec.Source != "hook:post-tool" {
		t.Errorf("Source = %q, want hook:post-tool", rec.Source)
	}
	if rec.SessionID != "session-xyz" || rec.ToolName != "Bash" || rec.Cwd != "/tmp/proj" {
		t.Errorf("provenance lost, got %+v", rec)
	}
	hasAction, hasTool, hasSession := false, false, false
	for _, tag := range rec.Tags {
		switch tag {
		case "type:action":
			hasAction = true
		case "tool:Bash":
			hasTool = true
		case "session:session-xyz":
			hasSession = true
		}
	}
	if !hasAction || !hasTool || !hasSession {
		t.Errorf("missing tags, got %v", rec.Tags)
	}
}

// TestRunNativePostToolSkipsAlwaysSkipTool: Classify => SkipAlwaysSkip
// must short-circuit before the outbox is touched. No file written.
func TestRunNativePostToolSkipsAlwaysSkipTool(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)

	input := map[string]any{
		"tool_name":  "Read",
		"session_id": "session-skip",
		"tool_input": map[string]any{"file_path": "/etc/passwd"},
	}
	if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
		t.Fatalf("runNativePostTool: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 0 {
		t.Errorf("expected 0 files for SkipAlwaysSkip verdict, got %d", len(files))
	}
}

// TestRunNativePostToolMasksSecretsInBashCommand verifies the
// filters.MaskSecrets wire-up actually substitutes secrets in the
// queued payload.
func TestRunNativePostToolMasksSecretsInBashCommand(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)

	input := map[string]any{
		"tool_name":  "Bash",
		"session_id": "session-secret",
		"tool_input": map[string]any{
			"command": "export GITHUB_TOKEN=ghp_" + strings.Repeat("A", 36),
		},
	}
	if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
		t.Fatalf("runNativePostTool: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if strings.Contains(string(data), "ghp_AAAA") {
		t.Errorf("raw GitHub PAT survived masking: %s", data)
	}
	if !strings.Contains(string(data), "MASKED") {
		t.Errorf("no mask substitution applied: %s", data)
	}
}
