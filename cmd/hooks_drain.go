package cmd

// Outbox draining strategy (#51).
//
// PostToolUse queues one record per write-class tool call; something has
// to ship those records to the store. Until #51 that something was
// SessionStart, inline, unbounded -- so startup wall time scaled with how
// many edits had happened since the previous session. Measured on the
// reporter's machine: median 12.0 s, worst 27.1 s, against 0.86 s for the
// same hook with an empty queue. The hook blocks the client, so that was
// dead time before the user could type.
//
// The fix is to move the drain off the startup path rather than make it
// faster: SessionStart now re-execs this binary as a detached
// `hooks run drain` child and returns the recall context immediately.
// Startup cost becomes constant and independent of the backlog.
//
// Two invariants that the shape depends on:
//
//  1. The child must not inherit the hook's stdout/stderr. A hook client
//     waits on those pipes closing, so an inherited pipe would make the
//     "detached" drain block startup exactly as before -- the bug, with
//     more moving parts. The child's output goes to a truncate-on-open
//     log in the cache dir instead, which also gives a place to look
//     when a drain fails.
//  2. Two drainers can now overlap (a long background drain still running
//     when the next session starts). outbox.TryLock serialises them; a
//     drainer that cannot get the lock exits quietly, because the process
//     holding it is already doing the work.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/native/outbox"
)

// drainMode selects where the outbox drain happens relative to the
// SessionStart hook that triggers it.
type drainMode string

const (
	// drainAsync (default) spawns a detached drainer and returns. The
	// hook's wall time no longer depends on the queue depth.
	drainAsync drainMode = "async"
	// drainSync keeps the pre-#51 behaviour: drain inline, then build
	// the session context. Bounded by drainDeadline and --drain-batch.
	// Kept for users who want the store guaranteed current before the
	// session's first recall, and for debugging a drain that fails
	// silently in the background.
	drainSync drainMode = "sync"
	// drainOff skips the drain entirely. The queue still fills; some
	// later run with a different mode ships it.
	drainOff drainMode = "off"
)

const (
	// backgroundDrainDeadline bounds the detached drainer. Generous --
	// nobody is waiting on it -- but finite, so a wedged backend cannot
	// leave a process running until reboot. Must stay well under
	// outbox.DefaultLockStale or a second drainer could take the lock
	// from a live one.
	backgroundDrainDeadline = 10 * time.Minute

	// asyncFallbackBatch / asyncFallbackDeadline bound the inline drain
	// that async mode falls back to when the spawn itself fails (no
	// exec permission, a sandbox with no fork, a read-only cache dir).
	// Without a fallback, a machine that can never spawn would queue
	// forever; with an unbounded one, it would be the original bug. So:
	// ship a little, quickly, and let the next session ship the next
	// slice.
	asyncFallbackBatch    = 25
	asyncFallbackDeadline = 5 * time.Second

	// drainLogEnv redirects the detached drainer's log. Chiefly for
	// tests and for operators who want the log somewhere collected.
	drainLogEnv = "OGHAM_DRAIN_LOG"

	// drainModeEnv overrides the default mode without editing the hook
	// command in settings.json. The explicit --drain flag still wins.
	drainModeEnv = "OGHAM_DRAIN_MODE"
)

// parseDrainMode validates a --drain / $OGHAM_DRAIN_MODE value.
func parseDrainMode(s string) (drainMode, error) {
	switch drainMode(strings.TrimSpace(strings.ToLower(s))) {
	case drainAsync:
		return drainAsync, nil
	case drainSync:
		return drainSync, nil
	case drainOff:
		return drainOff, nil
	default:
		return "", fmt.Errorf("invalid drain mode %q (use async, sync, or off)", s)
	}
}

// resolveDrainMode picks the effective mode: an explicitly-passed flag
// beats $OGHAM_DRAIN_MODE, which beats the built-in default. flagSet
// reports whether the user actually typed --drain, so the env var is not
// silently overridden by the flag's own default value.
func resolveDrainMode(flagValue string, flagSet bool, env string) (drainMode, error) {
	if flagSet {
		return parseDrainMode(flagValue)
	}
	if env != "" {
		return parseDrainMode(env)
	}
	return parseDrainMode(flagValue)
}

// openOutbox resolves and opens the default outbox. Returns (nil, nil)
// when the queue directory does not exist yet -- nothing has ever been
// queued, so there is nothing to drain and nothing to create.
func openOutbox() (*outbox.Outbox, error) {
	dir, err := outbox.DefaultDir()
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return nil, nil
	}
	return outbox.New(dir)
}

// drainOutboxLocked ships queued records to the store under the drain
// lock. Returns (stats, nil) having done nothing when another drainer
// holds the lock -- that is a normal outcome, not a failure.
//
// batch <= 0 means outbox.DefaultDrainBatch.
func drainOutboxLocked(
	ctx context.Context,
	cfg *native.Config,
	profile string,
	batch int,
	deadline time.Duration,
) (outbox.DrainStats, error) {
	var stats outbox.DrainStats

	box, err := openOutbox()
	if err != nil || box == nil {
		return stats, err
	}
	box.DrainBatch = batch

	lock, err := box.TryLock()
	if err != nil {
		if errors.Is(err, outbox.ErrLocked) {
			return stats, nil // another drainer owns this queue right now
		}
		return stats, err
	}
	defer func() { _ = lock.Release() }()

	dctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	return box.Drain(dctx, func(c context.Context, rec *outbox.Record) error {
		recProfile := rec.Profile
		if recProfile == "" {
			recProfile = profile
		}
		_, sErr := native.Store(c, cfg, rec.Content, native.StoreOptions{
			Tags:    rec.Tags,
			Source:  rec.Source,
			Profile: recProfile,
		})
		return sErr
	})
}

// reportDrainStats writes the one-line drain summary, unless the drain
// was a no-op. Shared by the inline and background paths so their output
// cannot drift apart.
func reportDrainStats(w io.Writer, stats outbox.DrainStats) {
	if stats.Processed+stats.Failed+stats.Orphaned+stats.Malformed == 0 {
		return
	}
	fmt.Fprintf(w,
		"ogham: drained outbox -- processed=%d failed=%d orphaned=%d malformed=%d remaining=%d\n",
		stats.Processed, stats.Failed, stats.Orphaned, stats.Malformed, stats.Remaining)
}

// runNativeDrain is the `hooks run drain` event: ship the queue and
// exit. It is what the detached child runs, and it is also runnable by
// hand to flush the queue on demand or to see why a background drain is
// failing.
func runNativeDrain(ctx context.Context, cfg *native.Config, profile string, batch int) error {
	stats, err := drainOutboxLocked(ctx, cfg, profile, batch, backgroundDrainDeadline)
	reportDrainStats(os.Stderr, stats)
	return err
}

// drainLogPath is where a detached drainer's output goes. Truncated on
// each spawn, so it holds the most recent background drain only and
// cannot grow without bound. Empty string when there is no usable cache
// dir -- callers fall back to os.DevNull.
//
// Honours OGHAM_DRAIN_LOG for ops-level overrides and tests, mirroring
// OGHAM_OUTBOX_DIR on the queue itself.
func drainLogPath() string {
	if override := os.Getenv(drainLogEnv); override != "" {
		return override
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "ogham", "drain.log")
}

// openDrainLog returns the file the detached child's stdout/stderr are
// wired to. Never returns the caller's own streams: inheriting the
// hook's pipes is what would make the "detached" drain block startup.
func openDrainLog() (*os.File, error) {
	if path := drainLogPath(); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			// #nosec G304 -- path is the cache-dir default or an
			// explicit operator-set OGHAM_DRAIN_LOG, not caller input.
			if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err == nil {
				return f, nil
			}
		}
	}
	return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
}

// drainChildArgs builds the detached drainer's argv (everything after
// the binary path). Split out from spawnDetachedDrain so the shape can
// be asserted without spawning anything: the child MUST be a `drain`
// invocation, because a `session-start` child would spawn a drainer of
// its own and fork-bomb the machine.
func drainChildArgs(profile string, batch int) []string {
	args := []string{"hooks", "run", "drain", "--profile", profile}
	if batch > 0 {
		args = append(args, "--drain-batch", fmt.Sprint(batch))
	}
	return args
}

// spawnDetachedDrain starts `<this binary> hooks run drain` in its own
// session and returns without waiting. The child outlives this process,
// which is the point: the hook exits, the client unblocks, and the
// backlog ships in the background.
func spawnDetachedDrain(binPath, profile string, batch int) error {
	args := drainChildArgs(profile, batch)

	// #nosec G204 -- binPath is this process's own executable path
	// (os.Executable / exec.LookPath via oghamBinaryPath); the args are
	// literals plus a profile name and an integer. Nothing here is
	// shell-interpreted: exec.Command does not use a shell.
	cmd := exec.Command(binPath, args...)

	logFile, err := openDrainLog()
	if err != nil {
		return fmt.Errorf("drain: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }() // the child keeps its own fd

	cmd.Stdin = nil // /dev/null
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("drain: spawn: %w", err)
	}
	// Release drops our handle on the child without reaping it; we are
	// about to exit anyway and init will adopt it.
	return cmd.Process.Release()
}

// maybeDrainAsync is the SessionStart-side of async mode. It spawns a
// detached drainer when there is something to drain and nobody else is
// draining, and falls back to a small bounded inline drain when the
// spawn fails. Warnings go to stderr; nothing here is fatal to the
// session-start context that follows.
func maybeDrainAsync(ctx context.Context, cfg *native.Config, profile string, batch int, w io.Writer) {
	box, err := openOutbox()
	if err != nil {
		fmt.Fprintf(w, "ogham: outbox drain warning: %v\n", err)
		return
	}
	if box == nil {
		return // nothing has ever been queued
	}
	pending, err := box.Pending()
	if err != nil {
		fmt.Fprintf(w, "ogham: outbox drain warning: %v\n", err)
		return
	}
	if pending == 0 || box.LockHeld() {
		// Nothing to ship, or a drainer from a previous session is
		// still shipping it. Either way, do not spawn.
		return
	}

	spawnErr := spawnDetachedDrain(oghamBinaryPath(), profile, batch)
	if spawnErr == nil {
		return
	}
	fmt.Fprintf(w, "ogham: background drain unavailable (%v); draining %d inline\n",
		spawnErr, asyncFallbackBatch)

	stats, derr := drainOutboxLocked(ctx, cfg, profile, asyncFallbackBatch, asyncFallbackDeadline)
	reportDrainStats(w, stats)
	if derr != nil {
		fmt.Fprintf(w, "ogham: outbox drain warning: %v\n", derr)
	}
}
