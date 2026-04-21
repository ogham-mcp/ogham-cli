package cmd

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,,", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,c", []string{"a", "b", "c"}},
		{"type:decision,project:ogham", []string{"type:decision", "project:ogham"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abcdefghij", 10, "abcdefghij"},
		{"abcdefghijk", 10, "abcdefghij..."},
		{"日本語テスト", 3, "日本語..."},
	}
	for _, tc := range cases {
		got := truncate(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestExtractMemories(t *testing.T) {
	t.Run("raw array", func(t *testing.T) {
		got := extractMemories([]any{
			map[string]any{"id": "1"},
			map[string]any{"id": "2"},
		})
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
	})
	t.Run("results key", func(t *testing.T) {
		got := extractMemories(map[string]any{
			"results": []any{map[string]any{"id": "x"}},
		})
		if len(got) != 1 || got[0]["id"] != "x" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("result singular key (Python MCP wrapper)", func(t *testing.T) {
		got := extractMemories(map[string]any{
			"result": []any{map[string]any{"id": "y"}, map[string]any{"id": "z"}},
		})
		if len(got) != 2 {
			t.Errorf("got %+v, want 2 memories", got)
		}
	})
	t.Run("unknown shape", func(t *testing.T) {
		if got := extractMemories("stringy"); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
	t.Run("non-map entries dropped", func(t *testing.T) {
		got := extractMemories([]any{
			map[string]any{"id": "1"},
			"stray",
			map[string]any{"id": "2"},
		})
		if len(got) != 2 {
			t.Errorf("len = %d, want 2 (non-map dropped): %+v", len(got), got)
		}
	})
}

func TestNotImplemented(t *testing.T) {
	err := notImplemented("search")
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() == "" {
		t.Error("error should have a message")
	}
}

// writeSidecarFallbackNotice must emit a visible notice by default but
// stay silent under --sidecar / --legacy / --python (user already knows)
// or --quiet (user suppressed). Tests the global-flag-driven suppression
// without clobbering os.Stderr. The default notice text now points at
// --sidecar (the new canonical name), not --legacy.
func TestWriteSidecarFallbackNotice(t *testing.T) {
	// Restore globals after each subtest.
	origSidecar, origLegacy, origPython, origQuiet :=
		rootSidecarFlag, rootLegacyFlag, rootPythonAlias, rootQuietFlag
	t.Cleanup(func() {
		rootSidecarFlag, rootLegacyFlag, rootPythonAlias, rootQuietFlag =
			origSidecar, origLegacy, origPython, origQuiet
	})

	cases := []struct {
		name        string
		sidecar     bool
		legacy      bool
		pythonAlias bool
		quiet       bool
		wantEmit    bool
	}{
		{"default emits", false, false, false, false, true},
		{"--sidecar silences", true, false, false, false, false},
		{"--legacy silences (deprecated alias)", false, true, false, false, false},
		{"--python silences (alias for --sidecar)", false, false, true, false, false},
		{"--quiet silences", false, false, false, true, false},
		{"combined flags still silent", true, false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootSidecarFlag = tc.sidecar
			rootLegacyFlag = tc.legacy
			rootPythonAlias = tc.pythonAlias
			rootQuietFlag = tc.quiet

			var buf bytes.Buffer
			writeSidecarFallbackNotice(&buf, "store")

			got := buf.String()
			if tc.wantEmit {
				if got == "" {
					t.Error("expected notice, got empty")
				}
				if !strings.Contains(got, `"store"`) {
					t.Errorf("notice missing tool name: %q", got)
				}
				// Default message should now name --sidecar, not the
				// deprecated --legacy alias.
				if !strings.Contains(got, "--sidecar") {
					t.Errorf("notice should mention --sidecar; got %q", got)
				}
			} else if got != "" {
				t.Errorf("expected silence, got: %q", got)
			}
		})
	}
}

// TestLegacyFlagDeprecation confirms that --legacy is still accepted as
// a functional opt-in for the sidecar path (parity with --sidecar), but
// is hidden from --help so new users don't pick it up. The deprecation
// warning itself is emitted via slog.Warn in PersistentPreRunE; this
// test pins the semantic guarantee that useSidecar() still returns true
// when only --legacy is set.
func TestLegacyFlagIsStillFunctional(t *testing.T) {
	origSidecar, origLegacy, origPython :=
		rootSidecarFlag, rootLegacyFlag, rootPythonAlias
	t.Cleanup(func() {
		rootSidecarFlag, rootLegacyFlag, rootPythonAlias =
			origSidecar, origLegacy, origPython
	})

	// Only --legacy set -- useSidecar() must still return true.
	rootSidecarFlag, rootLegacyFlag, rootPythonAlias = false, true, false
	if !useSidecar() {
		t.Error("--legacy alone must still activate sidecar routing")
	}

	// useLegacy stays a compat synonym so no call-sites break mid-rename.
	if !useLegacy() {
		t.Error("useLegacy() must track useSidecar() for backward compat")
	}

	// --sidecar alone works on its own.
	rootSidecarFlag, rootLegacyFlag, rootPythonAlias = true, false, false
	if !useSidecar() {
		t.Error("--sidecar alone must activate sidecar routing")
	}

	// All flags clear -> default (native) path.
	rootSidecarFlag, rootLegacyFlag, rootPythonAlias = false, false, false
	if useSidecar() {
		t.Error("no flags set should yield useSidecar() == false")
	}
}

// TestLegacyDeprecationWarning pins the slog.Warn behaviour: --legacy
// emits exactly one warning containing the new flag name and a v0.8
// removal notice; --sidecar emits nothing; --quiet silences the warning.
func TestLegacyDeprecationWarning(t *testing.T) {
	origSidecar, origLegacy, origQuiet := rootSidecarFlag, rootLegacyFlag, rootQuietFlag
	t.Cleanup(func() {
		rootSidecarFlag, rootLegacyFlag, rootQuietFlag = origSidecar, origLegacy, origQuiet
	})

	captureWarn := func(t *testing.T) string {
		t.Helper()
		var buf bytes.Buffer
		orig := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelWarn,
		})))
		t.Cleanup(func() { slog.SetDefault(orig) })
		warnLegacyDeprecated()
		return buf.String()
	}

	t.Run("--legacy emits warn with new flag name + v0.8 notice", func(t *testing.T) {
		rootSidecarFlag, rootLegacyFlag, rootQuietFlag = false, true, false
		out := captureWarn(t)
		if out == "" {
			t.Fatal("expected deprecation warning, got empty")
		}
		if !strings.Contains(out, "--sidecar") {
			t.Errorf("warning should point users at --sidecar: %q", out)
		}
		if !strings.Contains(out, "v0.8") {
			t.Errorf("warning should mention v0.8 removal: %q", out)
		}
	})

	t.Run("--sidecar alone is silent", func(t *testing.T) {
		rootSidecarFlag, rootLegacyFlag, rootQuietFlag = true, false, false
		if out := captureWarn(t); out != "" {
			t.Errorf("--sidecar alone must not warn; got %q", out)
		}
	})

	t.Run("--quiet suppresses --legacy warning", func(t *testing.T) {
		rootSidecarFlag, rootLegacyFlag, rootQuietFlag = false, true, true
		if out := captureWarn(t); out != "" {
			t.Errorf("--quiet must silence the --legacy warning; got %q", out)
		}
	})
}
