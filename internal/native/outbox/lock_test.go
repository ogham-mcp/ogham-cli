package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTryLockAcquiresAndReleases(t *testing.T) {
	o := mustNew(t)

	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !o.LockHeld() {
		t.Errorf("LockHeld() = false while lock is held")
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if o.LockHeld() {
		t.Errorf("LockHeld() = true after Release")
	}
}

func TestTryLockSecondCallerGetsErrLocked(t *testing.T) {
	o := mustNew(t)

	first, err := o.TryLock()
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := o.TryLock()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock err = %v, want ErrLocked", err)
	}
	if second != nil {
		t.Errorf("second TryLock returned a lock alongside ErrLocked")
	}
}

func TestTryLockTakesOverStaleLock(t *testing.T) {
	o := mustNew(t)

	// A drainer that was SIGKILLed leaves the file behind. Anything
	// older than DefaultLockStale is assumed dead -- otherwise one
	// crash wedges the queue permanently.
	stale := lockInfo{PID: 999999, AcquiredAt: time.Now().Add(-2 * DefaultLockStale)}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(o.Dir, lockFileName), data, 0o600); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}

	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock over stale lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestTryLockRespectsFreshLockWithUnparseableBody(t *testing.T) {
	o := mustNew(t)

	// A truncated write (SIGKILL between create and write) must not be
	// read as "free" -- fall back to mtime, which is fresh here.
	if err := os.WriteFile(filepath.Join(o.Dir, lockFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	if _, err := o.TryLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("TryLock err = %v, want ErrLocked for a fresh unparseable lock", err)
	}
}

func TestTryLockTakesOverStaleLockWithUnparseableBody(t *testing.T) {
	o := mustNew(t)

	path := filepath.Join(o.Dir, lockFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	old := time.Now().Add(-2 * DefaultLockStale)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock over stale unparseable lock: %v", err)
	}
	_ = lock.Release()
}

func TestReleaseIsIdempotent(t *testing.T) {
	o := mustNew(t)
	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second Release: %v, want nil", err)
	}
}

func TestLockFileIsNotDrainedAsARecord(t *testing.T) {
	o := mustNew(t)
	if err := o.Write(&Record{Content: "Edit: /repo/foo.go"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	stats, err := o.Drain(context.Background(), func(context.Context, *Record) error { return nil })
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Processed != 1 || stats.Malformed != 0 {
		t.Errorf("stats = %+v, want Processed=1 Malformed=0 (lock file must be ignored)", stats)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, lockFileName)); err != nil {
		t.Errorf("Drain removed the lock file: %v", err)
	}
}

func TestLockHeldFalseOnMissingDir(t *testing.T) {
	o := &Outbox{Dir: filepath.Join(t.TempDir(), "absent"), now: time.Now}
	if o.LockHeld() {
		t.Errorf("LockHeld() = true for a directory that does not exist")
	}
}

func TestPendingCountsOnlyLiveRecords(t *testing.T) {
	o := mustNew(t)
	for i := 0; i < 3; i++ {
		if err := o.Write(&Record{Content: "Edit: /repo/foo.go"}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	// Noise that must not be counted: a stray .tmp and the lock file.
	if err := os.WriteFile(filepath.Join(o.Dir, "stray.tmp"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed tmp: %v", err)
	}
	lock, err := o.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	n, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if n != 3 {
		t.Errorf("Pending() = %d, want 3", n)
	}
}

func TestPendingZeroOnMissingDir(t *testing.T) {
	o := &Outbox{Dir: filepath.Join(t.TempDir(), "absent"), now: time.Now}
	n, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if n != 0 {
		t.Errorf("Pending() = %d, want 0", n)
	}
}
