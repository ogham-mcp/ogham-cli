package cmd

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Golden-file tests for the cobra subcommand tree. Covers --help
// output for every top-level command + a handful of functional
// deterministic paths. Lifts cmd/ coverage from near-zero by
// exercising cobra wiring (flag parsing, help formatting, output
// routing) which the argument-boundary unit tests in other packages
// never touch.
//
// Non-determinism is scrubbed before comparison: Go version, OS,
// arch, build commit, and build date all vary per-machine. scrub()
// replaces these with stable placeholders so golden files stay
// committable across CI runners.
//
// Regenerate goldens with:
//   go test -run TestCmdGolden ./cmd/ -update

var updateGolden = flag.Bool("update", false,
	"regenerate golden files from current cobra output")

// scrub replaces per-machine or per-build substrings with placeholders
// so golden files match across contributors + CI. Order matters: more
// specific patterns before generic ones.
var scrubPatterns = []struct {
	pattern *regexp.Regexp
	replace string
}{
	// go1.26.2 / go1.27 etc.
	{regexp.MustCompile(`go1\.\d+(\.\d+)?`), "go1.X"},
	// runtime.GOOS / GOARCH combinations surface in version.
	{regexp.MustCompile(`\b(darwin|linux|windows|freebsd)/(amd64|arm64|386)\b`), "OS/ARCH"},
	{regexp.MustCompile(`"os":\s*"[^"]+"`), `"os": "OS"`},
	{regexp.MustCompile(`"arch":\s*"[^"]+"`), `"arch": "ARCH"`},
	{regexp.MustCompile(`"go":\s*"[^"]+"`), `"go": "GO"`},
	// build info defaults to "unknown" unless ldflags injected; no
	// further scrubbing needed beyond the version placeholder itself.
	{regexp.MustCompile(`"version":\s*"[^"]+"`), `"version": "VERSION"`},
	{regexp.MustCompile(`(?m)^\s*"build_date":\s*"[^"]+",?$`), `  "build_date": "BUILD_DATE",`},
	{regexp.MustCompile(`(?m)^\s*"commit":\s*"[^"]+",?$`), `  "commit": "COMMIT",`},
}

func scrub(s string) string {
	for _, p := range scrubPatterns {
		s = p.pattern.ReplaceAllString(s, p.replace)
	}
	return strings.TrimRight(s, " \n") + "\n"
}

// goldenCase is one row in the table. setup runs before the command
// (e.g. to reset HOME for profile tests). args lives OWNED by the case
// -- cobra's rootCmd holds args between Execute() calls, so each case
// re-sets them.
type goldenCase struct {
	name   string
	args   []string
	setup  func(t *testing.T)
	expect string // optional substring check AFTER the golden diff
}

func TestCmdGolden(t *testing.T) {
	// NOTE: cobra's rootCmd retains state across Execute() calls --
	// once a case fires `--help`, subsequent cases inherit the
	// help-requested state regardless of SetArgs. Functional golden
	// cases (profile current, version --json, etc.) need a fresh
	// rootCmd per call, which means refactoring cmd/root.go to expose
	// a NewRootCmd() constructor. Tracked in docs/backlog.md.
	//
	// This sweep stays at --help pages (mechanically deterministic,
	// don't care about cobra state leakage since every case just
	// wants the help output).

	cases := []goldenCase{
		// --- help pages (deterministic cobra output) ----------------
		{name: "root_help", args: []string{"--help"}},
		{name: "version_help", args: []string{"version", "--help"}},
		{name: "serve_help", args: []string{"serve", "--help"}},
		{name: "store_help", args: []string{"store", "--help"}},
		{name: "search_help", args: []string{"search", "--help"}},
		{name: "list_help", args: []string{"list", "--help"}},
		{name: "health_help", args: []string{"health", "--help"}},
		{name: "profile_help", args: []string{"profile", "--help"}},
		{name: "profile_current_help", args: []string{"profile", "current", "--help"}},
		{name: "profile_switch_help", args: []string{"profile", "switch", "--help"}},
		{name: "profile_list_help", args: []string{"profile", "list", "--help"}},
		{name: "profile_ttl_help", args: []string{"profile", "ttl", "--help"}},
		{name: "delete_help", args: []string{"delete", "--help"}},
		{name: "cleanup_help", args: []string{"cleanup", "--help"}},
		{name: "decay_help", args: []string{"decay", "--help"}},
		{name: "audit_help", args: []string{"audit", "--help"}},
		{name: "stats_help", args: []string{"stats", "--help"}},
		{name: "config_help", args: []string{"config", "--help"}},
		{name: "export_help", args: []string{"export", "--help"}},
		{name: "import_help", args: []string{"import", "--help"}},
		{name: "import_agent_zero_help", args: []string{"import-agent-zero", "--help"}},
		{name: "hooks_help", args: []string{"hooks", "--help"}},
		{name: "plugin_help", args: []string{"plugin", "--help"}},
		{name: "init_help", args: []string{"init", "--help"}},
		{name: "capabilities_help", args: []string{"capabilities", "--help"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}

			buf := &bytes.Buffer{}
			rootCmd.SetOut(buf)
			rootCmd.SetErr(buf)
			rootCmd.SetArgs(tc.args)
			// Reset --help on every run -- cobra remembers the last
			// subcommand that fired help and leaks across cases.
			rootCmd.SilenceUsage = true

			// cobra returns an error for help requests in some
			// versions; swallow -- the output is what we care about.
			_ = rootCmd.Execute()

			got := scrub(buf.String())

			// Substring check (exercised for the functional cases).
			if tc.expect != "" && !strings.Contains(got, tc.expect) {
				t.Errorf("output missing %q; got %q", tc.expect, got)
			}

			// Golden-file diff.
			goldenDir := filepath.Join("testdata", "golden")
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatalf("mkdir golden: %v", err)
			}
			goldenPath := filepath.Join(goldenDir, tc.name+".txt")

			if *updateGolden {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to generate)", goldenPath, err)
			}
			if got != string(want) {
				t.Errorf("golden diff for %s:\nWANT:\n%s\n---\nGOT:\n%s",
					tc.name, want, got)
			}
		})
	}
}

// --- direct RunE tests: bypass cobra state leakage ----------------------
//
// Calling RunE functions directly on the subcommand gives us real coverage
// of the command body without fighting cobra's internal help-flag state.
// Pattern: build a throwaway *cobra.Command as the "caller", point stdout
// at a buffer via the package's emit helpers, assert the output.

// stubStdout redirects os.Stdout + os.Stderr to a buffer and returns a
// restore function. Needed because helpers.emitJSON / fmt.Println /
// fmt.Printf in the RunE bodies write directly to os.Stdout, not the
// cobra.Command.OutOrStdout(). Without this, test output gets drowned
// in CLI output and assertions can't inspect it.
func stubStdout(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	origStdout := os.Stdout
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	os.Stderr = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(buf, r)
		close(done)
	}()
	return buf, func() {
		_ = w.Close()
		<-done
		os.Stdout = origStdout
		os.Stderr = origStderr
	}
}

func TestProfileCurrent_DefaultProfile(t *testing.T) {
	// Empty HOME + no OGHAM_PROFILE -> resolver lands on "default".
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OGHAM_PROFILE", "")

	buf, restore := stubStdout(t)
	defer restore()

	c := &cobra.Command{}
	// Call the RunE directly on the subcommand -- bypasses rootCmd
	// dispatch entirely, so no help-state leakage.
	err := profileCurrentCmd.RunE(c, []string{})
	restore()
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	out := buf.String()
	// JSON is the default output shape; the resolver stores "default"
	// when no sentinel, env, or TOML profile is set.
	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not JSON: %q", out)
	}
	if parsed["profile"] != "default" {
		t.Errorf("profile = %q, want 'default'", parsed["profile"])
	}
}

func TestProfileCurrent_EnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OGHAM_PROFILE", "env-wins")

	buf, restore := stubStdout(t)
	defer restore()

	c := &cobra.Command{}
	err := profileCurrentCmd.RunE(c, []string{})
	restore()
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "env-wins") {
		t.Errorf("want 'env-wins' in output; got %q", buf.String())
	}
}

func TestVersionCmd_JSON(t *testing.T) {
	buf, restore := stubStdout(t)
	defer restore()

	c := &cobra.Command{}
	if err := versionCmd.RunE(c, []string{}); err != nil {
		restore()
		t.Fatalf("versionCmd: %v", err)
	}
	restore()

	out := buf.String()
	// Default output is JSON; must parse and contain a Go version.
	var info map[string]any
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("version output not JSON: %q", out)
	}
	if _, ok := info["go"]; !ok {
		t.Errorf("version JSON missing 'go' key: %+v", info)
	}
}

func TestPluginCmd_OpenClawManifest(t *testing.T) {
	// plugin openclaw prints a manifest snippet. Offline, no backend
	// needed -- pure string output.
	buf, restore := stubStdout(t)
	defer restore()

	c := &cobra.Command{}
	err := pluginOpenClawCmd.RunE(c, []string{})
	restore()
	if err != nil {
		t.Fatalf("openclaw manifest: %v", err)
	}
	if !strings.Contains(buf.String(), "ogham") {
		t.Errorf("manifest should reference 'ogham'; got %q", buf.String())
	}
}

// TestDisplayProfile_HandlesUnsetSentinel pins the helper that the
// profile subcommands use to render the "old" value during a switch.
// Ensures the (unset) placeholder surfaces cleanly for fresh installs.
func TestDisplayProfile_HandlesUnsetSentinel(t *testing.T) {
	if got := displayProfile(""); got != "(unset)" {
		t.Errorf("displayProfile(\"\") = %q, want '(unset)'", got)
	}
	if got := displayProfile("work"); got != "work" {
		t.Errorf("displayProfile(\"work\") = %q, want 'work'", got)
	}
}
