package extraction

import (
	"strings"
	"testing"
)

// The person: entity class was removed. It was the only one of the four
// with no unambiguous syntactic marker -- entity: has CamelCase, file:
// has path separators, error: has the Error/Exception suffix, and a
// person name has nothing but Title Case, which it shares with every
// product noun phrase ever written.
//
// Two days of rules were spent trying to separate them and the measured
// outcome was that no shape-only rule can: on hook-captured technical
// prose the classifier produced 4,140 person entities with zero people
// in a random sample of 30, while the rule strict enough to stop that
// dropped real names in subject position on hand-written prose. The two
// genres have opposite base rates, so any single threshold is wrong on
// one of them.
//
// On the parity corpus person: was also the ONLY prefix on which Go and
// Python disagreed at all -- entity:, file: and error: matched 97/97.
//
// These tests pin the removal so it cannot drift back in.

func TestNoPersonTagsAreEverEmitted(t *testing.T) {
	cases := []string{
		// Genuine names, previously tagged. Deliberately included: the
		// point is that these are gone too, not that junk is gone.
		"Change authored by Kevin Burns for the release.",
		"Emma Carter tested the migration.",
		"We met Hiroshi Tanaka yesterday at the conference.",
		"Kevin Burns, Owen Fletcher and Luis Ramirez agreed.",
		"Written by Ada Lovelace in 1843.",
		// The junk that motivated the removal.
		"Grant EXECUTE to SECURITY DEFINER functions.",
		"We store state in Durable Objects for the coordinator.",
		"The Knowledge Graph layer sits above the vector index.",
		"Attention Patterns explain the retrieval behaviour.",
	}
	for _, c := range cases {
		for _, e := range Entities(c) {
			if strings.HasPrefix(e, "person:") {
				t.Errorf("person tag %q emitted for %q", e, c)
			}
		}
	}
}

// TestOtherEntityClassesSurvive is the control: removing person: must
// not disturb the three classes that have real syntactic markers.
func TestOtherEntityClassesSurvive(t *testing.T) {
	cases := []struct{ content, want string }{
		{"The PostgresBackend handles it.", "entity:PostgresBackend"},
		{"Edited /repo/internal/native/store.go today.", "file:/repo/internal/native/store.go"},
		{"A ValueError was raised during the run.", "error:ValueError"},
	}
	for _, tc := range cases {
		found := false
		for _, e := range Entities(tc.content) {
			if e == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("lost %q from %q -- got %v", tc.want, tc.content, Entities(tc.content))
		}
	}
}
