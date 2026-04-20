package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// captureStdout runs fn and returns everything it wrote to stdout.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	err = fn()
	_ = w.Close()
	os.Stdout = origStdout

	if err != nil {
		t.Fatalf("fn returned error: %v", err)
	}
	return string(<-done)
}

func TestPluginOpenClaw_EmitsValidJSON(t *testing.T) {
	out := captureStdout(t, func() error {
		return pluginOpenClawCmd.RunE(pluginOpenClawCmd, nil)
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n---output---\n%s", err, out)
	}
	for _, field := range []string{"name", "kind", "command", "args", "capabilities"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required field %q in manifest", field)
		}
	}
	if m["name"] != "ogham" {
		t.Errorf("name = %v, want ogham", m["name"])
	}
	if m["kind"] != "knowledge" {
		t.Errorf("kind = %v, want knowledge (so we don't collide with Neotoma's memory slot)", m["kind"])
	}
}

func TestPluginAgentZero_EmitsValidJSON(t *testing.T) {
	out := captureStdout(t, func() error {
		return pluginAgentZeroCmd.RunE(pluginAgentZeroCmd, nil)
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n---output---\n%s", err, out)
	}
	for _, field := range []string{"type", "name", "transport", "command", "args", "tools"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required field %q in manifest", field)
		}
	}
	if m["type"] != "mcp" {
		t.Errorf("type = %v, want mcp", m["type"])
	}
	if m["transport"] != "stdio" {
		t.Errorf("transport = %v, want stdio", m["transport"])
	}
}
