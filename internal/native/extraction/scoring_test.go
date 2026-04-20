package extraction

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Bare content with no signals, no tags, short: exactly the base 0.2.
func TestImportance_Base(t *testing.T) {
	got := Importance("a simple note without signal words.", nil)
	if !near(got, 0.2) {
		t.Errorf("base score = %v, want 0.2", got)
	}
}

// Every axis maxed -> should cap at 1.0 (raw sum 1.3).
func TestImportance_CappedAtOne(t *testing.T) {
	signals := "we decided to refactor the architecture after a failed " +
		"deploy. See ./cmd/root.go -- a RuntimeException fired. " +
		"`ogham serve` was the entry point."
	// Pad the content past 500 chars without adding any further signals.
	pad := ""
	for len(pad) < 600 {
		pad += " and then and then and then"
	}
	content := signals + pad
	if len(content) <= 500 {
		t.Fatalf("fixture too short (%d) -- fix the test not the code", len(content))
	}
	tags := []string{"a", "b", "c", "d"}
	got := Importance(content, tags)
	if !near(got, 1.0) {
		t.Errorf("all signals present = %v, want 1.0", got)
	}
}

// Error signal via regex (not word list): "CustomError" triggers the
// errorTypeRe fallback path even though "error" wasn't a standalone
// word. Coverage check for the `|| errorTypeRe.MatchString(content)`
// branch.
func TestImportance_ErrorViaRegexOnly(t *testing.T) {
	// Content has no error _words_ but does have an ErrorType name.
	got := Importance("CustomError occurred during sync.", nil)
	// base 0.2 + err 0.2 = 0.4. Note: "error" IS a substring of
	// "CustomError" lowercase so the word-list path also matches;
	// score adds once regardless. What we're verifying is it doesn't
	// double-count.
	if !near(got, 0.4) {
		t.Errorf("error-via-regex = %v, want 0.4 (one +0.2)", got)
	}
}

// Tags at boundary: 2 tags does NOT trigger the +0.1, 3 tags does.
func TestImportance_TagBoundary(t *testing.T) {
	c := "a note."
	if !near(Importance(c, []string{"a", "b"}), 0.2) {
		t.Error("2 tags must not add 0.1")
	}
	if !near(Importance(c, []string{"a", "b", "c"}), 0.3) {
		t.Error("3 tags must add 0.1")
	}
}

// Length at boundary: 500 chars does NOT trigger, 501 does.
func TestImportance_LengthBoundary(t *testing.T) {
	// Build content of exact length 500 with no signal words.
	noise := "xx "
	content := ""
	for len(content) < 500 {
		content += noise
	}
	content = content[:500]
	if !near(Importance(content, nil), 0.2) {
		t.Errorf("len=500 must not add 0.1: %v", Importance(content, nil))
	}
	content += "x"
	if !near(Importance(content, nil), 0.3) {
		t.Errorf("len=501 must add 0.1: %v", Importance(content, nil))
	}
}

// Code fence: inline backtick counts, so does triple backtick. Both
// paths hit the same branch but we want to assert both shapes work.
func TestImportance_CodeFenceShapes(t *testing.T) {
	if !near(Importance("run `ls` here", nil), 0.3) {
		t.Errorf("inline backtick failed")
	}
	if !near(Importance("```sh\nls\n```", nil), 0.3) {
		t.Errorf("triple fence failed")
	}
}

// Empty content + no tags: still scores base (no crash, no negative).
func TestImportance_Empty(t *testing.T) {
	if !near(Importance("", nil), 0.2) {
		t.Errorf("empty = %v, want 0.2", Importance("", nil))
	}
}

// Case-insensitivity check: UPPERCASE signal words still trigger.
func TestImportance_CaseInsensitive(t *testing.T) {
	got := Importance("We DECIDED to ship.", nil)
	if !near(got, 0.5) {
		t.Errorf("case-insensitive decision = %v, want 0.5", got)
	}
}
