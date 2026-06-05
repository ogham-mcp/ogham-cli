// Package outbox is the durable buffer between PostToolUse hook
// invocations and the memory store. Hook processes write a Record
// to the outbox directory and exit; a separate drainer (invoked on
// SessionStart) ships queued records to the store and deletes them.
//
// Design choice: directory queue rather than SQLite / bbolt.
//
//   - Hook processes are ephemeral (each `ogham hooks run post-tool`
//     is its own process). The drainer runs in a different process.
//     A long-lived embedded-DB connection adds no value when nobody
//     holds it open.
//   - POSIX guarantees `rename(2)` atomicity, so `write -> fsync ->
//     rename` produces either a complete final file or no file at
//     all. SIGKILL between fsync and rename leaves a stray `.tmp`
//     that the next drain cleans up. No torn records, ever.
//   - Filename scheme `{unix-nano}-{8hex}.jsonl` sorts string-wise
//     by enqueue time. Drain processes oldest-first.
//   - Zero CGO, zero binary bloat, zero new top-level dependencies
//     (google/uuid is already in tree). The drainer is `os.ReadDir`
//     + `os.ReadFile` + `os.Remove`.
//
// Bounds (from the v0.9 council perf seat):
//   - Drain deadline: 30 s (configurable via context).
//   - Per-drain cap: 1000 records (avoid hour-long SessionStart).
//   - Stray .tmp threshold: 5 minutes (covers slow disk + dead writers).
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultDrainBatch caps the number of records processed per
	// Drain call. Mirrors the council's "1000-row cap" guidance.
	DefaultDrainBatch = 1000
	// DefaultTmpThreshold is how old a `.tmp` file must be before
	// Drain treats it as an orphan and deletes it. Set higher than
	// the slowest plausible fsync + rename so we don't delete a
	// genuine in-flight write.
	DefaultTmpThreshold = 5 * time.Minute

	// liveExtension is the suffix for completed records ready to
	// drain. inflightExtension is for partial writes; renamed to
	// liveExtension atomically once fsync returns.
	liveExtension     = ".jsonl"
	inflightExtension = ".tmp"
)

// Outbox is a thin handle over a directory. Each Write produces one
// file; each Drain consumes some files. Safe for concurrent writers
// (each Write picks a unique filename) and one drainer at a time
// (drains are serialised by the SessionStart hook that owns them;
// concurrent drains would race on deletes but not on writers).
type Outbox struct {
	Dir           string
	DrainBatch    int           // 0 means DefaultDrainBatch
	TmpThreshold  time.Duration // 0 means DefaultTmpThreshold
	now           func() time.Time
}

// New constructs an Outbox over the given directory, creating it
// (mode 0700) if absent. Returns an error if the directory exists
// but is not writable.
func New(dir string) (*Outbox, error) {
	if dir == "" {
		return nil, errors.New("outbox: dir is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("outbox: mkdir %s: %w", dir, err)
	}
	return &Outbox{Dir: dir, now: time.Now}, nil
}

// Write persists rec to the outbox. The sequence is:
//  1. Open `{name}.tmp` with O_EXCL.
//  2. Marshal rec, write the bytes.
//  3. fsync the file.
//  4. Rename `.tmp` -> `.jsonl` (atomic per POSIX).
//
// A SIGKILL at any point before step 4 leaves either nothing or a
// stray `.tmp` that the next Drain cleans up. After step 4 the
// record is durable; an immediately following SIGKILL still leaves
// a fully-formed `.jsonl`.
func (o *Outbox) Write(rec *Record) error {
	if rec == nil {
		return errors.New("outbox: nil record")
	}
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = CurrentSchemaVersion
	}
	if rec.EnqueuedAt.IsZero() {
		rec.EnqueuedAt = o.now()
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("outbox: marshal: %w", err)
	}

	base := fmt.Sprintf("%020d-%s", rec.EnqueuedAt.UnixNano(), shortUUID())
	tmpPath := filepath.Join(o.Dir, base+inflightExtension)
	livePath := filepath.Join(o.Dir, base+liveExtension)

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("outbox: open tmp: %w", err)
	}
	if _, werr := f.Write(append(data, '\n')); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("outbox: write tmp: %w", werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("outbox: fsync tmp: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("outbox: close tmp: %w", cerr)
	}
	if rerr := os.Rename(tmpPath, livePath); rerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("outbox: rename: %w", rerr)
	}
	return nil
}

// Handler is called with each Record drained from the outbox. A nil
// return deletes the file (success); a non-nil error leaves it for
// the next drain. The handler MUST NOT modify rec.
type Handler func(ctx context.Context, rec *Record) error

// DrainStats summarises one Drain call.
type DrainStats struct {
	Processed   int // handler called and returned nil
	Failed      int // handler returned error; file kept
	Orphaned    int // stray .tmp deleted
	Malformed   int // file unparseable; quarantined
	Remaining   int // files left when we stopped (cap or deadline)
}

// Drain processes queued records oldest-first. Stops on context
// cancellation, DrainBatch limit, or when the directory is empty.
// Errors from individual handler calls are recorded in stats but
// don't abort the whole drain.
//
// Returns the file count left in the directory (for telemetry). Any
// I/O error reading the directory itself is returned; per-record
// errors stay in stats so partial progress isn't lost.
func (o *Outbox) Drain(ctx context.Context, handler Handler) (DrainStats, error) {
	stats := DrainStats{}
	if handler == nil {
		return stats, errors.New("outbox: nil handler")
	}

	cap := o.DrainBatch
	if cap <= 0 {
		cap = DefaultDrainBatch
	}
	tmpThresh := o.TmpThreshold
	if tmpThresh <= 0 {
		tmpThresh = DefaultTmpThreshold
	}

	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		return stats, fmt.Errorf("outbox: read dir: %w", err)
	}

	// Partition into live records and orphan .tmp files. Sort live
	// by filename (= unix-nano prefix) for oldest-first ordering.
	var live []string
	var orphans []string
	now := o.now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch filepath.Ext(name) {
		case liveExtension:
			live = append(live, name)
		case inflightExtension:
			info, ierr := e.Info()
			if ierr == nil && now.Sub(info.ModTime()) > tmpThresh {
				orphans = append(orphans, name)
			}
		}
	}
	sort.Strings(live)

	for _, name := range orphans {
		_ = os.Remove(filepath.Join(o.Dir, name))
		stats.Orphaned++
	}

	for i, name := range live {
		if ctx.Err() != nil {
			stats.Remaining = len(live) - i
			return stats, nil
		}
		if i >= cap {
			stats.Remaining = len(live) - i
			return stats, nil
		}

		path := filepath.Join(o.Dir, name)
		rec, rerr := readRecord(path)
		if rerr != nil {
			// Quarantine malformed file by renaming -- preserves it
			// for postmortem without blocking subsequent drains.
			_ = os.Rename(path, path+".malformed")
			stats.Malformed++
			continue
		}
		if herr := handler(ctx, rec); herr != nil {
			stats.Failed++
			continue
		}
		_ = os.Remove(path)
		stats.Processed++
	}

	return stats, nil
}

// readRecord loads one .jsonl file and parses its single line.
func readRecord(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec Record
	if jerr := json.Unmarshal(data, &rec); jerr != nil {
		return nil, jerr
	}
	if rec.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("outbox: future schema_version=%d in %s",
			rec.SchemaVersion, filepath.Base(path))
	}
	return &rec, nil
}

// shortUUID returns the first 8 hex chars of a v4 UUID. Used to
// disambiguate concurrent writers within the same nanosecond.
func shortUUID() string {
	return uuid.NewString()[:8]
}

// DefaultDir returns the canonical path for the per-user outbox.
// Used by the production runPostTool path; tests should pass their
// own t.TempDir().
func DefaultDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("outbox: user cache dir: %w", err)
	}
	return filepath.Join(cache, "ogham", "outbox"), nil
}

