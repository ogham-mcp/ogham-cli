package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// settingsWithCommands builds the nested Claude Code hooks shape from a
// flat event -> command map, so these tests describe the case rather
// than the JSON.
func settingsWithCommands(byEvent map[string]string) map[string]any {
	hooks := map[string]any{}
	for event, command := range byEvent {
		hooks[event] = []any{
			map[string]any{
				"matcher": "",
				"hooks": []any{
					map[string]any{"type": "command", "command": command},
				},
			},
		}
	}
	return map[string]any{"hooks": hooks}
}

func TestDetectDeprecatedHooksFindsStaleInscribeWiring(t *testing.T) {
	// The exact shape a pre-v0.8 `hooks install` left behind, and which
	// later installs never removed because they only replace what they
	// write (#51 aside).
	settings := settingsWithCommands(map[string]string{
		"PreCompact":   "/usr/local/bin/ogham hooks run inscribe",
		"SessionStart": "/usr/local/bin/ogham hooks run session-start",
		"PostToolUse":  "/usr/local/bin/ogham hooks run post-tool",
	})

	found := detectDeprecatedHooks(settings)
	if len(found) != 1 {
		t.Fatalf("found %d deprecated entries, want 1: %+v", len(found), found)
	}
	if found[0].Event != "PreCompact" {
		t.Errorf("Event = %q, want PreCompact", found[0].Event)
	}
	if !strings.Contains(found[0].Why, "#11") {
		t.Errorf("Why should cite the deprecating issue, got: %q", found[0].Why)
	}
}

func TestDetectDeprecatedHooksIgnoresCurrentAndPythonWiring(t *testing.T) {
	// The Python package's two-token `ogham hooks inscribe` is another
	// tool's config. We report Python coexistence elsewhere; this
	// detector must not claim it as our own stale entry, because the
	// remedy it prints (`ogham hooks uninstall`) would not touch it.
	settings := settingsWithCommands(map[string]string{
		"SessionStart": "/usr/local/bin/ogham hooks run session-start",
		"PostCompact":  "/usr/local/bin/ogham hooks run recall",
		"PreCompact":   "/home/u/.venv/bin/ogham hooks inscribe",
		"Stop":         "/usr/bin/some-other-tool hooks run inscribe",
	})

	if found := detectDeprecatedHooks(settings); len(found) != 0 {
		t.Errorf("found %+v, want nothing", found)
	}
}

func TestDetectDeprecatedHooksHandlesNoHooksKey(t *testing.T) {
	if found := detectDeprecatedHooks(map[string]any{}); len(found) != 0 {
		t.Errorf("found %+v for settings with no hooks key, want nothing", found)
	}
}

func TestFormatDeprecatedHookWarningNamesEventCommandAndRemedy(t *testing.T) {
	msg := formatDeprecatedHookWarning([]deprecatedHookEntry{{
		Event:   "PreCompact",
		Command: "/usr/local/bin/ogham hooks run inscribe",
		Why:     "inscribe was deprecated in v0.8 (#11)",
	}}, "/home/u/.claude/settings.json", "omcli")

	for _, want := range []string{
		"PreCompact",
		"/usr/local/bin/ogham hooks run inscribe",
		"/home/u/.claude/settings.json",
		"omcli hooks uninstall && omcli hooks install",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning missing %q:\n%s", want, msg)
		}
	}
}

func TestFormatDeprecatedHookWarningEmptyWhenClean(t *testing.T) {
	if msg := formatDeprecatedHookWarning(nil, "/home/u/.claude/settings.json", "ogham"); msg != "" {
		t.Errorf("warning = %q, want empty for a clean settings file", msg)
	}
}

func TestNoticeInscribeDeprecatedIsOneActionableLine(t *testing.T) {
	var b bytes.Buffer
	noticeInscribeDeprecated(&b, "omcli")
	got := b.String()

	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
		t.Errorf("notice spans %d extra lines; it fires on every compact, keep it to one:\n%s", n+1, got)
	}
	// The remedy must name the binary the user actually has installed,
	// not the project name -- they differ on the machine that filed #51.
	for _, want := range []string{"deprecated", "#11", "omcli hooks install", "omcli inscribe"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q: %s", want, got)
		}
	}
}

func TestNoticeInscribeDeprecatedRepeatsEveryCall(t *testing.T) {
	// Deliberately NOT marker-gated like the post-tool notice: the
	// wiring being warned about fires repeatedly, and a once-per-machine
	// notice would scroll past while the stubs kept accumulating.
	var first, second bytes.Buffer
	noticeInscribeDeprecated(&first, "ogham")
	noticeInscribeDeprecated(&second, "ogham")
	if second.Len() == 0 || first.String() != second.String() {
		t.Errorf("second call = %q, want the same notice as the first (%q)", second.String(), first.String())
	}
}

func TestPrintOutboxStatusReportsDepthAndDrainState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	box := mustOutbox(t, dir)
	for i := 0; i < 2; i++ {
		writeOutboxRecord(t, box)
	}

	var b bytes.Buffer
	printOutboxStatus(&b)
	got := b.String()
	if !strings.Contains(got, "Outbox: 2 queued") {
		t.Errorf("status = %q, want the queue depth", got)
	}
	if strings.Contains(got, "drain in progress") {
		t.Errorf("status = %q, no drainer is running", got)
	}
	if !strings.Contains(got, "hooks run drain") {
		t.Errorf("status = %q, want the manual-flush hint when records are queued", got)
	}
}

func TestPrintOutboxStatusFlagsARunningDrain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OGHAM_OUTBOX_DIR", dir)
	box := mustOutbox(t, dir)
	writeOutboxRecord(t, box)
	lock, err := box.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	var b bytes.Buffer
	printOutboxStatus(&b)
	if !strings.Contains(b.String(), "drain in progress") {
		t.Errorf("status = %q, want the in-progress marker", b.String())
	}
}

func TestPrintOutboxStatusOnQueueThatWasNeverCreated(t *testing.T) {
	t.Setenv("OGHAM_OUTBOX_DIR", t.TempDir()+"/never")
	var b bytes.Buffer
	printOutboxStatus(&b)
	if !strings.Contains(b.String(), "empty") {
		t.Errorf("status = %q, want an 'empty' report rather than an error", b.String())
	}
}
