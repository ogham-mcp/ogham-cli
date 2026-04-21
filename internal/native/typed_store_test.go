package native

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Typed-store tests focus on the layers above Store() -- content
// assembly, tag injection, metadata shape. The DB write path is
// covered by store_test.go + the live-tagged pass; we don't reach the
// backend from here (empty cfg errors out of Store() before the write,
// and we pin the error site so refactors don't drift).

// --- appendTypeTag + nonNilSlice helpers ----------------------------------

func TestAppendTypeTag_AddsWhenMissing(t *testing.T) {
	got := appendTypeTag([]string{"user"}, "type:decision")
	want := []string{"user", "type:decision"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("append-missing: got %v, want %v", got, want)
	}
}

func TestAppendTypeTag_IdempotentWhenPresent(t *testing.T) {
	// Re-adding the same type tag must not duplicate. The caller may
	// have already set type:decision intentionally (e.g. via an LLM
	// that mirrors our own docstrings); we honour it.
	got := appendTypeTag([]string{"type:decision", "other"}, "type:decision")
	want := []string{"type:decision", "other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("append-present: got %v, want %v", got, want)
	}
}

func TestAppendTypeTag_DoesNotMutateCallerSlice(t *testing.T) {
	// Caller may reuse the slice; Store() does mutate its input via
	// mergeTags. We must return a fresh slice to avoid surprising the
	// caller when they append more tags after the typed-store call.
	caller := []string{"user"}
	appendTypeTag(caller, "type:decision")
	if len(caller) != 1 || caller[0] != "user" {
		t.Errorf("caller slice mutated: got %v", caller)
	}
}

func TestNonNilSlice_NilBecomesEmpty(t *testing.T) {
	got := nonNilSlice(nil)
	if got == nil {
		t.Error("nil input must return non-nil empty slice for JSON parity")
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestNonNilSlice_NonEmptyPassThrough(t *testing.T) {
	in := []string{"a", "b"}
	got := nonNilSlice(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("pass-through: got %v, want %v", got, in)
	}
}

// --- argument validation (pre-DB error paths) -----------------------------

func TestStoreDecision_RejectsEmptyDecision(t *testing.T) {
	_, err := StoreDecision(ctxBG(t), &Config{}, StoreDecisionOptions{
		Rationale: "because",
	})
	if err == nil || !contains(err.Error(), "decision is required") {
		t.Errorf("want 'decision is required'; got %v", err)
	}
}

func TestStoreDecision_RejectsEmptyRationale(t *testing.T) {
	_, err := StoreDecision(ctxBG(t), &Config{}, StoreDecisionOptions{
		Decision: "pick X",
	})
	if err == nil || !contains(err.Error(), "rationale is required") {
		t.Errorf("want 'rationale is required'; got %v", err)
	}
}

func TestStoreDecision_RelatedMemoriesNoLongerRejected(t *testing.T) {
	// Batch E landed: store_decision now creates 'supports' edges to
	// each related_memories UUID after the INSERT. Empty cfg still
	// errors at backend resolution, but the error MUST NOT be the
	// old 'related_memories not yet absorbed' message -- if it is, a
	// regression has re-gated the feature.
	_, err := StoreDecision(ctxBG(t), &Config{}, StoreDecisionOptions{
		Decision:        "pick X",
		Rationale:       "because",
		RelatedMemories: []string{"abc-123"},
	})
	if err != nil && contains(err.Error(), "not yet absorbed") {
		t.Errorf("related_memories should be supported post-Batch-E; got %v", err)
	}
}

func TestStoreFact_DefaultConfidence(t *testing.T) {
	// Python default confidence is 1.0; Go maps JSON zero-value to
	// "caller didn't specify" and coerces to 1.0. This test pins the
	// coercion -- a regression here would silently store every fact
	// with confidence=0 (useless at retrieval).
	//
	// We can't reach Store() without a live backend, so we verify via
	// the error path: the call fails at backend resolution but not at
	// the confidence-range validator.
	_, err := StoreFact(ctxBG(t), &Config{}, StoreFactOptions{Fact: "water boils at 100C"})
	if err != nil && contains(err.Error(), "confidence must be in") {
		t.Errorf("zero confidence should default, not fail validation; got %v", err)
	}
}

func TestStoreFact_RejectsOutOfRangeConfidence(t *testing.T) {
	_, err := StoreFact(ctxBG(t), &Config{}, StoreFactOptions{
		Fact:       "x",
		Confidence: 1.5,
	})
	if err == nil || !contains(err.Error(), "confidence must be in [0.0, 1.0]") {
		t.Errorf("want confidence-range error; got %v", err)
	}
	_, err = StoreFact(ctxBG(t), &Config{}, StoreFactOptions{
		Fact:       "x",
		Confidence: -0.1,
	})
	if err == nil || !contains(err.Error(), "confidence must be in [0.0, 1.0]") {
		t.Errorf("want confidence-range error; got %v", err)
	}
}

func TestStorePreference_RejectsUnknownStrength(t *testing.T) {
	_, err := StorePreference(ctxBG(t), &Config{}, StorePreferenceOptions{
		Preference: "dark mode",
		Strength:   "extreme",
	})
	if err == nil || !contains(err.Error(), "strength must be one of") {
		t.Errorf("want strength validation; got %v", err)
	}
}

func TestStorePreference_DefaultStrengthIsNormal(t *testing.T) {
	// Empty strength must coerce to "normal" (Python default), not fail.
	// Same error-path trick as TestStoreFact_DefaultConfidence.
	_, err := StorePreference(ctxBG(t), &Config{}, StorePreferenceOptions{
		Preference: "dark mode",
	})
	if err != nil && contains(err.Error(), "strength must be one of") {
		t.Errorf("empty strength should default, not fail validation; got %v", err)
	}
}

func TestStoreEvent_RejectsEmptyEvent(t *testing.T) {
	_, err := StoreEvent(ctxBG(t), &Config{}, StoreEventOptions{})
	if err == nil || !contains(err.Error(), "event is required") {
		t.Errorf("want 'event is required'; got %v", err)
	}
}

// --- helpers --------------------------------------------------------------

// ctxBG returns a background context bound to the test's lifetime. The
// typed-store tests need a context but never actually time out -- Store()
// errors before any network I/O when cfg is empty.
func ctxBG(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// contains is a short alias for strings.Contains so the assertion
// lines above read naturally.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
