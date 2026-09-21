package main

// Tests for install.sh. The script had none until now, which is how it
// kept a hole that `go test` could never have seen: it decided whether an
// install was an in-place upgrade by comparing PATHS, so a file sitting at
// the target path was assumed to be ours and silently replaced. On a
// machine that also develops the Python ogham-mcp, the file at
// ~/.local/bin/ogham is that project's venv shim.
//
// Everything here runs the real script against a local file:// "release"
// tree, which is the layout BASE_URL exists for. No network, no GitHub.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeBinarySource is a stand-in for the released binary: a compiled Go
// program, not a script, because "is it a script?" is one of the things
// the identity check keys on.
//
// It must mirror the REAL CLI's output shape, which is the whole lesson
// of v0.13.6. The first version of this stand-in printed the
// "ogham-cli/..." line for a bare `version`, so every test here passed
// while the installer's probe -- which also ran bare `version` -- was
// broken against the real binary. The real `version` defaults to JSON,
// and that JSON names no product; only `version --text` identifies us.
// TestRealBinaryVersionTextIsIdentifiable pins the contract this
// imitates, so the two cannot drift apart again in silence.
const fakeBinarySource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		for _, a := range os.Args[2:] {
			if a == "--text" {
				fmt.Println("ogham-cli/0.0.0-test  commit=test  built=test")
				return
			}
		}
		// Default output is JSON, exactly like the real CLI -- and note
		// that nothing in it names the product.
		fmt.Println(` + "`" + `{"version": "0.0.0-test", "commit": "test"}` + "`" + `)
		return
	}
	fmt.Println("fake ogham-cli")
}
`

var (
	fakeBinOnce sync.Once
	fakeBinPath string
	fakeBinErr  error
)

// buildFakeBinary compiles the stand-in once per test run and hands back
// its path. Built once because every case needs it and the compile is the
// slow part.
func buildFakeBinary(t *testing.T) string {
	t.Helper()
	fakeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ogham-fake-bin")
		if err != nil {
			fakeBinErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(fakeBinarySource), 0o600); err != nil {
			fakeBinErr = err
			return
		}
		out := filepath.Join(dir, "ogham")
		cmd := exec.Command("go", "build", "-o", out, src)
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeBinErr = fmt.Errorf("build fake binary: %v: %s", err, b)
			return
		}
		fakeBinPath = out
	})
	if fakeBinErr != nil {
		t.Fatalf("%v", fakeBinErr)
	}
	return fakeBinPath
}

// makeReleaseTree writes a tar.gz holding the fake binary plus a matching
// checksums.txt, mirroring what GoReleaser publishes. Returns the dir to
// hand to the script as BASE_URL.
func makeReleaseTree(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script drives tar/curl; not exercised on Windows")
	}
	bin := buildFakeBinary(t)
	body, err := os.ReadFile(bin) // #nosec G304 -- path produced by this test
	if err != nil {
		t.Fatalf("read fake binary: %v", err)
	}

	dir := t.TempDir()
	asset := fmt.Sprintf("ogham-cli-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archivePath := filepath.Join(dir, asset)

	f, err := os.Create(archivePath) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "ogham", Mode: 0o755, Size: int64(len(body)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	for _, c := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := c(); err != nil {
			t.Fatalf("close archive: %v", err)
		}
	}

	archived, err := os.ReadFile(archivePath) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	sum := sha256.Sum256(archived)
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(line), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	return dir
}

// runInstall executes install.sh against the local release tree.
func runInstall(t *testing.T, installDir string, args ...string) (string, error) {
	t.Helper()
	base := makeReleaseTree(t)
	argv := append([]string{"install.sh"}, args...)
	cmd := exec.Command("bash", argv...) // #nosec G204 -- fixed script, test-owned args
	cmd.Env = append(os.Environ(),
		"BASE_URL=file://"+base,
		"INSTALL_DIR="+installDir,
		// Keep the script's PATH-collision check from tripping on the
		// developer's own ogham/omcli install; this suite is about the
		// target-identity check.
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin:"+filepath.Dir(mustLookGo(t)),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustLookGo(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not on PATH: %v", err)
	}
	return p
}

func TestInstallDefaultsToTheNameOgham(t *testing.T) {
	dir := t.TempDir()
	out, err := runInstall(t, dir)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "ogham")); err != nil {
		t.Errorf("expected %s/ogham to exist: %v\n%s", dir, err, out)
	}
}

func TestInstallUnderAnAlternateName(t *testing.T) {
	// The point of the flag: on a machine where the Python package owns
	// `ogham`, this binary installs as omcli or om. cmd/hooks.go already
	// matches those names; without this the installer could not produce
	// them.
	for _, name := range []string{"omcli", "om"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runInstall(t, dir, "--name", name)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("expected %s/%s to exist: %v\n%s", dir, name, err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "ogham")); err == nil {
				t.Errorf("--name %s also wrote an 'ogham'", name)
			}
		})
	}
}

func TestInstallRefusesToClobberASymlink(t *testing.T) {
	// The exact shape that slipped through: a venv console-script shim at
	// the install target. The old check compared paths, saw target ==
	// itself, and called it an in-place upgrade.
	dir := t.TempDir()
	victim := filepath.Join(dir, "python-ogham-entry-point")
	if err := os.WriteFile(victim, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil { // #nosec G306 -- imitating a console script
		t.Fatalf("write victim: %v", err)
	}
	target := filepath.Join(dir, "ogham")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	out, err := runInstall(t, dir)
	if err == nil {
		t.Fatalf("install should have refused to replace a symlink\n%s", out)
	}
	if !strings.Contains(out, "NOT an ogham-cli binary") {
		t.Errorf("refusal should say what it found:\n%s", out)
	}
	if !strings.Contains(out, "--name omcli") {
		t.Errorf("refusal should offer the way out:\n%s", out)
	}
	// And the file it refused to touch must still be a symlink to the
	// same place. A guard that warns and clobbers anyway is no guard.
	got, err := os.Readlink(target)
	if err != nil || got != victim {
		t.Errorf("symlink was damaged: readlink=%q err=%v", got, err)
	}
}

func TestInstallRefusesToClobberAScript(t *testing.T) {
	// Same product, installed by pip as a real file rather than a symlink.
	dir := t.TempDir()
	target := filepath.Join(dir, "ogham")
	body := "#!/usr/bin/env python3\nprint('python ogham-mcp')\n"
	if err := os.WriteFile(target, []byte(body), 0o755); err != nil { // #nosec G306 -- imitating a console script
		t.Fatalf("write script: %v", err)
	}

	out, err := runInstall(t, dir)
	if err == nil {
		t.Fatalf("install should have refused to replace a script\n%s", out)
	}
	after, rerr := os.ReadFile(target) // #nosec G304 -- test temp dir
	if rerr != nil || string(after) != body {
		t.Errorf("the script was overwritten despite the refusal")
	}
}

func TestInstallForceOverridesTheIdentityCheck(t *testing.T) {
	// The escape hatch has to work, or people will reach for something
	// worse.
	dir := t.TempDir()
	target := filepath.Join(dir, "ogham")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho not ours\n"), 0o755); err != nil { // #nosec G306 -- imitating a console script
		t.Fatalf("write script: %v", err)
	}

	out, err := runInstall(t, dir, "--force")
	if err != nil {
		t.Fatalf("--force should install anyway: %v\n%s", err, out)
	}
	after, rerr := os.ReadFile(target) // #nosec G304 -- test temp dir
	if rerr != nil {
		t.Fatalf("read target: %v", rerr)
	}
	if strings.HasPrefix(string(after), "#!") {
		t.Errorf("--force did not replace the file")
	}
}

func TestInstallUpgradesItsOwnBinaryWithoutPrompting(t *testing.T) {
	// The case the path comparison was trying to serve, now decided on
	// identity instead: a real ogham-cli at the target upgrades silently.
	dir := t.TempDir()
	if out, err := runInstall(t, dir); err != nil {
		t.Fatalf("first install: %v\n%s", err, out)
	}
	out, err := runInstall(t, dir)
	if err != nil {
		t.Fatalf("upgrade over our own binary should not prompt: %v\n%s", err, out)
	}
	if strings.Contains(out, "NOT an ogham-cli binary") {
		t.Errorf("upgrade was treated as a foreign file:\n%s", out)
	}
}

func TestInstallRejectsANameThatIsAPath(t *testing.T) {
	// This script is run as `curl | bash`. A --name that can contain a
	// slash is an installer that writes wherever it is pointed.
	for _, bad := range []string{"../evil", "/etc/evil", "a/b", "..", "", "ev;il", "ev il"} {
		t.Run("name="+bad, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runInstall(t, dir, "--name="+bad)
			if err == nil {
				t.Fatalf("--name %q should be rejected\n%s", bad, out)
			}
			if !strings.Contains(out, "--name must be a bare filename") &&
				!strings.Contains(out, "--name may only contain") {
				t.Errorf("rejection should explain the rule:\n%s", out)
			}
		})
	}
}

// TestRealBinaryVersionTextIsIdentifiable pins the contract install.sh's
// identity probe depends on: `version --text` must emit a line starting
// with "ogham-cli/". Nothing else the CLI prints identifies the product --
// the default JSON carries version, commit, build_date, go, os and arch,
// and not the name.
//
// This test exists because the stand-in above can be made to say anything.
// If the real output format ever changes, the installer silently stops
// recognising its own binary and starts refusing every upgrade; that is
// what v0.13.6 shipped. This fails instead.
func TestRealBinaryVersionTextIsIdentifiable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("builds and runs a unix binary")
	}
	bin := filepath.Join(t.TempDir(), "ogham")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version", "--text").Output() // #nosec G204 -- path built by this test
	if err != nil {
		t.Fatalf("version --text: %v", err)
	}
	if !strings.HasPrefix(string(out), "ogham-cli/") {
		t.Errorf("version --text = %q, want a line starting \"ogham-cli/\" -- install.sh's identity probe keys on it", out)
	}

	// The other half of the lesson: bare `version` must NOT be what the
	// probe relies on. If this ever starts matching, the probe could be
	// simplified -- but only deliberately, not by accident.
	plain, err := exec.Command(bin, "version").Output() // #nosec G204 -- path built by this test
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if strings.HasPrefix(string(plain), "ogham-cli/") {
		t.Logf("note: bare `version` now emits the text form too: %q", plain)
	}
}
