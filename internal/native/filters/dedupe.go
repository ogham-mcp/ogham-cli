package filters

import (
	"sync"
	"time"
)

const (
	// DedupWindow is the period during which a repeated
	// (session, tool, target) is treated as a duplicate. Within this
	// window the second event is skipped and the timestamp is
	// refreshed.
	DedupWindow = 5 * time.Minute
	// PruneThreshold drops entries older than this on each
	// IsDuplicate call to keep the map bounded. Set higher than
	// DedupWindow so refreshes don't race with prunes.
	PruneThreshold = 30 * time.Minute
)

type dedupeKey struct {
	sessionID string
	toolName  string
	target    string
}

// Deduper tracks recently seen (session, tool, target) tuples and
// reports duplicates within DedupWindow. Safe for concurrent use.
// The package-global instance (DefaultDeduper) is what hooks.go uses
// in production; tests build their own with an injected clock.
type Deduper struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[dedupeKey]time.Time
}

// NewDeduper builds a Deduper with the standard wall clock.
func NewDeduper() *Deduper {
	return &Deduper{
		now:     time.Now,
		entries: make(map[dedupeKey]time.Time),
	}
}

// NewDeduperWithClock builds a Deduper backed by an injected clock
// function. Used by tests to advance time deterministically.
func NewDeduperWithClock(now func() time.Time) *Deduper {
	return &Deduper{
		now:     now,
		entries: make(map[dedupeKey]time.Time),
	}
}

// IsDuplicate reports whether (sessionID, toolName, target) was seen
// within DedupWindow. If so, refreshes the timestamp (so repeated
// hits keep the entry hot) and returns true. Otherwise records the
// new tuple and returns false. Also prunes stale entries.
func (d *Deduper) IsDuplicate(sessionID, toolName, target string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	d.pruneLocked(now)

	key := dedupeKey{sessionID, toolName, target}
	if seenAt, ok := d.entries[key]; ok {
		if now.Sub(seenAt) < DedupWindow {
			d.entries[key] = now
			return true
		}
	}
	d.entries[key] = now
	return false
}

// Size returns the current entry count. Useful for tests and metrics.
func (d *Deduper) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

func (d *Deduper) pruneLocked(now time.Time) {
	for k, t := range d.entries {
		if now.Sub(t) > PruneThreshold {
			delete(d.entries, k)
		}
	}
}

// DefaultDeduper is the package-global Deduper used by the
// production runPostTool path. Tests should construct their own.
var DefaultDeduper = NewDeduper()
