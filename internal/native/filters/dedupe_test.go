package filters

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestDeduper builds a Deduper over a per-test temp directory with an
// injected clock, so nothing touches the real cache dir and time can be
// advanced deterministically.
func newTestDeduper(t *testing.T, now func() time.Time) *Deduper {
	t.Helper()
	return NewDeduperAt(t.TempDir(), now)
}

func TestDeduperFirstHitIsNotDuplicate(t *testing.T) {
	d := newTestDeduper(t, time.Now)
	if d.IsDuplicate("s1", "Bash", "git commit") {
		t.Errorf("first hit reported as duplicate")
	}
	if d.Size() != 1 {
		t.Errorf("Size after first hit = %d, want 1", d.Size())
	}
}

func TestDeduperRepeatedWithinWindow(t *testing.T) {
	clock := newFakeClock()
	d := newTestDeduper(t, clock.now)

	if d.IsDuplicate("s1", "Edit", "foo.go") {
		t.Errorf("first hit reported as duplicate")
	}
	clock.advance(1 * time.Minute) // well within DedupWindow
	if !d.IsDuplicate("s1", "Edit", "foo.go") {
		t.Errorf("second hit within window not reported as duplicate")
	}
	clock.advance(1 * time.Minute)
	if !d.IsDuplicate("s1", "Edit", "foo.go") {
		t.Errorf("third hit (timestamp refreshed) not reported as duplicate")
	}
}

func TestDeduperHitsOutsideWindowAreFresh(t *testing.T) {
	clock := newFakeClock()
	d := newTestDeduper(t, clock.now)

	if d.IsDuplicate("s1", "Bash", "git push") {
		t.Errorf("first hit reported as duplicate")
	}
	clock.advance(DedupWindow + time.Second)
	if d.IsDuplicate("s1", "Bash", "git push") {
		t.Errorf("hit after DedupWindow incorrectly reported as duplicate")
	}
}

func TestDeduperDifferentSessionsAreIndependent(t *testing.T) {
	d := newTestDeduper(t, time.Now)
	d.IsDuplicate("session-a", "Bash", "git status")
	if d.IsDuplicate("session-b", "Bash", "git status") {
		t.Errorf("(session-b, Bash, git status) flagged duplicate when only session-a saw it")
	}
}

func TestDeduperDifferentTargetsAreIndependent(t *testing.T) {
	d := newTestDeduper(t, time.Now)
	d.IsDuplicate("s1", "Edit", "foo.go")
	if d.IsDuplicate("s1", "Edit", "bar.go") {
		t.Errorf("Edit on bar.go flagged duplicate when only foo.go was seen")
	}
}

func TestDeduperPrunesStaleEntries(t *testing.T) {
	clock := newFakeClock()
	d := newTestDeduper(t, clock.now)

	for i := 0; i < 10; i++ {
		d.IsDuplicate("s1", "Bash", "cmd-"+string(rune('a'+i)))
	}
	if d.Size() != 10 {
		t.Fatalf("expected 10 entries before prune, got %d", d.Size())
	}
	clock.advance(PruneThreshold + time.Minute)
	// Recording a fresh key triggers the prune sweep.
	d.IsDuplicate("s1", "Bash", "trigger-prune")
	if d.Size() != 1 {
		t.Errorf("after prune, expected 1 entry, got %d", d.Size())
	}
}

func TestDeduperConcurrentSafe(t *testing.T) {
	d := newTestDeduper(t, time.Now)
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			d.IsDuplicate("s1", "Bash", "concurrent-cmd")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	// Only 1 distinct entry should be recorded.
	if d.Size() != 1 {
		t.Errorf("Size after 50 concurrent same-key hits = %d, want 1", d.Size())
	}
}

// --- Cross-process persistence (issue #26 finding 4) -----------------
//
// `ogham hooks run post-tool` is registered as a "type": "command"
// hook, so Claude Code spawns a fresh process per tool call. A Deduper
// whose state lives in process memory is empty on every invocation and
// can never fire. These tests use a second Deduper over the same
// directory to simulate that restart.

// TestDeduperSurvivesProcessRestart is the regression test for finding
// 4. It fails against any in-process implementation.
func TestDeduperSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeClock()

	first := NewDeduperAt(dir, clock.now)
	if first.IsDuplicate("s1", "Bash", "git push origin main") {
		t.Fatalf("first hit reported as duplicate")
	}

	// Process exits, a new one starts for the next tool call.
	clock.advance(2 * time.Second)
	second := NewDeduperAt(dir, clock.now)
	if !second.IsDuplicate("s1", "Bash", "git push origin main") {
		t.Errorf("repeat in a fresh process not reported as duplicate -- state did not persist")
	}
}

// TestDeduperRestartRespectsWindow asserts persistence doesn't mean
// "forever": a fresh process seeing the key after DedupWindow treats it
// as new.
func TestDeduperRestartRespectsWindow(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeClock()

	NewDeduperAt(dir, clock.now).IsDuplicate("s1", "Edit", "foo.go")
	clock.advance(DedupWindow + time.Second)
	if NewDeduperAt(dir, clock.now).IsDuplicate("s1", "Edit", "foo.go") {
		t.Errorf("hit after DedupWindow in a fresh process incorrectly reported as duplicate")
	}
}

// TestDeduperRestartKeysStayDistinct guards against a hashing collapse
// where every key maps to one on-disk entry -- which would make the
// first tool call of a session suppress every later one.
func TestDeduperRestartKeysStayDistinct(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeClock()

	NewDeduperAt(dir, clock.now).IsDuplicate("s1", "Bash", "go build ./...")
	fresh := NewDeduperAt(dir, clock.now)
	if fresh.IsDuplicate("s1", "Bash", "go test ./...") {
		t.Errorf("distinct command flagged as duplicate across restart")
	}
	if fresh.IsDuplicate("s2", "Bash", "go build ./...") {
		t.Errorf("distinct session flagged as duplicate across restart")
	}
	if got := fresh.Size(); got != 3 {
		t.Errorf("Size = %d, want 3 distinct entries", got)
	}
}

// TestDeduperKeysAreNotPathTraversable asserts targets containing path
// separators and dot segments cannot escape the dedupe directory.
func TestDeduperKeysAreNotPathTraversable(t *testing.T) {
	dir := t.TempDir()
	d := NewDeduperAt(dir, time.Now)

	d.IsDuplicate("s1", "Edit", "../../../etc/passwd")
	d.IsDuplicate("s1", "Edit", "/absolute/path/to/foo.go")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in the dedupe dir, got %d", len(entries))
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("entry %q is a directory -- key was not flattened", e.Name())
		}
		if filepath.Base(e.Name()) != e.Name() {
			t.Errorf("entry %q contains a path separator", e.Name())
		}
	}
}

// TestDefaultDedupeDirHonoursOverride mirrors the outbox's
// OGHAM_OUTBOX_DIR contract so tests and sandboxes can redirect state.
func TestDefaultDedupeDirHonoursOverride(t *testing.T) {
	t.Setenv("OGHAM_DEDUPE_DIR", "/tmp/ogham-dedupe-test")
	got, err := DefaultDedupeDir()
	if err != nil {
		t.Fatalf("DefaultDedupeDir: %v", err)
	}
	if got != "/tmp/ogham-dedupe-test" {
		t.Errorf("DefaultDedupeDir() = %q, want the override", got)
	}
}

// --- helpers ---

type fakeClock struct {
	t time.Time
}

func newFakeClock() *fakeClock {
	// Fixed reference epoch -- avoids Date.now()-style nondeterminism.
	return &fakeClock{t: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
