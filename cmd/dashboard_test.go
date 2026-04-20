package cmd

import (
	"reflect"
	"testing"
)

func TestSwapTerminalToDashboard(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"standard uv tool run line",
			[]string{"uv", "tool", "run", "--python", "3.13", "--from", "ogham-mcp[postgres,gemini]", "ogham", "serve"},
			[]string{"uv", "tool", "run", "--python", "3.13", "--from", "ogham-mcp[postgres,gemini]", "ogham", "dashboard"},
		},
		{
			"direct ogham serve",
			[]string{"ogham", "serve"},
			[]string{"ogham", "dashboard"},
		},
		{
			"no serve in command -- appends dashboard",
			[]string{"/custom/path/ogham"},
			[]string{"/custom/path/ogham", "dashboard"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := swapTerminalToDashboard(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveDashboardCommand_CmdOverride(t *testing.T) {
	t.Setenv("OGHAM_DASHBOARD_CMD", "my-dashboard --verbose")
	t.Setenv("OGHAM_SIDECAR_CMD", "uv run ogham serve") // should be ignored
	got := resolveDashboardCommand()
	want := []string{"my-dashboard", "--verbose"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveDashboardCommand_SidecarCmdTransformed(t *testing.T) {
	t.Setenv("OGHAM_DASHBOARD_CMD", "")
	t.Setenv("OGHAM_SIDECAR_CMD", "uv run ogham serve")
	got := resolveDashboardCommand()
	want := []string{"uv", "run", "ogham", "dashboard"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveDashboardCommand_DefaultIncludesDashboardExtra(t *testing.T) {
	t.Setenv("OGHAM_DASHBOARD_CMD", "")
	t.Setenv("OGHAM_SIDECAR_CMD", "")
	t.Setenv("OGHAM_SIDECAR_EXTRAS", "postgres,gemini")
	got := resolveDashboardCommand()
	if len(got) < 7 {
		t.Fatalf("unexpected command: %v", got)
	}
	// The --from arg should contain the dashboard extra.
	var fromIdx int = -1
	for i, a := range got {
		if a == "--from" && i+1 < len(got) {
			fromIdx = i + 1
			break
		}
	}
	if fromIdx < 0 {
		t.Fatalf("no --from in command: %v", got)
	}
	spec := got[fromIdx]
	if spec != "ogham-mcp[postgres,gemini,dashboard]" {
		t.Errorf("--from = %q, want dashboard appended to extras", spec)
	}
}

func TestResolveDashboardCommand_DefaultNoExtras(t *testing.T) {
	t.Setenv("OGHAM_DASHBOARD_CMD", "")
	t.Setenv("OGHAM_SIDECAR_CMD", "")
	t.Setenv("OGHAM_SIDECAR_EXTRAS", "")
	got := resolveDashboardCommand()
	for i, a := range got {
		if a == "--from" && i+1 < len(got) {
			spec := got[i+1]
			if spec != "ogham-mcp[postgres,dashboard]" {
				t.Errorf("--from = %q, want postgres+dashboard default", spec)
			}
		}
	}
}
