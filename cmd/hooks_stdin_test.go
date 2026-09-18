package cmd

import "testing"

// TestEventReadsStdinExcludesDrain pins the fix for the v0.13.4 smoke-test
// finding: `hooks run drain` blocked on a stdin it never reads.
//
// `readStdin` short-circuits on a character device, so an interactive
// terminal was never affected and neither was the detached drainer (its
// stdin is /dev/null). What hung was the case the verb exists for: a
// script, a cron entry or a CI step, where stdin is an open pipe that
// nobody is going to write to. Measured before the fix: 8.0 s of doing
// nothing against a pipe held open for 8 s.
func TestEventReadsStdinExcludesDrain(t *testing.T) {
	if eventReadsStdin("drain") {
		t.Error("drain consumes no hook payload; reading stdin can only block it")
	}
}

// TestEventReadsStdinIncludesEveryPayloadEvent is the other half, and the
// more important one: every event that DOES parse a hook payload must keep
// reading stdin. Widening the exclusion by accident would silently strip
// tool_name / cwd / session_id from the payload, and the hook would still
// exit 0 while capturing nothing.
func TestEventReadsStdinIncludesEveryPayloadEvent(t *testing.T) {
	for _, event := range []string{"session-start", "post-tool", "recall", "inscribe"} {
		if !eventReadsStdin(event) {
			t.Errorf("%q parses a hook payload from stdin and must keep reading it", event)
		}
	}
}

// TestEventReadsStdinDefaultsToReading keeps the default safe: an unknown
// or newly added verb reads stdin until someone deliberately opts it out,
// because the cost of a needless read is a hang in a script, while the cost
// of a missed read is silent data loss.
func TestEventReadsStdinDefaultsToReading(t *testing.T) {
	if !eventReadsStdin("some-future-verb") {
		t.Error("unknown events must default to reading stdin")
	}
}
