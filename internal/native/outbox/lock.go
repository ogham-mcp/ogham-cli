package outbox

// Drain exclusion.
//
// Until v0.14 exactly one process ever drained: the SessionStart hook,
// inline. Draining in a detached background process (#51) means two
// drainers can now overlap -- a background drain still shipping a long
// backlog when the next session starts. Concurrent drainers do not
// corrupt the queue (writers are unaffected, and each record file is
// deleted by whoever handled it), but they can double-store a record
// that both read before either deleted it.
//
// The lock is a lockfile rather than flock(2): the drainer's competitor
// is a *different process*, the queue already lives on a filesystem the
// package owns, and O_EXCL create is portable to Windows, which the
// release matrix cross-compiles for. A SIGKILLed drainer leaves the file
// behind, so the lock carries an acquisition timestamp and any lock older
// than DefaultLockStale is taken over -- one crash must not wedge the
// queue forever.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked is returned by TryLock when another drainer holds the lock
// and it is not yet stale. Callers treat this as "someone else is
// already doing this work", not as a failure.
var ErrLocked = errors.New("outbox: drain lock held by another process")

const (
	// DefaultLockStale is how old a lock must be before TryLock takes
	// it over. Set well above the longest plausible background drain
	// so a live drainer is never stolen from mid-run.
	DefaultLockStale = 30 * time.Minute

	// lockFileName sits inside the outbox directory. Its extension is
	// neither liveExtension nor inflightExtension, so Drain's partition
	// ignores it -- TestLockFileIsNotDrainedAsARecord pins that.
	lockFileName = ".drain.lock"
)

// lockInfo is the lockfile payload. PID is for humans reading the file
// during a postmortem; nothing branches on it (pid liveness checks are
// not portable, and a recycled pid would lie anyway). AcquiredAt is what
// staleness is measured from.
type lockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// DrainLock is a held drain lock. Release it when the drain finishes;
// Release is idempotent so `defer lock.Release()` is always safe.
type DrainLock struct {
	path     string
	released bool
}

// TryLock takes the drain lock without blocking. Returns ErrLocked when
// a live drainer holds it. A lock older than DefaultLockStale (or one
// whose body is unreadable and whose mtime is that old) is assumed to
// belong to a dead process and is taken over.
func (o *Outbox) TryLock() (*DrainLock, error) {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("outbox: mkdir %s: %w", o.Dir, err)
	}
	path := filepath.Join(o.Dir, lockFileName)

	err := o.acquireLock(path)
	if errors.Is(err, ErrLocked) && o.lockIsStale(path) {
		// Exactly one takeover attempt: if another process wins the
		// race to recreate the lock we lose it cleanly rather than
		// looping and stealing from a drainer that just started.
		_ = os.Remove(path)
		err = o.acquireLock(path)
	}
	if err != nil {
		return nil, err
	}
	return &DrainLock{path: path}, nil
}

// LockHeld reports whether a lock file is present. Cheap and advisory:
// callers use it to skip spawning a drainer that would immediately bail
// on ErrLocked. It does not consider staleness -- TryLock is the only
// place that decides a lock is dead.
func (o *Outbox) LockHeld() bool {
	_, err := os.Stat(filepath.Join(o.Dir, lockFileName))
	return err == nil
}

// Pending returns the number of drainable records in the queue. Stray
// `.tmp` writes and the lock file are excluded, so the count is what a
// drain would actually ship. A missing directory is zero, not an error.
func (o *Outbox) Pending() (int, error) {
	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("outbox: read dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == liveExtension {
			n++
		}
	}
	return n, nil
}

// acquireLock creates the lockfile with O_EXCL, so the create either
// wins outright or reports that someone else holds it.
func (o *Outbox) acquireLock(path string) error {
	// #nosec G304 -- path is filepath.Join(o.Dir, lockFileName); both
	// components are owned by this package.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return ErrLocked
		}
		return fmt.Errorf("outbox: create lock: %w", err)
	}
	data, merr := json.Marshal(lockInfo{PID: os.Getpid(), AcquiredAt: o.clock()})
	if merr != nil { // unreachable for this struct, but never leave a lock we cannot describe
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("outbox: marshal lock: %w", merr)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("outbox: write lock: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("outbox: close lock: %w", cerr)
	}
	return nil
}

// lockIsStale decides whether an existing lock may be taken over.
// Reads AcquiredAt when the body parses; falls back to the file's mtime
// when it does not, so a torn lockfile still ages out instead of
// wedging the queue.
func (o *Outbox) lockIsStale(path string) bool {
	age, ok := o.lockAge(path)
	return ok && age > DefaultLockStale
}

func (o *Outbox) lockAge(path string) (time.Duration, bool) {
	// #nosec G304 -- see acquireLock.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var info lockInfo
	if jerr := json.Unmarshal(data, &info); jerr == nil && !info.AcquiredAt.IsZero() {
		return o.clock().Sub(info.AcquiredAt), true
	}
	st, serr := os.Stat(path)
	if serr != nil {
		return 0, false
	}
	return o.clock().Sub(st.ModTime()), true
}

// Release removes the lockfile. Safe to call twice, and safe to call on
// a lock whose file another process already took over as stale.
func (l *DrainLock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("outbox: release lock: %w", err)
	}
	return nil
}

// clock returns the Outbox's time source, defaulting to time.Now for
// zero-value Outbox structs built outside New (tests do this).
func (o *Outbox) clock() time.Time {
	if o.now == nil {
		return time.Now()
	}
	return o.now()
}
