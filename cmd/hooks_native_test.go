package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildPostToolContentBash covers the Bash branch. Content is the
// command and nothing else -- see
// TestBuildPostToolContentBashNeverIncludesOutput for why.
func TestBuildPostToolContentBash(t *testing.T) {
	input := map[string]any{"command": "git commit -m 'fix'"}
	content, target := buildPostToolContent("Bash", input, toolOutcome{Known: true, Failed: false})
	if content != "Bash: git commit -m 'fix'" {
		t.Errorf("content = %q, want the bare command", content)
	}
	if target != "git commit -m 'fix'" {
		t.Errorf("target = %q, want full command", target)
	}
}

// TestBuildPostToolContentBashNeverIncludesOutput is the regression
// test for #26 finding 1. The previous implementation appended up to
// 2000 chars of raw tool response to the memory. Storing command
// output is the defect TBU-231 item 2 forbids: memories should carry
// the command and a derived outcome, never the payload.
func TestBuildPostToolContentBashNeverIncludesOutput(t *testing.T) {
	input := map[string]any{"command": "cat /etc/hosts"}
	for _, oc := range []toolOutcome{
		{Known: true, Failed: false},
		{Known: true, Failed: true},
		{Known: false},
	} {
		content, _ := buildPostToolContent("Bash", input, oc)
		if strings.Contains(content, "127.0.0.1") || strings.Contains(content, "stdout") {
			t.Errorf("outcome %+v leaked output into content: %q", oc, content)
		}
		if !strings.Contains(content, "cat /etc/hosts") {
			t.Errorf("outcome %+v lost the command: %q", oc, content)
		}
	}
}

// TestBuildPostToolContentBashMarksFailure covers #26 finding 3: the
// derived outcome has to be visible in the memory, since the raw
// output no longer is.
func TestBuildPostToolContentBashMarksFailure(t *testing.T) {
	input := map[string]any{"command": "go build ./..."}

	failed, _ := buildPostToolContent("Bash", input, toolOutcome{Known: true, Failed: true})
	if !strings.Contains(failed, "failed") {
		t.Errorf("failed command not marked, got %q", failed)
	}

	ok, _ := buildPostToolContent("Bash", input, toolOutcome{Known: true, Failed: false})
	if strings.Contains(ok, "failed") {
		t.Errorf("successful command marked as failed, got %q", ok)
	}

	unknown, _ := buildPostToolContent("Bash", input, toolOutcome{Known: false})
	if strings.Contains(unknown, "failed") {
		t.Errorf("unknown outcome must not claim failure, got %q", unknown)
	}
}

func TestBuildPostToolContentBashEmptyCommand(t *testing.T) {
	content, target := buildPostToolContent("Bash", map[string]any{}, toolOutcome{})
	if content != "" || target != "" {
		t.Errorf("empty Bash should produce empty content/target, got %q/%q", content, target)
	}
}

func TestBuildPostToolContentEdit(t *testing.T) {
	input := map[string]any{"file_path": "/repo/foo.go"}
	content, target := buildPostToolContent("Edit", input, toolOutcome{})
	if !strings.HasPrefix(content, "Edit: ") {
		t.Errorf("content = %q, want 'Edit: ...' prefix", content)
	}
	if target != "/repo/foo.go" {
		t.Errorf("target = %q, want file path", target)
	}
}

func TestBuildPostToolContentWrite(t *testing.T) {
	input := map[string]any{"file_path": "/repo/new.go"}
	content, target := buildPostToolContent("Write", input, toolOutcome{})
	if !strings.HasPrefix(content, "Write: ") {
		t.Errorf("content = %q, want 'Write: ...' prefix", content)
	}
	if target != "/repo/new.go" {
		t.Errorf("target = %q, want file path", target)
	}
}

func TestBuildPostToolContentUnknownTool(t *testing.T) {
	content, target := buildPostToolContent("Unknown", map[string]any{}, toolOutcome{})
	if content != "" || target != "" {
		t.Errorf("unknown tool should produce empty content/target, got %q/%q", content, target)
	}
}

// TestReadToolOutcomeReadsIsErrorFromObject covers #26 findings 2 + 3.
// Claude Code sends a Bash tool_response as an object carrying
// is_error; the previous readToolResponse type-asserted to string, so
// the object fell through and no outcome was ever read.
func TestReadToolOutcomeReadsIsErrorFromObject(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  toolOutcome
	}{
		{
			"failure",
			map[string]any{"tool_response": map[string]any{
				"stdout": "", "stderr": "boom", "is_error": true,
			}},
			toolOutcome{Known: true, Failed: true},
		},
		{
			"success",
			map[string]any{"tool_response": map[string]any{
				"stdout": "ok", "stderr": "", "is_error": false,
			}},
			toolOutcome{Known: true, Failed: false},
		},
		{
			"camelCase variant",
			map[string]any{"tool_response": map[string]any{"isError": true}},
			toolOutcome{Known: true, Failed: true},
		},
		{
			"field-name drift: response",
			map[string]any{"response": map[string]any{"is_error": true}},
			toolOutcome{Known: true, Failed: true},
		},
		{
			"object without is_error",
			map[string]any{"tool_response": map[string]any{"stdout": "hi"}},
			toolOutcome{Known: false},
		},
		{"absent", map[string]any{}, toolOutcome{Known: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readToolOutcome(tc.input)
			if got != tc.want {
				t.Errorf("readToolOutcome(%v) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

// TestReadToolOutcomeNeverInfersFailureFromText is TBU-231 defect 2 in
// regression form. The Python hook matched `\b\w*(?:Error|Exception)\b`
// against the stringified response and hit the envelope's own
// `is_error` field name, classifying every success as an error. Outcome
// must come from a structured field or not at all -- never from
// pattern-matching the payload.
func TestReadToolOutcomeNeverInfersFailureFromText(t *testing.T) {
	for _, text := range []string{
		"{'stdout': '', 'stderr': '', 'is_error': False}",
		"ValueError: something went wrong",
		"error: pathspec 'nope' did not match",
	} {
		got := readToolOutcome(map[string]any{"tool_response": text})
		if got.Known || got.Failed {
			t.Errorf("string response %q produced %+v, want an unknown outcome", text, got)
		}
	}
}

// TestRunNativePostToolQueuesBashEvent end-to-end: invokes
// runNativePostTool against a temp-dir outbox and verifies the
// queued .jsonl file deserialises into a Record with the expected
// shape (content masked, tags set, session/tool/cwd preserved).
func TestRunNativePostToolQueuesBashEvent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

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
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

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
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

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

// --- End-to-end regressions for #26 findings 1-4 ---------------------

// TestRunNativePostToolNeverQueuesRawOutput drives the whole path with
// the object-shaped tool_response Claude Code actually sends for Bash,
// and asserts no fragment of stdout or stderr reaches the outbox file.
// Finding 1 was latent behind finding 2's type assertion; this covers
// both, so teaching readToolOutcome to parse objects cannot re-arm it.
func TestRunNativePostToolNeverQueuesRawOutput(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

	input := map[string]any{
		"tool_name":  "Bash",
		"session_id": "session-raw",
		"tool_input": map[string]any{"command": "cat /etc/hosts"},
		"tool_response": map[string]any{
			"stdout":   "127.0.0.1 localhost\nSUPER-SECRET-LINE\n",
			"stderr":   "a warning on stderr",
			"is_error": false,
		},
	}
	if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
		t.Fatalf("runNativePostTool: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 queued file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	for _, leak := range []string{"127.0.0.1", "SUPER-SECRET-LINE", "a warning on stderr", "stdout"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("raw output %q reached the outbox: %s", leak, data)
		}
	}
	if !strings.Contains(string(data), "cat /etc/hosts") {
		t.Errorf("command lost from queued record: %s", data)
	}
}

// TestRunNativePostToolRecordsFailureOutcome covers finding 3
// end-to-end: is_error must survive into the queued record, since the
// output it was previously inferred from is gone.
func TestRunNativePostToolRecordsFailureOutcome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

	input := map[string]any{
		"tool_name":  "Bash",
		"session_id": "session-fail",
		"tool_input": map[string]any{"command": "go build ./..."},
		"tool_response": map[string]any{
			"stdout": "", "stderr": "undefined: foo", "is_error": true,
		},
	}
	if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
		t.Fatalf("runNativePostTool: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 queued file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	var rec struct {
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Contains(string(data), "undefined: foo") {
		t.Errorf("stderr leaked into the record: %s", data)
	}
	if !strings.Contains(rec.Content, "failed") {
		t.Errorf("content does not record the failure: %q", rec.Content)
	}
	hasOutcome := false
	for _, tag := range rec.Tags {
		if tag == "outcome:error" {
			hasOutcome = true
		}
	}
	if !hasOutcome {
		t.Errorf("missing outcome:error tag, got %v", rec.Tags)
	}
}

// TestRunNativePostToolDedupesAcrossProcesses is finding 4 at the hook
// boundary. Each call models a separate `ogham hooks run post-tool`
// process; only the first should queue a record.
func TestRunNativePostToolDedupesAcrossProcesses(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

	input := map[string]any{
		"tool_name":  "Edit",
		"session_id": "session-dupe",
		"tool_input": map[string]any{"file_path": "/repo/MEMORY.md"},
	}
	for i := 0; i < 3; i++ {
		if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 1 {
		t.Errorf("expected 1 queued file after 3 identical events, got %d", len(files))
	}
}

// TestRunNativePostToolDistinctTargetsBothQueue guards the dedupe wiring
// against over-suppression -- different files in one session must both
// be captured.
func TestRunNativePostToolDistinctTargetsBothQueue(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", tmp)
	t.Setenv("OGHAM_DEDUPE_DIR", t.TempDir())

	for _, path := range []string{"/repo/a.go", "/repo/b.go"} {
		input := map[string]any{
			"tool_name":  "Edit",
			"session_id": "session-distinct",
			"tool_input": map[string]any{"file_path": path},
		}
		if err := runNativePostTool(context.Background(), nil, input, "work"); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(tmp, "*.jsonl"))
	if len(files) != 2 {
		t.Errorf("expected 2 queued files for 2 distinct targets, got %d", len(files))
	}
}
