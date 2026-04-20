package native

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# a comment
EMPTY_LINE_BELOW

FOO=bar
  SPACED_KEY  =  spaced_value
QUOTED="hello world"
SINGLE_QUOTED='val with spaces'
export WITH_EXPORT=exported
# trailing comment
MALFORMED
=no_key_here
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got := parseEnvFile(path)
	want := []string{
		"FOO=bar",
		"SPACED_KEY=spaced_value",
		"QUOTED=hello world",
		"SINGLE_QUOTED=val with spaces",
		"WITH_EXPORT=exported",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvFile mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestParseEnvFile_Missing(t *testing.T) {
	got := parseEnvFile("/definitely/not/a/real/path.env")
	if got != nil {
		t.Errorf("missing file should return nil, got %v", got)
	}
}

func TestLoadEnvFiles_Precedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	// Global fallback.
	globalDir := filepath.Join(home, ".ogham")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.env"),
		[]byte("SHARED=from_global\nGLOBAL_ONLY=g"), 0600); err != nil {
		t.Fatal(err)
	}

	// Project-local overrides global for shared keys.
	if err := os.WriteFile(filepath.Join(cwd, ".env"),
		[]byte("SHARED=from_project\nPROJECT_ONLY=p"), 0600); err != nil {
		t.Fatal(err)
	}

	prevCwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prevCwd) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	got := LoadEnvFiles()
	// Global entries should come before project entries so later-wins gives
	// project precedence when the slice is appended to os.Environ().
	if len(got) != 4 {
		t.Fatalf("got %d entries: %v", len(got), got)
	}
	if got[0] != "SHARED=from_global" {
		t.Errorf("first should be global SHARED, got %q", got[0])
	}
	if got[2] != "SHARED=from_project" {
		t.Errorf("third should be project SHARED, got %q", got[2])
	}
}
