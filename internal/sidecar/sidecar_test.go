package sidecar

import (
	"reflect"
	"testing"
)

func TestResolveCommand_Explicit(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "should-be-ignored")
	got := resolveCommand("uv", []string{"run", "ogham", "serve"})
	// Explicit wins: whatever the caller passed is echoed back unchanged,
	// regardless of env var or defaultCmd.
	want := []string{"uv", "run", "ogham", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveCommand(explicit) = %v, want %v", got, want)
	}
}

func TestResolveCommand_EnvVar(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "python -m ogham serve")
	got := resolveCommand("", nil)
	want := []string{"python", "-m", "ogham", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveCommand(env) = %v, want %v", got, want)
	}
}

func TestResolveCommand_EnvVarEmptyFallsBack(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "   ")
	got := resolveCommand("", nil)
	want := []string{"uv", "tool", "run", "--python", "3.13", "--from", "ogham-mcp", "ogham", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveCommand(whitespace env) = %v, want %v", got, want)
	}
}

func TestResolveCommand_Default(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "")
	t.Setenv("OGHAM_SIDECAR_EXTRAS", "")
	got := resolveCommand("", nil)
	want := []string{"uv", "tool", "run", "--python", "3.13", "--from", "ogham-mcp", "ogham", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveCommand(default) = %v, want %v", got, want)
	}
	// The returned slice must not share backing with defaultCmdBase.
	got[0] = "tampered"
	if defaultCmdBase[0] == "tampered" {
		t.Errorf("resolveCommand leaked defaultCmdBase by reference")
	}
}

func TestResolveCommand_Extras(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "")
	t.Setenv("OGHAM_SIDECAR_EXTRAS", "postgres,gemini")
	got := resolveCommand("", nil)
	want := []string{"uv", "tool", "run", "--python", "3.13", "--from", "ogham-mcp[postgres,gemini]", "ogham", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveCommand(extras) = %v, want %v", got, want)
	}
}

func TestNew_UsesProvidedImpl(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "echo stub")
	c := New(Options{})
	if c.impl == nil || c.impl.Name != "ogham-cli" {
		t.Errorf("expected default Impl with Name=ogham-cli, got %+v", c.impl)
	}
	if c.cmd == nil || c.cmd.Path == "" {
		t.Error("expected cmd to be built on New()")
	}
}

func TestClient_CallToolBeforeConnect(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "echo stub")
	c := New(Options{})
	if _, err := c.CallTool(testCtx(t), "anything", nil); err == nil {
		t.Error("expected error when calling CallTool before Connect")
	}
}

func TestClient_CloseBeforeConnectIsNoop(t *testing.T) {
	t.Setenv("OGHAM_SIDECAR_CMD", "echo stub")
	c := New(Options{})
	if err := c.Close(); err != nil {
		t.Errorf("Close on unconnected client: %v", err)
	}
}
