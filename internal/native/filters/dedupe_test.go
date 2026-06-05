package filters

import (
	"testing"
	"time"
)

func TestDeduperFirstHitIsNotDuplicate(t *testing.T) {
	d := NewDeduper()
	if d.IsDuplicate("s1", "Bash", "git commit") {
		t.Errorf("first hit reported as duplicate")
	}
	if d.Size() != 1 {
		t.Errorf("Size after first hit = %d, want 1", d.Size())
	}
}

func TestDeduperRepeatedWithinWindow(t *testing.T) {
	clock := newFakeClock()
	d := NewDeduperWithClock(clock.now)

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
	d := NewDeduperWithClock(clock.now)

	if d.IsDuplicate("s1", "Bash", "git push") {
		t.Errorf("first hit reported as duplicate")
	}
	clock.advance(DedupWindow + time.Second)
	if d.IsDuplicate("s1", "Bash", "git push") {
		t.Errorf("hit after DedupWindow incorrectly reported as duplicate")
	}
}

func TestDeduperDifferentSessionsAreIndependent(t *testing.T) {
	d := NewDeduper()
	d.IsDuplicate("session-a", "Bash", "git status")
	if d.IsDuplicate("session-b", "Bash", "git status") {
		t.Errorf("(session-b, Bash, git status) flagged duplicate when only session-a saw it")
	}
}

func TestDeduperDifferentTargetsAreIndependent(t *testing.T) {
	d := NewDeduper()
	d.IsDuplicate("s1", "Edit", "foo.go")
	if d.IsDuplicate("s1", "Edit", "bar.go") {
		t.Errorf("Edit on bar.go flagged duplicate when only foo.go was seen")
	}
}

func TestDeduperPrunesStaleEntries(t *testing.T) {
	clock := newFakeClock()
	d := NewDeduperWithClock(clock.now)

	for i := 0; i < 10; i++ {
		d.IsDuplicate("s1", "Bash", "cmd-"+string(rune('a'+i)))
	}
	if d.Size() != 10 {
		t.Fatalf("expected 10 entries before prune, got %d", d.Size())
	}
	clock.advance(PruneThreshold + time.Minute)
	// Any IsDuplicate call triggers pruneLocked.
	d.IsDuplicate("s1", "Bash", "trigger-prune")
	if d.Size() != 1 {
		t.Errorf("after prune, expected 1 entry, got %d", d.Size())
	}
}

func TestDeduperConcurrentSafe(t *testing.T) {
	d := NewDeduper()
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func(id int) {
			d.IsDuplicate("s1", "Bash", "concurrent-cmd")
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	// Only 1 distinct entry should be recorded.
	if d.Size() != 1 {
		t.Errorf("Size after 50 concurrent same-key hits = %d, want 1", d.Size())
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

func (c *fakeClock) now() time.Time           { return c.t }
func (c *fakeClock) advance(d time.Duration)  { c.t = c.t.Add(d) }
