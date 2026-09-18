package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native/outbox"
)

func TestParseDrainModeAcceptsTheThreeModes(t *testing.T) {
	cases := map[string]drainMode{
		"async":   drainAsync,
		"sync":    drainSync,
		"off":     drainOff,
		" ASYNC ": drainAsync, // flags arrive from settings.json by hand
	}
	for in, want := range cases {
		got, err := parseDrainMode(in)
		if err != nil {
			t.Errorf("parseDrainMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseDrainMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDrainModeRejectsUnknown(t *testing.T) {
	if _, err := parseDrainMode("background"); err == nil {
		t.Fatal("parseDrainMode(\"background\") should error")
	} else if !strings.Contains(err.Error(), "async, sync, or off") {
		t.Errorf("error should name the valid modes, got: %v", err)
	}
}

func TestResolveDrainModePrecedence(t *testing.T) {
	// An explicit flag beats the env var; the env var beats the flag's
	// own default. Getting this backwards would make $OGHAM_DRAIN_MODE
	// silently dead, which is exactly the kind of thing nobody notices.
	tests := []struct {
		name      string
		flagValue string
		flagSet   bool
		env       string
		want      drainMode
	}{
		{"default when nothing set", "async", false, "", drainAsync},
		{"env applies when flag unset", "async", false, "sync", drainSync},
		{"flag wins over env", "off", true, "sync", drainOff},
		{"flag wins even matching default", "async", true, "off", drainAsync},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDrainMode(tc.flagValue, tc.flagSet, tc.env)
			if err != nil {
				t.Fatalf("resolveDrainMode: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveDrainModeRejectsBadEnv(t *testing.T) {
	if _, err := resolveDrainMode("async", false, "nope"); err == nil {
		t.Fatal("a bad OGHAM_DRAIN_MODE should error rather than fall back silently")
	}
}

func TestOpenOutboxReturnsNilWhenQueueNeverCreated(t *testing.T) {
	t.Setenv("OGHAM_OUTBOX_DIR", filepath.Join(t.TempDir(), "never-created"))
	box, err := openOutbox()
	if err != nil {
		t.Fatalf("openOutbox: %v", err)
	}
	if box != nil {
		t.Errorf("openOutbox created a queue directory that had never been written to")
	}
}

func TestMaybeDrainAsyncDoesNothingOnEmptyQueue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	if _, err := outbox.New(dir); err != nil {
		t.Fatalf("New: %v", err)
	}

	var stderr bytes.Buffer
	// cfg is nil on purpose: an empty queue must not reach the store,
	// so a nil config must not be dereferenced.
	maybeDrainAsync(context.Background(), nil, "work", 0, &stderr)

	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence on an empty queue", stderr.String())
	}
}

func TestMaybeDrainAsyncSkipsWhenAnotherDrainerHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	box, err := outbox.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := box.Write(&outbox.Record{Content: "Edit: /repo/foo.go"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lock, err := box.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	var stderr bytes.Buffer
	maybeDrainAsync(context.Background(), nil, "work", 0, &stderr)

	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence when a drainer is already running", stderr.String())
	}
	pending, err := box.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("Pending() = %d, want the record left for the running drainer", pending)
	}
}

func TestDrainOutboxLockedIsANoOpWhenLocked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	box, err := outbox.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := box.Write(&outbox.Record{Content: "Edit: /repo/foo.go"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lock, err := box.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	// nil cfg again: reaching native.Store here would be the bug.
	stats, err := drainOutboxLocked(context.Background(), nil, "work", 0, time.Second)
	if err != nil {
		t.Fatalf("drainOutboxLocked: %v", err)
	}
	if stats.Processed != 0 {
		t.Errorf("stats = %+v, want an untouched queue", stats)
	}
}

func TestDrainOutboxLockedReleasesTheLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	if _, err := outbox.New(dir); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Empty queue: the drain does nothing, but it must still hand the
	// lock back or the next session start would skip forever.
	if _, err := drainOutboxLocked(context.Background(), nil, "work", 0, time.Second); err != nil {
		t.Fatalf("drainOutboxLocked: %v", err)
	}
	box, err := outbox.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if box.LockHeld() {
		t.Errorf("drain left the lock behind")
	}
}

func TestReportDrainStatsSilentOnNoOp(t *testing.T) {
	var b bytes.Buffer
	reportDrainStats(&b, outbox.DrainStats{Remaining: 7})
	if b.Len() != 0 {
		t.Errorf("stats line = %q, want silence when nothing was handled", b.String())
	}
}

func TestReportDrainStatsNamesEveryCounter(t *testing.T) {
	var b bytes.Buffer
	reportDrainStats(&b, outbox.DrainStats{Processed: 3, Failed: 1, Orphaned: 2, Malformed: 1, Remaining: 4})
	got := b.String()
	for _, want := range []string{"processed=3", "failed=1", "orphaned=2", "malformed=1", "remaining=4"} {
		if !strings.Contains(got, want) {
			t.Errorf("stats line missing %q: %s", want, got)
		}
	}
}

func TestSpawnDetachedDrainReportsAnUnrunnableBinary(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // keep the real drain.log alone
	err := spawnDetachedDrain(filepath.Join(t.TempDir(), "no-such-ogham"), "work", 0)
	if err == nil {
		t.Fatal("spawnDetachedDrain should report a binary it cannot execute")
	}
	if !strings.Contains(err.Error(), "spawn") {
		t.Errorf("error should name the failed step, got: %v", err)
	}
}

func TestDrainChildArgsAlwaysTargetTheDrainVerb(t *testing.T) {
	// A child that ran session-start would spawn a drainer of its own,
	// and so would that one. Pin the verb.
	got := strings.Join(drainChildArgs("work", 0), " ")
	if got != "hooks run drain --profile work" {
		t.Errorf("drainChildArgs = %q", got)
	}
	if strings.Contains(got, "session-start") {
		t.Fatal("the detached child must never be a session-start invocation")
	}
}

func TestDrainChildArgsForwardBatchOnlyWhenSet(t *testing.T) {
	withBatch := strings.Join(drainChildArgs("work", 42), " ")
	if !strings.Contains(withBatch, "--drain-batch 42") {
		t.Errorf("drainChildArgs(batch=42) = %q, want --drain-batch forwarded", withBatch)
	}
	if strings.Contains(strings.Join(drainChildArgs("work", 0), " "), "--drain-batch") {
		t.Error("batch 0 means \"package default\"; it must not be passed explicitly")
	}
}

func TestSpawnDetachedDrainDoesNotInheritStdio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/echo as a stand-in for the ogham binary")
	}
	logPath := filepath.Join(t.TempDir(), "drain.log")
	t.Setenv(drainLogEnv, logPath)

	if err := spawnDetachedDrain("/bin/echo", "work", 42); err != nil {
		t.Fatalf("spawnDetachedDrain: %v", err)
	}

	// The child's stdout must land in the log, NOT on the hook's own
	// stdout -- an inherited pipe is what would keep the client blocked,
	// which is the whole bug (#51).
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		body, _ = os.ReadFile(logPath) // #nosec G304 -- test-owned temp path
		if len(body) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := string(body)
	if !strings.Contains(got, "hooks run drain") {
		t.Errorf("drain.log = %q, want the child's own argv", got)
	}
	if !strings.Contains(got, "--drain-batch 42") {
		t.Errorf("drain.log = %q, want --drain-batch forwarded to the child", got)
	}
}

func TestDrainLogPathHonoursOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "elsewhere.log")
	t.Setenv(drainLogEnv, want)
	if got := drainLogPath(); got != want {
		t.Errorf("drainLogPath() = %q, want %q", got, want)
	}
}

func TestDrainLogDefaultsUnderTheCacheDir(t *testing.T) {
	t.Setenv(drainLogEnv, "")
	got := drainLogPath()
	if got == "" {
		t.Skip("no usable user cache dir on this machine")
	}
	if filepath.Base(got) != "drain.log" || !strings.Contains(got, "ogham") {
		t.Errorf("drainLogPath() = %q, want <cache>/ogham/drain.log", got)
	}
}

func TestOpenDrainLogTruncatesBetweenSpawns(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "drain.log")
	t.Setenv(drainLogEnv, logPath)
	if err := os.WriteFile(logPath, []byte("stale output from a previous drain"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	f, err := openDrainLog()
	if err != nil {
		t.Fatalf("openDrainLog: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	body, err := os.ReadFile(logPath) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("log = %q, want truncated -- it must not grow without bound", body)
	}
}

func TestBackgroundDrainDeadlineStaysUnderTheLockStaleWindow(t *testing.T) {
	// If a background drain could outlive the lock's staleness window,
	// the next session start would take the lock from a live drainer and
	// both would ship the same records.
	if backgroundDrainDeadline >= outbox.DefaultLockStale {
		t.Fatalf("backgroundDrainDeadline (%v) must stay below outbox.DefaultLockStale (%v)",
			backgroundDrainDeadline, outbox.DefaultLockStale)
	}
}

// --- helpers ---

func mustOutbox(t *testing.T, dir string) *outbox.Outbox {
	t.Helper()
	box, err := outbox.New(dir)
	if err != nil {
		t.Fatalf("outbox.New(%s): %v", dir, err)
	}
	return box
}

func writeOutboxRecord(t *testing.T, box *outbox.Outbox) {
	t.Helper()
	if err := box.Write(&outbox.Record{Content: "Edit: /repo/foo.go"}); err != nil {
		t.Fatalf("outbox Write: %v", err)
	}
}
