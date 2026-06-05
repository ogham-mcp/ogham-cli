package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if o.Dir != dir {
		t.Errorf("Dir = %q, want %q", o.Dir, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("created path is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %v, want 0700", info.Mode().Perm())
	}
}

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Errorf("New(\"\") should error")
	}
}

func TestWriteAndDrainRoundtrip(t *testing.T) {
	o := mustNew(t)
	in := &Record{
		Content:   "Bash: git commit -m 'fix typo'",
		Profile:   "work",
		Source:    "hook:post-tool",
		Tags:      []string{"type:action", "tool:Bash"},
		SessionID: "session-abc",
		ToolName:  "Bash",
		Cwd:       "/tmp/proj",
	}
	if err := o.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var seen []*Record
	stats, err := o.Drain(context.Background(), func(_ context.Context, rec *Record) error {
		seen = append(seen, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Processed != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want Processed=1", stats)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 record drained, got %d", len(seen))
	}
	got := seen[0]
	if got.Content != in.Content {
		t.Errorf("Content = %q, want %q", got.Content, in.Content)
	}
	if got.Profile != in.Profile {
		t.Errorf("Profile = %q, want %q", got.Profile, in.Profile)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
	if got.EnqueuedAt.IsZero() {
		t.Errorf("EnqueuedAt was not set")
	}

	// After successful drain, directory should be empty of .jsonl.
	if files := globMust(t, o.Dir, "*.jsonl"); len(files) != 0 {
		t.Errorf("expected 0 .jsonl after drain, got %d", len(files))
	}
}

func TestDrainOldestFirst(t *testing.T) {
	o := mustNew(t)
	for i, content := range []string{"first", "second", "third"} {
		rec := &Record{
			Content:    content,
			Profile:    "work",
			Source:     "hook:post-tool",
			EnqueuedAt: time.Unix(int64(1000+i), 0),
		}
		if err := o.Write(rec); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}

	var order []string
	_, err := o.Drain(context.Background(), func(_ context.Context, rec *Record) error {
		order = append(order, rec.Content)
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(order) != len(want) {
		t.Fatalf("drained %v, want %v", order, want)
	}
	for i, c := range want {
		if order[i] != c {
			t.Errorf("position %d: got %q, want %q (full order: %v)", i, order[i], c, order)
		}
	}
}

func TestDrainKeepsFileOnHandlerError(t *testing.T) {
	o := mustNew(t)
	rec := &Record{Content: "fails", Profile: "work", Source: "hook:post-tool"}
	if err := o.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return errors.New("simulated downstream failure")
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Processed != 0 || stats.Failed != 1 {
		t.Errorf("stats = %+v, want Failed=1 Processed=0", stats)
	}
	if files := globMust(t, o.Dir, "*.jsonl"); len(files) != 1 {
		t.Errorf("expected file kept for retry, got %d", len(files))
	}
}

func TestDrainCleansUpOrphanTmp(t *testing.T) {
	o := mustNew(t)
	// Pretend a previous writer was SIGKILL'd between fsync and rename.
	orphan := filepath.Join(o.Dir, "00000000000000001000-deadbeef.tmp")
	if err := os.WriteFile(orphan, []byte(`{"content":"half-written"}`), 0o600); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	// Backdate the orphan past the threshold so the drain treats it as stale.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1 (stats=%+v)", stats.Orphaned, stats)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan file not deleted: %v", err)
	}
}

func TestDrainSparesFreshTmp(t *testing.T) {
	o := mustNew(t)
	// A .tmp that's only seconds old must NOT be deleted -- it could
	// be a live writer mid-fsync.
	fresh := filepath.Join(o.Dir, "00000000000000002000-cafebabe.tmp")
	if err := os.WriteFile(fresh, []byte(`{"content":"in-flight"}`), 0o600); err != nil {
		t.Fatalf("seed fresh tmp: %v", err)
	}

	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Orphaned != 0 {
		t.Errorf("Orphaned = %d, want 0 -- fresh .tmp should be spared", stats.Orphaned)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh .tmp deleted: %v", err)
	}
}

func TestDrainQuarantinesMalformedJSONL(t *testing.T) {
	o := mustNew(t)
	bad := filepath.Join(o.Dir, "00000000000000003000-baadf00d.jsonl")
	if err := os.WriteFile(bad, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seed malformed: %v", err)
	}

	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Malformed != 1 {
		t.Errorf("Malformed = %d, want 1", stats.Malformed)
	}
	// Original gone, quarantined file present.
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Errorf("malformed original not renamed away")
	}
	files := globMust(t, o.Dir, "*.malformed")
	if len(files) != 1 {
		t.Errorf("expected 1 .malformed file, got %d", len(files))
	}
}

func TestDrainHonoursDrainBatch(t *testing.T) {
	o := mustNew(t)
	o.DrainBatch = 3
	for i := 0; i < 5; i++ {
		if err := o.Write(&Record{
			Content:    "rec",
			Profile:    "work",
			Source:     "hook:post-tool",
			EnqueuedAt: time.Unix(int64(1000+i), 0),
		}); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}
	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Processed != 3 {
		t.Errorf("Processed = %d, want 3 (cap)", stats.Processed)
	}
	if stats.Remaining != 2 {
		t.Errorf("Remaining = %d, want 2", stats.Remaining)
	}
}

func TestDrainStopsOnContextCancel(t *testing.T) {
	o := mustNew(t)
	for i := 0; i < 5; i++ {
		if err := o.Write(&Record{Content: "rec", Profile: "work", Source: "h"}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	stats, err := o.Drain(ctx, func(_ context.Context, _ *Record) error {
		count++
		if count == 2 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Processed < 2 {
		t.Errorf("Processed = %d, want >=2 before cancel", stats.Processed)
	}
	if stats.Remaining == 0 {
		t.Errorf("Remaining = 0, expected some left after cancel")
	}
}

func TestConcurrentWritersUniqueFilenames(t *testing.T) {
	o := mustNew(t)
	var wg sync.WaitGroup
	const n = 40
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = o.Write(&Record{
				Content: "concurrent",
				Profile: "work",
				Source:  "hook:post-tool",
			})
		}(i)
	}
	wg.Wait()
	files := globMust(t, o.Dir, "*.jsonl")
	if len(files) != n {
		t.Errorf("concurrent writers produced %d files, want %d (filename collision?)", len(files), n)
	}
}

func TestWriteRejectsFutureSchemaOnDrain(t *testing.T) {
	o := mustNew(t)
	// Hand-craft a future-schema file. The drainer should quarantine
	// it rather than crash on the unknown schema.
	rec := Record{
		SchemaVersion: 999,
		EnqueuedAt:    time.Unix(5000, 0),
		Content:       "from-the-future",
	}
	data, _ := json.Marshal(rec)
	path := filepath.Join(o.Dir, "00000000000000005000-feedbeef.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stats, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Malformed != 1 {
		t.Errorf("Malformed = %d, want 1 (future schema)", stats.Malformed)
	}
}

func TestWriteRejectsNilRecord(t *testing.T) {
	o := mustNew(t)
	if err := o.Write(nil); err == nil {
		t.Errorf("Write(nil) should error")
	}
}

func TestDefaultDirNonEmpty(t *testing.T) {
	t.Setenv("OGHAM_OUTBOX_DIR", "")
	d, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if !strings.Contains(d, "ogham") || !strings.HasSuffix(d, "outbox") {
		t.Errorf("DefaultDir = %q, want path containing 'ogham' ending in 'outbox'", d)
	}
}

func TestDefaultDirHonoursEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-queue")
	t.Setenv("OGHAM_OUTBOX_DIR", override)
	d, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if d != override {
		t.Errorf("DefaultDir = %q, want %q from OGHAM_OUTBOX_DIR", d, override)
	}
}

// --- helpers ---

func mustNew(t *testing.T) *Outbox {
	t.Helper()
	o, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return o
}

func globMust(t *testing.T, dir, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	return matches
}
