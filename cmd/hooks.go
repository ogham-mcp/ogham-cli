package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/native/filters"
	"github.com/ogham-mcp/ogham-cli/internal/native/outbox"
	"github.com/spf13/cobra"
)

// drainDeadline bounds how long an INLINE drain (`--drain sync`) spends
// shipping queued PostToolUse records before falling through to the
// actual session-context build. Mirrors the council perf-seat 30s figure.
//
// The default path no longer drains inline at all -- see cmd/hooks_drain.go
// and #51. The background drainer uses backgroundDrainDeadline instead,
// because nothing is waiting on it.
const drainDeadline = 30 * time.Second

// defaultPostToolMatcher scopes PostToolUse to write-class tools, so the
// hook fires only on calls that produce content worth capturing.
//
// Pre-v0.8 the matcher was "", which fires on every tool call -- read-class
// tools (Read, Grep, Glob) produce noise the filter then discards anyway.
// See #10.
//
// Bash was dropped in #26 step 3, on measurement rather than taste. On the
// reference store, hook:post-tool was 9,580 of 11,957 rows (80%) with
// tool:Bash the dominant tag at 7,819, and only 104 of those rows -- 1.1%
// -- had ever been recalled. Sampling the recalled ones showed raw stdout
// captures, not anything worth keeping: a nonzero access_count means the
// row matched a similarity query, not that it was useful.
//
// Write and Edit stay because they carry a durable target. "Edit:
// /repo/foo.go" is a fact about the project that stays true and is worth
// recalling; "Bash: cd x && ls" is a keystroke.
//
// TestDefaultPostToolMatcherExcludesBash pins this. Re-adding Bash means
// re-running the measurement, not editing the test.
const defaultPostToolMatcher = "Write|Edit"

// oghamGoBinaryNames enumerates the names this Go binary ships under.
// Every one of them is matched by the Go-owned regexes above and below.
//
// There are four, and which one you get depends on what else is on the
// machine rather than on the build:
//
//	ogham       when the Python ogham-mcp is NOT installed and the
//	            name is free
//	ogham-cli   the pre-v0.7.4 install name
//	omcli       when ogham-mcp IS installed, since it owns `ogham`
//	om          the same, for the OpenBrain project
//
// `omcli` and `om` were added on evidence, not speculation: the machine
// that reported #51 runs `~/.local/bin/omcli`, because that laptop also
// develops the Python ogham-mcp. Without them the "Go-owned" regex
// matched none of that machine's four hook entries, so `hooks install`
// stacked duplicates instead of replacing (its whole idempotency claim),
// `hooks uninstall` removed nothing, and the stale-wiring warning below
// was silent on precisely the install that needed it.
//
// Adding a name here widens what `hooks install` REPLACES and what
// `hooks uninstall` DELETES, so a name goes in only once it is confirmed
// to be this binary. #7 cuts both ways: never clobber a config you do
// not own, and never orphan one you do.
//
// The Python package's console script is `ogham` and always uses the
// two-token `hooks <verb>` form, so widening the NAME list cannot make
// the Go matcher eat a Python entry -- the `run` token is what separates
// them, and that is unchanged. TestOghamGoHookCommandRegex pins both.
//
// `om` is short enough to be worth stating that it cannot match a longer
// name by accident: the pattern requires whitespace immediately after
// the name, so `omnibus hooks run x` does not match. Pinned by test.
const oghamGoBinaryNames = `ogham-cli|ogham|omcli|om`

// oghamGoHookCommandRegex matches hook commands owned by THIS Go binary.
// The verb shape `hooks run <verb>` distinguishes the Go CLI's three-token
// form from the Python ogham-mcp's two-token `hooks <verb>` form, so the
// idempotent install pre-pass and `hooks uninstall` only touch Go-owned
// entries -- a user with both Python and Go ogham binaries installed
// won't have their Python hook lines accidentally stripped (#7).
//
// Matches:
//
//	ogham-cli hooks run session-start           (pre-v0.7.4 broken form)
//	/usr/local/bin/ogham hooks run session-start
//	/Users/foo/.local/bin/ogham hooks run recall
//	/Users/foo/.local/bin/omcli hooks run inscribe
//	/Users/foo/.local/bin/om hooks run post-tool
//
// Does NOT match (Python ogham-mcp):
//
//	/path/to/.venv/bin/ogham hooks recall
//	/path/to/.venv/bin/ogham hooks inscribe
var oghamGoHookCommandRegex = regexp.MustCompile(
	`(?:^|/)(?:` + oghamGoBinaryNames + `)\s+hooks\s+run\s+`)

// oghamPythonHookCommandRegex matches hook commands owned by the Python
// ogham-mcp package: the two-token `ogham hooks <verb>` form, as opposed
// to the Go binary's three-token `ogham hooks run <verb>`.
//
// The verbs are enumerated rather than excluding "run" with a negative
// lookahead, which Go's RE2 does not support. Listing them is also
// stricter: an unrelated `sometool hooks deploy` will not match.
//
// We never delete what this matches without --replace-python (#7: do not
// clobber another tool's config). We do report it (#30: a user running
// both gets two hooks per event, the Python one unscoped, writing to the
// same store).
var oghamPythonHookCommandRegex = regexp.MustCompile(
	`(?:^|/)(ogham-cli|ogham)\s+hooks\s+(session-start|post-tool|inscribe|recall)\b`)

// pythonHookEntry is one detected Python-owned hook, for reporting.
type pythonHookEntry struct {
	Event   string
	Command string
}

// detectPythonHooks returns every Python-owned hook command in settings,
// sorted by event for stable output. Read-only.
func detectPythonHooks(settings map[string]any) []pythonHookEntry {
	var found []pythonHookEntry
	forEachHookCommand(settings, func(event, command string) {
		if oghamPythonHookCommandRegex.MatchString(command) {
			found = append(found, pythonHookEntry{Event: event, Command: command})
		}
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].Event != found[j].Event {
			return found[i].Event < found[j].Event
		}
		return found[i].Command < found[j].Command
	})
	return found
}

// forEachHookCommand walks settings["hooks"] and calls fn for every
// inner hook command string. Shared by the Go and Python scanners so
// they cannot disagree about the config shape.
func forEachHookCommand(settings map[string]any, fn func(event, command string)) {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return
	}
	for event, eventHooksRaw := range hooks {
		eventHooks, ok := eventHooksRaw.([]any)
		if !ok {
			continue
		}
		for _, entry := range eventHooks {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			inner, ok := entryMap["hooks"].([]any)
			if !ok {
				continue
			}
			for _, h := range inner {
				hMap, ok := h.(map[string]any)
				if !ok {
					continue
				}
				if cmd, ok := hMap["command"].(string); ok {
					fn(event, cmd)
				}
			}
		}
	}
}

// prunePythonHooks strips Python-owned hook entries. Only ever called
// behind --replace-python: deleting another tool's configuration is not
// something an install should do on its own initiative.
func prunePythonHooks(settings map[string]any) int {
	return pruneHooksMatching(settings, oghamPythonHookCommandRegex)
}

// formatPythonHookWarning renders the coexistence notice, or "" when
// there is nothing to report. Kept separate from the install path so
// its content is testable without touching the filesystem.
func formatPythonHookWarning(found []pythonHookEntry, settingsPath string) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nogham: found %d existing ogham-mcp (Python) hook entr%s in %s:\n",
		len(found), plural(len(found), "y", "ies"), settingsPath)
	for _, f := range found {
		fmt.Fprintf(&b, "    %-14s %s\n", f.Event, f.Command)
	}
	b.WriteString("  Both installs now fire on every event and write to the same store,\n")
	b.WriteString("  so you will get duplicate memories. The Python PostToolUse hook is\n")
	b.WriteString("  also unscoped (matcher \"\"), so it captures every tool call.\n")
	b.WriteString("  Remove those entries, or re-run: ogham hooks install --replace-python\n")
	return b.String()
}

// oghamDeprecatedHookCommandRegex matches Go-owned hook commands for
// verbs this binary still runs but no longer wires. Today that is
// `hooks run inscribe`, deprecated in v0.8 (#11).
//
// It is deliberately narrower than oghamGoHookCommandRegex: a stale
// entry is a thing to warn about, not a thing to delete. `hooks
// uninstall` already removes Go-owned entries wholesale for users who
// want them gone.
var oghamDeprecatedHookCommandRegex = regexp.MustCompile(
	`(?:^|/)(?:` + oghamGoBinaryNames + `)\s+hooks\s+run\s+inscribe\b`)

// deprecatedHookEntry is one stale Go-owned hook wiring, for reporting.
type deprecatedHookEntry struct {
	Event   string
	Command string
	Why     string
}

// detectDeprecatedHooks finds Go-owned hook entries whose verb is
// deprecated, sorted by event for stable output. Read-only.
//
// #51 (aside): a machine installed before v0.8 keeps its `PreCompact ->
// hooks run inscribe` entry forever -- `hooks install` only replaces
// entries it writes, and v0.8 stopped writing that one. Nothing surfaced
// the leftover, so it kept firing and kept writing metadata-only stubs.
func detectDeprecatedHooks(settings map[string]any) []deprecatedHookEntry {
	var found []deprecatedHookEntry
	forEachHookCommand(settings, func(event, command string) {
		if oghamDeprecatedHookCommandRegex.MatchString(command) {
			found = append(found, deprecatedHookEntry{
				Event:   event,
				Command: command,
				Why:     "inscribe was deprecated in v0.8 (#11): it writes a metadata-only stub on every compact, which dilutes recall",
			})
		}
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].Event != found[j].Event {
			return found[i].Event < found[j].Event
		}
		return found[i].Command < found[j].Command
	})
	return found
}

// formatDeprecatedHookWarning renders the stale-wiring notice, or "" when
// there is nothing to report. Split from the status command so its
// content is testable without touching the filesystem.
func formatDeprecatedHookWarning(found []deprecatedHookEntry, settingsPath, binName string) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nogham: %d deprecated ogham hook entr%s still wired in %s:\n",
		len(found), plural(len(found), "y", "ies"), settingsPath)
	for _, f := range found {
		fmt.Fprintf(&b, "    %-14s %s\n", f.Event, f.Command)
		fmt.Fprintf(&b, "      %s\n", f.Why)
	}
	fmt.Fprintf(&b, "  Fix: %[1]s hooks uninstall && %[1]s hooks install\n", binName)
	fmt.Fprintf(&b, "  Then commit pre-distilled content explicitly with `%s inscribe`.\n", binName)
	return b.String()
}

// noticeInscribeDeprecated emits the one-line runtime deprecation notice
// for the inscribe hook event. Unlike the post-tool notice this is NOT
// marker-gated: the whole point is that the wiring is stale and fires
// repeatedly, so a once-per-machine notice would be read and forgotten
// while the stubs kept accumulating. One line, on stderr, every time.
func noticeInscribeDeprecated(w io.Writer, binName string) {
	fmt.Fprintf(w,
		"ogham: `hooks run inscribe` is deprecated (v0.8, #11) -- it writes a metadata-only stub per compact, which dilutes recall. Re-wire with `%[1]s hooks uninstall && %[1]s hooks install`, and use `%[1]s inscribe` for real content.\n",
		binName)
}

// oghamInvocationName is the binary name to print in remedies: the name
// this binary is actually installed under, not the project's name. They
// differ in the wild (#51 -- see oghamGoBinaryNames), and a copy-pasteable
// command has to name the binary the user actually has.
func oghamInvocationName() string {
	return filepath.Base(oghamBinaryPath())
}

// oghamBinaryPath returns the absolute path of the running ogham binary,
// for use when writing hook commands into settings.json. Resolution order:
//
//  1. os.Executable() -- the binary that's actually running, regardless of
//     $PATH state at execution time. Matches the pattern cmd/plugin.go
//     uses for openclaw/agent-zero emitters.
//  2. exec.LookPath("ogham") -- fallback if os.Executable() can't resolve
//     (rare, generally only on platforms where /proc/self/exe is missing).
//  3. Bare "ogham" -- last resort. Will still trigger #2's $PATH issue
//     for users with no ogham on $PATH, but at least the hooks install
//     succeeds and the user gets a diagnostic when the hook fires.
//
// The returned path is what Claude Code will execute when SessionStart /
// PostToolUse / PreCompact / PostCompact fires.
func oghamBinaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	if p, err := exec.LookPath("ogham"); err == nil && p != "" {
		return p
	}
	return "ogham"
}

// oghamHookCommand formats the command string for a Go-side hook event.
// Mirrors `ogham hooks run <verb>` with the resolved absolute binary path.
func oghamHookCommand(verb string) string {
	return oghamHookCommandFor(oghamBinaryPath(), verb)
}

// oghamHookCommandFor is the testable form of oghamHookCommand: callers
// can pass an explicit binary path instead of resolving from
// os.Executable(). The split lets buildOghamHookSet stay pure (no
// filesystem reads) so the matcher/wired-or-skipped logic is unit-testable.
func oghamHookCommandFor(binPath, verb string) string {
	return fmt.Sprintf("%s hooks run %s", binPath, verb)
}

// buildOghamHookSet returns the map of Claude Code hook event names to
// their hook entries. v0.9 (#278) wires PostToolUse unconditionally:
// the native post-tool path (Classify -> MaskSecrets -> outbox.Write)
// works without a gateway api_key, so v0.8's apiKey gate is gone.
// Users with active gateway setups can still opt back into the
// synchronous gateway path with `ogham hooks run post-tool --gateway`.
//
// Pure helper: no filesystem access, no config loading. apiKey is
// accepted for ABI compatibility with v0.7/v0.8 callers and unit-test
// fixtures but no longer affects the output.
//
// PostToolUse uses defaultPostToolMatcher rather than "" so the hook
// only fires on write-class tools (Write / Edit).
//
// #11: PreCompact -> inscribe is NOT in the default scaffold from v0.8
// onwards. The legacy native inscribe writes a metadata-only stub on
// every compact event (session_id / cwd / timestamp only -- no
// transcript content), which dilutes recall at scale. Users keep their
// existing entries until they `hooks uninstall` then `hooks install`.
// The new explicit `ogham inscribe` verb is the preferred commit
// primitive for pre-distilled content (whether from a transcript
// reader, a skill, or a future plugin -- see the superpowers-memory
// bridge spec §4.3 for the signal-gated + staged + distilled pattern
// the verb is designed to compose with).
func buildOghamHookSet(apiKey, binPath string) map[string]map[string]any {
	_ = apiKey // parameter kept for ABI compat; see doc comment.
	return map[string]map[string]any{
		"SessionStart": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "session-start")}},
		},
		"PostCompact": {
			"matcher": "",
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "recall")}},
		},
		"PostToolUse": {
			"matcher": defaultPostToolMatcher,
			"hooks":   []map[string]string{{"type": "command", "command": oghamHookCommandFor(binPath, "post-tool")}},
		},
	}
}

// postToolNoticeMarkerPath returns the cache-dir marker that tracks
// whether the "post-tool fired without gateway key" notice has already
// been emitted on this machine. Empty string when UserCacheDir errors --
// callers treat that as "always emit" so the user still gets diagnostic
// output even on systems without a usable cache dir.
func postToolNoticeMarkerPath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "ogham", "post-tool-unconfigured-notice")
}

// noticePostToolUnconfiguredOnce emits a one-time stderr diagnostic when
// the post-tool hook fires without a gateway api_key configured. Uses
// markerPath as a stash file so subsequent invocations stay silent.
// Returns true when it actually wrote a notice, false when the marker
// already existed (idempotent re-entry).
//
// Defense-in-depth for #10: settings.json may pre-date the v0.8
// install-time skip; without this fallback, every tool call would spawn a
// subprocess that exits non-zero and Claude Code would log a hook error
// on every turn.
func noticePostToolUnconfiguredOnce(markerPath string, w io.Writer) bool {
	if markerPath != "" {
		if _, err := os.Stat(markerPath); err == nil {
			return false // already notified on this machine
		}
		_ = os.MkdirAll(filepath.Dir(markerPath), 0700)
		_ = os.WriteFile(markerPath, []byte("noticed\n"), 0600)
	}
	fmt.Fprintln(w, "ogham: post-tool ran with --gateway but no gateway api_key configured -- skipping (exit 0).")
	fmt.Fprintln(w, "  To use the v0.9 native path: drop the --gateway flag (or omit it in your settings.json hook entry).")
	fmt.Fprintln(w, "  To keep the gateway path: run `ogham auth login --api-key KEY`.")
	if markerPath != "" {
		fmt.Fprintf(w, "  This notice will not repeat (marker: %s).\n", markerPath)
	}
	return true
}

// pruneOghamGoHooks walks settings["hooks"] and strips any inner hook
// command matching oghamGoHookCommandRegex. Returns the number of inner
// hook commands removed. Mutates the passed settings map in place.
//
// Used as the idempotent pre-pass on install (so re-running `hooks
// install` after a broken-binary-name install cleans up before adding the
// fresh entries) and as the core of `hooks uninstall`.
//
// Leaves Python `ogham hooks <verb>` entries untouched -- those are owned
// by ogham-mcp, not by us. See detectPythonHooks / prunePythonHooks for
// the opt-in path that does remove them (#30).
func pruneOghamGoHooks(settings map[string]any) int {
	return pruneHooksMatching(settings, oghamGoHookCommandRegex)
}

// pruneHooksMatching is the shared filter behind pruneOghamGoHooks and
// prunePythonHooks: strip every inner hook whose command matches re,
// dropping matcher blocks and events left empty. Returns the number of
// inner hook commands removed. Mutates settings in place.
func pruneHooksMatching(settings map[string]any, re *regexp.Regexp) int {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	removed := 0
	for event, eventHooksRaw := range hooks {
		eventHooks, ok := eventHooksRaw.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(eventHooks))
		for _, entry := range eventHooks {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			innerHooks, ok := entryMap["hooks"].([]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			filteredInner := make([]any, 0, len(innerHooks))
			for _, h := range innerHooks {
				hm, ok := h.(map[string]any)
				if !ok {
					filteredInner = append(filteredInner, h)
					continue
				}
				cmd, _ := hm["command"].(string)
				if re.MatchString(cmd) {
					removed++
					continue
				}
				filteredInner = append(filteredInner, h)
			}
			if len(filteredInner) == 0 {
				continue // drop empty matcher block entirely
			}
			entryMap["hooks"] = filteredInner
			kept = append(kept, entryMap)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	settings["hooks"] = hooks
	return removed
}

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle hooks for AI coding clients",
	Long:  "Run hooks that inject memory context at session start, capture tool activity, and survive compaction.",
}

var hooksRunCmd = &cobra.Command{
	Use:   "run [event]",
	Short: "Run a hook event (session-start, post-tool, drain, inscribe, recall)",
	Long: `Run a lifecycle hook event.

Routing (v0.9):
  - Native (Supabase / Postgres direct) is the default for all four
    events. session-start, recall, and inscribe run synchronously
    against the local backend. post-tool classifies + secret-masks
    the event and queues to a SIGKILL-safe directory outbox; the
    queued records ship to the store on next session-start.
  - Gateway is the legacy synchronous path. --gateway forces it for
    any event, useful only for installs that still have a working
    gateway api_key in config.toml.

Draining (#51): session-start no longer ships the queue
inline. It spawns a detached ` + "`hooks run drain`" + ` child and returns
immediately, so startup cost is constant instead of scaling with how
many edits happened since the last session. Control it with:

  --drain async   (default) detached child; startup does not wait
  --drain sync    ship inline before building the session context
  --drain off     skip; some later run ships the queue
  --drain-batch N cap records shipped per drain (0 = 1000)

$OGHAM_DRAIN_MODE sets the default without editing the hook command
in settings.json; an explicit --drain still wins. Run ` + "`hooks run drain`" + `
by hand to flush the queue now, or to see an error a background drain
swallowed -- the detached child's output goes to <cache>/ogham/drain.log.
Only one drainer runs at a time; the rest exit quietly.

DEPRECATED (v0.8, #11): the 'inscribe' event runner stays for users
with pre-v0.8 hook entries in their settings.json, but PreCompact ->
inscribe is no longer wired by ` + "`hooks install`" + ` -- the native
implementation writes a metadata-only stub on every compact event,
which dilutes recall at scale. Use the explicit ` + "`ogham inscribe`" + ` verb
instead and let the caller (orchestrator / skill / scribe / plugin)
decide what to commit.

See issue #6 for the rationale behind the native routing and #11 for
the inscribe verb reshape.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event := args[0]

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Only events that actually parse a hook payload touch stdin --
		// reading one the verb never uses turns into a hang whenever
		// stdin is an open pipe (see eventReadsStdin).
		var input map[string]any
		if eventReadsStdin(event) {
			input = readStdin()
		}

		profile, _ := cmd.Flags().GetString("profile")
		forceGateway, _ := cmd.Flags().GetBool("gateway")

		drainFlag, _ := cmd.Flags().GetString("drain")
		drainBatch, _ := cmd.Flags().GetInt("drain-batch")
		mode, err := resolveDrainMode(drainFlag, cmd.Flags().Changed("drain"), os.Getenv(drainModeEnv))
		if err != nil {
			return err
		}

		// Decide routing: native (Supabase / Postgres direct) wins by
		// default; --gateway flips back to the legacy gateway path.
		nativeCfg, nativeReady := loadNativeIfReady(profile)
		useNative := nativeReady && !forceGateway

		switch event {
		case "session-start":
			if useNative {
				return runNativeSessionStart(ctx, nativeCfg, input, profile, mode, drainBatch)
			}
			return runGatewaySessionStart(ctx, input, profile)

		case "drain":
			// #51: the detached drainer session-start spawns, and the
			// manual "flush the queue now" verb. Native-only -- the
			// outbox exists precisely because the native post-tool path
			// does not write synchronously; --gateway post-tool never
			// queues anything.
			if !useNative {
				return fmt.Errorf(
					"hooks drain: no native database backend configured (set SUPABASE_URL+SUPABASE_KEY or DATABASE_URL in ~/.ogham/config.env); the outbox is only used by the native post-tool path")
			}
			return runNativeDrain(ctx, nativeCfg, profile, drainBatch)

		case "recall":
			if useNative {
				return runNativeRecall(ctx, nativeCfg, input, profile)
			}
			return runGatewayRecall(ctx, input, profile)

		case "inscribe":
			// #51 (aside): nothing warned at runtime that a pre-v0.8
			// `PreCompact -> hooks run inscribe` wiring was still firing,
			// so machines upgraded in place kept writing metadata-only
			// stubs on every compact and kept diluting recall. Say so,
			// once per invocation, on the stream the client shows.
			noticeInscribeDeprecated(os.Stderr, oghamInvocationName())
			if useNative {
				return runNativeInscribe(ctx, nativeCfg, input, profile)
			}
			return runGatewayInscribe(ctx, input, profile)

		case "post-tool":
			// v0.9 (#278): native path is now the default. Classify
			// the event with the embedded shared-data ruleset, secret-
			// mask the content, then queue to the SIGKILL-safe outbox.
			// The next session-start drains the queue into the store.
			// --gateway forces the legacy synchronous path for users
			// with active gateway setups.
			if useNative {
				return runNativePostTool(ctx, nativeCfg, input, profile)
			}
			return runGatewayPostTool(ctx, input, profile)

		default:
			return fmt.Errorf("unknown hook event: %s (use session-start, post-tool, drain, inscribe, or recall)", event)
		}
	},
}

// loadNativeIfReady returns the native config when a working backend
// is configured. Returns (cfg, true) when SessionStart / Recall /
// Inscribe can run locally, (nil, false) otherwise.
//
// Honours --profile by overriding cfg.Profile when the flag is set,
// so per-invocation profile selection works the same way as the
// existing native commands (store, search, list).
func loadNativeIfReady(profile string) (*native.Config, bool) {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return nil, false
	}
	if _, err := cfg.ResolveBackend(); err != nil {
		return nil, false
	}
	if profile != "" {
		cfg.Profile = profile
	}
	return cfg, true
}

// ---- Native event runners -------------------------------------------

func runNativeSessionStart(
	ctx context.Context,
	cfg *native.Config,
	input map[string]any,
	profile string,
	mode drainMode,
	batch int,
) error {
	// Ship the PostToolUse records queued since the last session. In the
	// default async mode this only spawns a detached drainer, so the
	// hook's wall time no longer scales with the backlog (#51). Every
	// outcome here is best-effort: a failed drain logs to stderr and the
	// session-start context is still produced.
	switch mode {
	case drainAsync:
		maybeDrainAsync(ctx, cfg, profile, batch, os.Stderr)
	case drainSync:
		stats, err := drainOutboxLocked(ctx, cfg, profile, batch, drainDeadline)
		reportDrainStats(os.Stderr, stats)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ogham: outbox drain warning: %v\n", err)
		}
	case drainOff:
		// Deliberately nothing.
	}

	cwd := getField(input, "cwd", ".")
	out, err := native.SessionStart(ctx, cfg, cwd, native.HookOptions{Profile: profile})
	if err != nil {
		return err
	}
	if out != "" {
		fmt.Print(out)
	}
	return nil
}

// runNativePostTool is the v0.9 default PostToolUse path. Classifies
// the event against the embedded shared-data ruleset, secret-masks
// any content, builds a minimal memory string, and queues to the
// SIGKILL-safe outbox. The actual store write happens at next
// session-start when the drainer runs. Returns nil on skip-classified
// events (Read, Glob, etc.) so the hook always exits 0.
func runNativePostTool(ctx context.Context, _ *native.Config, input map[string]any, profile string) error {
	toolName := getField(input, "tool_name", "")
	if toolName == "" {
		return nil
	}

	verdict := filters.Classify(toolName)
	if !verdict.ShouldCapture() {
		return nil
	}

	toolInput, _ := input["tool_input"].(map[string]any)
	cwd := getField(input, "cwd", "")
	sessionID := getField(input, "session_id", "")
	outcome := readToolOutcome(input)

	content, target := buildPostToolContent(toolName, toolInput, outcome)
	if content == "" {
		return nil
	}
	content = filters.MaskSecrets(content)

	// Dedup state has to live on disk: this process handles exactly one
	// hook event and then exits (#26 finding 4). A failure to resolve
	// the directory is not fatal -- capture the event rather than drop
	// it.
	if dedupeDir, dirErr := filters.DefaultDedupeDir(); dirErr == nil {
		if filters.NewDeduperAt(dedupeDir, time.Now).IsDuplicate(sessionID, toolName, target) {
			return nil
		}
	}

	tags := []string{"type:action", "tool:" + toolName}
	if outcome.Known && outcome.Failed {
		tags = append(tags, "outcome:error")
	}
	if sessionID != "" {
		tags = append(tags, "session:"+sessionID)
	}

	dir, err := outbox.DefaultDir()
	if err != nil {
		return fmt.Errorf("post-tool: resolve outbox dir: %w", err)
	}
	box, err := outbox.New(dir)
	if err != nil {
		return fmt.Errorf("post-tool: open outbox: %w", err)
	}
	rec := &outbox.Record{
		Content:   content,
		Profile:   profile,
		Source:    "hook:post-tool",
		Tags:      tags,
		SessionID: sessionID,
		ToolName:  toolName,
		Cwd:       cwd,
	}
	if err := box.Write(rec); err != nil {
		return fmt.Errorf("post-tool: queue: %w", err)
	}
	_ = ctx
	return nil
}

// toolOutcome is everything we derive from a tool's response: did it
// fail, and do we know. Deliberately not the response text -- see
// readToolOutcome.
type toolOutcome struct {
	// Known is false when the payload carried no structured outcome
	// field. Callers must not treat that as success or as failure.
	Known bool
	// Failed mirrors the payload's is_error. Meaningless unless Known.
	Failed bool
}

// readToolOutcome derives the outcome of a tool call from the hook
// input, trying the field names Claude Code has used over time.
//
// Two rules, both learned from TBU-231:
//
//  1. Outcome comes from the response, never the request. The Python
//     hook read `tool_input.get("exit_code")`, but a Bash tool_input is
//     {command, description, timeout} -- there is no exit code in it,
//     so its success guard was inert and every command looked like a
//     failure.
//  2. Outcome comes from a structured field or not at all. Python
//     stringified the response and pattern-matched
//     `\b\w*(?:Error|Exception)\b` over it, which matched the
//     envelope's own `is_error` key and classified every success as an
//     error. A string response therefore yields Known=false here --
//     an unknown outcome, not a guessed one.
//
// The response body is read for is_error and then discarded. It is
// never returned, so it cannot reach memory content (#26 finding 1).
func readToolOutcome(input map[string]any) toolOutcome {
	for _, k := range []string{"tool_response", "response", "tool_output", "output"} {
		v, ok := input[k]
		if !ok {
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			// String / scalar response: no structured outcome to read,
			// and we will not infer one from its text.
			continue
		}
		for _, field := range []string{"is_error", "isError"} {
			if b, ok := obj[field].(bool); ok {
				return toolOutcome{Known: true, Failed: b}
			}
		}
	}
	return toolOutcome{}
}

// buildPostToolContent produces a short, human-readable memory string
// + a dedup target for one PostToolUse event.
//
// Content is the command or path plus a derived outcome, and never the
// tool's output. The v0.9 shape appended up to 2000 chars of raw
// response to Bash memories, which is the Go instance of TBU-231's
// "store the command, not the payload" (#26 finding 1). Output is
// high-volume, near-zero-recall, and the most likely place for
// secrets to survive masking.
//
// Richer extraction (diff summarisation, gh-action classification)
// remains unported.
func buildPostToolContent(toolName string, toolInput map[string]any, outcome toolOutcome) (content, target string) {
	switch toolName {
	case "Bash":
		cmd := getField(toolInput, "command", "")
		if cmd == "" {
			return "", ""
		}
		if outcome.Known && outcome.Failed {
			content = "Bash (failed): " + cmd
		} else {
			content = "Bash: " + cmd
		}
		target = cmd
	case "Edit":
		path := getField(toolInput, "file_path", "")
		if path == "" {
			return "", ""
		}
		content = "Edit: " + path
		target = path
	case "Write":
		path := getField(toolInput, "file_path", "")
		if path == "" {
			return "", ""
		}
		content = "Write: " + path
		target = path
	default:
		return "", ""
	}
	return content, target
}

func runNativeRecall(ctx context.Context, cfg *native.Config, input map[string]any, profile string) error {
	cwd := getField(input, "cwd", ".")
	out, err := native.Recall(ctx, cfg, cwd, native.HookOptions{Profile: profile})
	if err != nil {
		return err
	}
	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func runNativeInscribe(ctx context.Context, cfg *native.Config, input map[string]any, profile string) error {
	sessionID := getField(input, "session_id", "unknown")
	cwd := getField(input, "cwd", ".")
	_, err := native.Inscribe(ctx, cfg, sessionID, cwd, native.HookOptions{Profile: profile})
	return err
}

// ---- Gateway event runners (legacy / Pro+ path) ---------------------

// requireGateway builds a gateway client and errors out cleanly if no
// API key is configured. Replaces the silent 401 from issue #6 with a
// surfaced hint. The hint mentions native config as an alternative for
// event types that have a working native path (session-start, recall,
// inscribe). post-tool is gateway-only today -- its smart filtering
// hasn't been ported -- so it gets a different message that doesn't
// dangle a "try native instead" suggestion that wouldn't actually help.
func requireGateway(usage string) (*gateway.Client, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.APIKey == "" {
		if usage == "post-tool" {
			return nil, fmt.Errorf(
				"hooks post-tool: gateway api_key required (run `ogham auth login`). post-tool's smart filtering (classification, duplicate detection, secret masking) is not yet available on the native path; track issue #6 follow-up for the native port",
			)
		}
		return nil, fmt.Errorf(
			"hooks %s: no gateway api_key configured (run `ogham auth login`) and no native database backend configured (set SUPABASE_URL+SUPABASE_KEY or DATABASE_URL in ~/.ogham/config.env to use the native path)",
			usage,
		)
	}
	return gateway.New(cfg.GatewayURL, cfg.APIKey, "ogham-cli/hooks"), nil
}

func runGatewaySessionStart(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("session-start")
	if err != nil {
		return err
	}
	cwd := getField(input, "cwd", ".")
	hookCtx, err := client.HookSessionStart(ctx, cwd, profile)
	if err != nil {
		return err
	}
	if hookCtx != "" {
		fmt.Print(hookCtx)
	}
	return nil
}

func runGatewayRecall(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("recall")
	if err != nil {
		return err
	}
	cwd := getField(input, "cwd", ".")
	hookCtx, err := client.HookRecall(ctx, cwd, profile)
	if err != nil {
		return err
	}
	if hookCtx != "" {
		fmt.Print(hookCtx)
	}
	return nil
}

func runGatewayInscribe(ctx context.Context, input map[string]any, profile string) error {
	client, err := requireGateway("inscribe")
	if err != nil {
		return err
	}
	sessionID := getField(input, "session_id", "unknown")
	cwd := getField(input, "cwd", ".")
	return client.HookInscribe(ctx, sessionID, cwd, profile)
}

func runGatewayPostTool(ctx context.Context, input map[string]any, profile string) error {
	toolName := getField(input, "tool_name", "")
	if toolName == "" {
		return nil // nothing to capture
	}
	// Defense-in-depth: when post-tool fires without a gateway api_key
	// configured, exit 0 with a one-time stderr notice rather than
	// returning a non-zero per-call error. v0.9 makes native the
	// default, so this path is only reached when the user explicitly
	// passed --gateway. The notice now points them at dropping the
	// flag rather than wiring a key.
	cfg, err := config.Load(config.DefaultPath())
	if err != nil || cfg == nil || cfg.APIKey == "" {
		noticePostToolUnconfiguredOnce(postToolNoticeMarkerPath(), os.Stderr)
		return nil
	}
	client := gateway.New(cfg.GatewayURL, cfg.APIKey, "ogham-cli/hooks")
	var toolInput map[string]any
	if ti, ok := input["tool_input"].(map[string]any); ok {
		toolInput = ti
	}
	cwd := getField(input, "cwd", "")
	sessionID := getField(input, "session_id", "")
	return client.HookPostTool(ctx, toolName, toolInput, cwd, sessionID, profile)
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Detect AI client and install hooks configuration",
	Long: `Detect the AI client and install ogham's lifecycle hooks.

Idempotent: re-running replaces this binary's own hook entries rather
than stacking duplicates.

If the Python ogham-mcp package has also installed hooks into the same
settings.json, both sets fire on every event and write to the same
store -- and the Python PostToolUse hook is unscoped, so it captures
every tool call. install reports that but will not delete another
tool's configuration on its own. Pass --replace-python to remove the
Python entries as part of the install.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		replacePython, _ := cmd.Flags().GetBool("replace-python")
		client := detectClient()
		fmt.Printf("Detected client: %s\n", client)

		switch client {
		case "claude-code":
			return installClaudeCodeHooks(replacePython)
		case "kiro":
			printKiroInstructions()
		default:
			fmt.Printf("%s doesn't support hooks natively.\n", client)
			fmt.Println("Use CLAUDE.md instructions or the Python CLI (ogham hooks install).")
		}
		return nil
	},
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show installed hooks, the outbox queue depth, and any stale wiring",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := detectClient()
		fmt.Printf("Client: %s\n", client)

		if client == "claude-code" {
			settings, err := readClaudeSettings()
			if err != nil {
				fmt.Println("No hooks installed (settings.json not found)")
				printOutboxStatus(os.Stdout)
				return nil
			}
			hooks, ok := settings["hooks"].(map[string]any)
			if !ok || len(hooks) == 0 {
				fmt.Println("No hooks installed")
				printOutboxStatus(os.Stdout)
				return nil
			}
			fmt.Println("Installed hooks:")
			events := make([]string, 0, len(hooks))
			for event := range hooks {
				events = append(events, event)
			}
			sort.Strings(events)
			for _, event := range events {
				fmt.Printf("  %s\n", event)
			}

			printOutboxStatus(os.Stdout)

			// #51 (aside): a stale PreCompact -> inscribe entry keeps
			// firing silently on machines installed before v0.8.
			home, _ := os.UserHomeDir()
			if msg := formatDeprecatedHookWarning(
				detectDeprecatedHooks(settings),
				home+"/.claude/settings.json",
				oghamInvocationName()); msg != "" {
				fmt.Fprint(os.Stderr, msg)
			}
		}
		return nil
	},
}

// printOutboxStatus reports the queue depth and whether a drainer is
// currently running. A backlog that never shrinks is the visible symptom
// of a drain that is failing in the background, so status is where a user
// should be able to see it (#51).
func printOutboxStatus(w io.Writer) {
	box, err := openOutbox()
	if err != nil {
		fmt.Fprintf(w, "Outbox: unavailable (%v)\n", err)
		return
	}
	if box == nil {
		fmt.Fprintln(w, "Outbox: empty (no queue directory yet)")
		return
	}
	pending, err := box.Pending()
	if err != nil {
		fmt.Fprintf(w, "Outbox: unavailable (%v)\n", err)
		return
	}
	state := ""
	if box.LockHeld() {
		state = ", drain in progress"
	}
	fmt.Fprintf(w, "Outbox: %d queued%s (%s)\n", pending, state, box.Dir)
	if pending > 0 {
		fmt.Fprintln(w, "  Ships on next session start; flush now with `ogham hooks run drain`.")
	}
}

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ogham hook entries from the client's settings",
	Long: `Strip Go-owned ogham hook entries from Claude Code's
~/.claude/settings.json. Leaves Python ogham-mcp hook entries and any
unrelated hooks alone -- detection is by command verb shape (ogham
hooks run <verb> = Go; ogham hooks <verb> = Python).

Remediation path for users stuck with the broken ` + "`ogham-cli hooks run`" + `
commands written by pre-v0.7.4 installs of this tool (#7). After
running uninstall, re-run ` + "`ogham hooks install`" + ` to land the fixed
config.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := detectClient()
		switch client {
		case "claude-code":
			return uninstallClaudeCodeHooks()
		default:
			fmt.Printf("Uninstall is currently only implemented for Claude Code (detected: %s).\n", client)
			fmt.Println("For Kiro / Cursor / generic clients, remove the entries manually from the host's config.")
			return nil
		}
	},
}

func init() {
	hooksRunCmd.Flags().String("profile", "work", "Memory profile")
	hooksRunCmd.Flags().Bool("gateway", false, "Force gateway path even when native backend is configured")
	hooksRunCmd.Flags().String("drain", string(drainAsync),
		"How session-start ships the queued outbox: async (detached child), sync (inline), off")
	hooksRunCmd.Flags().Int("drain-batch", 0,
		"Max records to ship per drain (0 = package default of 1000)")
	hooksCmd.AddCommand(hooksRunCmd)
	hooksInstallCmd.Flags().Bool("replace-python", false,
		"also remove ogham-mcp (Python) hook entries from settings.json")
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksStatusCmd)
	rootCmd.AddCommand(hooksCmd)
}

// eventReadsStdin reports whether a hook event consumes the JSON payload
// the client writes to stdin.
//
// Every event does except `drain`, which takes all it needs from flags
// and the queue on disk. That distinction is not cosmetic: readStdin
// short-circuits on a character device, so an interactive terminal is
// fine, but against an open pipe with no data it blocks in io.ReadAll
// until the writer closes. `drain` is the one verb meant to be run from
// a script, a cron entry or a CI step -- precisely where stdin is a pipe
// somebody else owns -- and `hooks status` tells users to run it by
// hand. Found smoke-testing v0.13.4: 8.0 s of doing nothing against a
// pipe held open for 8 s.
//
// The default is to READ, so a new verb opts out deliberately. A
// needless read costs a hang in a script; a missed read costs the whole
// payload, silently, with the hook still exiting 0.
func eventReadsStdin(event string) bool {
	return event != "drain"
}

// readStdin reads JSON from stdin if available.
func readStdin() map[string]any {
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return nil // interactive terminal, no piped input
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// getField extracts a string field from the input map.
func getField(input map[string]any, key, fallback string) string {
	if input == nil {
		return fallback
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return fallback
}

// detectClient checks which AI coding client is installed.
func detectClient() string {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(home + "/.claude/settings.json"); err == nil {
		return "claude-code"
	}
	if _, err := os.Stat(home + "/.kiro"); err == nil {
		return "kiro"
	}
	if _, err := os.Stat(home + "/.cursor"); err == nil {
		return "cursor"
	}
	return "generic"
}

// installClaudeCodeHooks writes ogham hook entries to Claude Code's global
// settings.json (~/.claude/settings.json). Idempotent by construction:
// pruneOghamGoHooks strips any Go-owned ogham hook entries first, so
// re-running `hooks install` after a broken-binary-name install (#7) ends
// up with a clean, correct config -- no stale `ogham-cli hooks run ...`
// lines left behind alongside the fresh ones.
//
// Hook commands embed the absolute path of the running binary
// (oghamBinaryPath), so they execute correctly regardless of binary name
// or $PATH state -- the load-bearing fix for #7 findings #1 and #2.
func installClaudeCodeHooks(replacePython bool) error {
	settings, _ := readClaudeSettings()
	if settings == nil {
		settings = make(map[string]any)
	}

	// Pre-pass: remove any Go-owned ogham hook entries before we add the
	// fresh ones. Leaves Python `ogham hooks <verb>` entries alone.
	removed := pruneOghamGoHooks(settings)

	// #30: a Python ogham-mcp install in the same settings.json means two
	// hooks fire per event into one store, and the Python PostToolUse is
	// unscoped. Detect before we write, so the notice can name what was
	// found; only remove on explicit opt-in.
	pythonFound := detectPythonHooks(settings)
	pythonRemoved := 0
	if replacePython && len(pythonFound) > 0 {
		pythonRemoved = prunePythonHooks(settings)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
	}

	// v0.9 (#278): PostToolUse runs natively (Classify -> MaskSecrets ->
	// outbox). No gateway api_key required. The v0.8 conditional-skip
	// path was retired -- every install gets the full hook set.
	binPath := oghamBinaryPath()
	oghamHooks := buildOghamHookSet("", binPath)

	for event, hookEntry := range oghamHooks {
		existing, _ := hooks[event].([]any)
		existing = append(existing, hookEntry)
		hooks[event] = existing
	}

	settings["hooks"] = hooks

	home, _ := os.UserHomeDir()
	path := home + "/.claude/settings.json"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	fmt.Printf("Claude Code hooks installed to %s\n", path)
	fmt.Printf("  Binary: %s\n", binPath)
	fmt.Printf("  Events: SessionStart, PostToolUse (matcher: %s), PostCompact (recall)\n", defaultPostToolMatcher)
	fmt.Println("  PostToolUse runs natively against your local backend -- no gateway api_key required.")
	if removed > 0 {
		fmt.Printf("  Cleaned %d stale ogham hook entr%s from previous install.\n",
			removed, plural(removed, "y", "ies"))
	}
	if pythonRemoved > 0 {
		fmt.Printf("  Removed %d ogham-mcp (Python) hook entr%s (--replace-python).\n",
			pythonRemoved, plural(pythonRemoved, "y", "ies"))
	} else if msg := formatPythonHookWarning(pythonFound, path); msg != "" {
		fmt.Fprint(os.Stderr, msg)
	}
	return nil
}

// uninstallClaudeCodeHooks strips Go-owned ogham hook entries from Claude
// Code's settings.json. Leaves Python ogham-mcp entries and unrelated
// hooks untouched. The remediation path for users stuck with the broken
// `ogham-cli hooks run ...` commands from pre-v0.7.4 installs (#7).
func uninstallClaudeCodeHooks() error {
	settings, err := readClaudeSettings()
	if err != nil || settings == nil {
		fmt.Println("No Claude Code settings.json found -- nothing to uninstall.")
		return nil
	}

	removed := pruneOghamGoHooks(settings)

	// #30: leaving without a word would imply hooks are gone. If a Python
	// install is still wired up, say so -- otherwise the user concludes
	// they have uninstalled ogham and keeps getting captures.
	pythonLeft := detectPythonHooks(settings)

	if removed == 0 {
		fmt.Println("No Go ogham hook entries found in settings.json -- nothing to remove.")
		if len(pythonLeft) > 0 {
			fmt.Fprintf(os.Stderr,
				"\nogham: %d ogham-mcp (Python) hook entr%s remain and are still active:\n",
				len(pythonLeft), plural(len(pythonLeft), "y", "ies"))
			for _, f := range pythonLeft {
				fmt.Fprintf(os.Stderr, "    %-14s %s\n", f.Event, f.Command)
			}
			fmt.Fprintln(os.Stderr, "  Remove them by hand to stop capturing entirely.")
		}
		return nil
	}

	home, _ := os.UserHomeDir()
	path := home + "/.claude/settings.json"
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	fmt.Printf("Removed %d ogham hook entr%s from %s\n",
		removed, plural(removed, "y", "ies"), path)
	return nil
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// readClaudeSettings reads ~/.claude/settings.json.
func readClaudeSettings() (map[string]any, error) {
	home, _ := os.UserHomeDir()
	// #nosec G304 -- fixed filename under os.UserHomeDir(); no caller input.
	data, err := os.ReadFile(home + "/.claude/settings.json")
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// printKiroInstructions outputs Kiro Hook UI setup steps. Uses the
// resolved absolute binary path so the printed instructions stay correct
// regardless of binary name or $PATH state (#7).
func printKiroInstructions() {
	sessionStartCmd := oghamHookCommand("session-start")
	postToolCmd := oghamHookCommand("post-tool")
	fmt.Println("\nKiro hooks -- manual setup via Hook UI:")
	fmt.Println("")
	fmt.Println("  1. Open Command Palette (Cmd+Shift+P / Ctrl+Shift+P)")
	fmt.Println("  2. Type 'Kiro: Open Kiro Hook UI'")
	fmt.Println("  3. Create these hooks:")
	fmt.Println("")
	fmt.Println("  Hook 1: Session Start")
	fmt.Println("    Event: User prompt submit")
	fmt.Println("    Action: Run Command")
	fmt.Printf("    Command: %s\n", sessionStartCmd)
	fmt.Println("")
	fmt.Println("  Hook 2: Post Tool")
	fmt.Println("    Event: Post tool invocation")
	fmt.Println("    Action: Run Command")
	fmt.Printf("    Command: %s\n", postToolCmd)
}
