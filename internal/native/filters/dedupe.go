package filters

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DedupWindow is the period during which a repeated
	// (session, tool, target) is treated as a duplicate. Within this
	// window the second event is skipped and the timestamp is
	// refreshed.
	DedupWindow = 5 * time.Minute
	// PruneThreshold drops entries older than this on each recording
	// sweep to keep the directory bounded. Set higher than DedupWindow
	// so refreshes don't race with prunes.
	PruneThreshold = 30 * time.Minute
)

// Deduper reports whether a (session, tool, target) tuple was already
// seen within DedupWindow.
//
// State lives on disk, one zero-byte marker file per tuple, with the
// marker's mtime as the timestamp. That is not an implementation
// detail -- it is the whole point. `ogham hooks run post-tool` is
// registered as a "type": "command" hook, so Claude Code spawns a
// fresh process for every tool call. The previous implementation kept
// entries in a package-global map, which was therefore empty on every
// invocation and could never report a duplicate (issue #26 finding 4,
// upstream TBU-206). Any state that has to outlive one hook event has
// to outlive the process.
//
// Safe for concurrent use within a process (mutex) and across
// processes (the claim is an O_CREATE|O_EXCL create, which POSIX makes
// atomic -- the same guarantee the outbox leans on for rename).
type Deduper struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
}

// NewDeduperAt builds a Deduper storing markers under dir, with an
// injected clock. The directory is created lazily on first record so
// constructing a Deduper is free and cannot fail.
func NewDeduperAt(dir string, now func() time.Time) *Deduper {
	if now == nil {
		now = time.Now
	}
	return &Deduper{dir: dir, now: now}
}

// DefaultDedupeDir resolves the on-disk dedupe location. Mirrors
// outbox.DefaultDir's contract, including the env override, so both
// pieces of hook state can be redirected the same way in tests and
// sandboxes.
func DefaultDedupeDir() (string, error) {
	if override := os.Getenv("OGHAM_DEDUPE_DIR"); override != "" {
		return override, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("dedupe: user cache dir: %w", err)
	}
	return filepath.Join(cache, "ogham", "dedupe"), nil
}

// markerName hashes the tuple into a flat, path-safe filename. Hashing
// rather than escaping keeps targets containing "/" or ".." from
// escaping dir, and keeps names bounded regardless of command length.
// The NUL separator makes the encoding unambiguous, so ("a", "bc") and
// ("ab", "c") cannot collide.
func markerName(sessionID, toolName, target string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + toolName + "\x00" + target))
	return hex.EncodeToString(sum[:])
}

// IsDuplicate reports whether (sessionID, toolName, target) was seen
// within DedupWindow. A hit refreshes the marker so repeated events
// keep the entry hot; a miss records it and prunes stale markers.
//
// Errors are deliberately swallowed into "not a duplicate". A hook
// that cannot write its dedupe state should still capture the event --
// losing a suppression is recoverable, losing the memory is not.
func (d *Deduper) IsDuplicate(sessionID, toolName, target string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	path := filepath.Join(d.dir, markerName(sessionID, toolName, target))

	if err := os.MkdirAll(d.dir, 0750); err != nil {
		return false
	}

	// Atomic claim: exactly one caller creates the marker.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_ = f.Close()
		d.stamp(path, now)
		d.pruneLocked(now)
		return false
	}
	if !os.IsExist(err) {
		return false // unwritable state -- fail open, capture the event
	}

	// Marker already present: duplicate only if it is still inside the
	// window. Read the age from mtime rather than tracking it in
	// memory, so a fresh process sees the same answer.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		return false
	}
	if now.Sub(fi.ModTime()) < DedupWindow {
		d.stamp(path, now) // refresh: keep hot entries hot
		return true
	}

	// Stale marker -- treat as a fresh sighting and restart its clock.
	d.stamp(path, now)
	d.pruneLocked(now)
	return false
}

// stamp sets a marker's mtime to the Deduper's notion of now. Explicit
// rather than relying on the filesystem clock so an injected test clock
// governs expiry.
func (d *Deduper) stamp(path string, now time.Time) {
	_ = os.Chtimes(path, now, now)
}

// Size returns the current marker count. Useful for tests and metrics.
func (d *Deduper) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// pruneLocked removes markers older than PruneThreshold. Called on the
// record path only -- duplicate hits are the hot path and skip the
// directory walk. Caller must hold d.mu.
func (d *Deduper) pruneLocked(now time.Time) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		if now.Sub(info.ModTime()) > PruneThreshold {
			_ = os.Remove(filepath.Join(d.dir, e.Name()))
		}
	}
}
