package native

import (
	"context"
	"strings"
	"testing"
)

// bogusPostgresConfig is a Config that resolves to the postgres backend
// but whose URL is not a real database -- enough to exercise every
// pre-network validation path in StoreTriple/QueryJoin without a live DB.
// Mirrors the pattern in wiki_walk_knowledge_test.go.
func bogusPostgresConfig() *Config {
	return &Config{Database: Database{Backend: "postgres", URL: "postgres://x"}}
}

func supabaseConfig() *Config {
	return &Config{Database: Database{Backend: "supabase", SupabaseURL: "https://x", SupabaseKey: "k"}}
}

// -----------------------------------------------------------------------
// V1Predicates

func TestV1Predicates_ShapeAndVocab(t *testing.T) {
	if len(V1Predicates) != 16 {
		t.Errorf("V1Predicates has %d entries, want 16", len(V1Predicates))
	}
	if _, ok := V1Predicates["SUPERSEDES"]; ok {
		t.Error("SUPERSEDES must not be in the v1 vocabulary (write-time supersession via valid_to covers it)")
	}
	for _, want := range []string{
		"DEPENDS_ON", "DEPENDED_ON_BY", "OWNS", "OWNED_BY", "ASSIGNED_TO",
		"HAS_ASSIGNEE", "DECIDED", "MENTIONS", "BLOCKS", "BLOCKED_BY",
		"PART_OF", "CONTAINS", "SUPPORTS", "CONTRADICTS", "EVOLVED_INTO", "RELATED_TO",
	} {
		if _, ok := V1Predicates[want]; !ok {
			t.Errorf("V1Predicates missing %q", want)
		}
	}
}

// -----------------------------------------------------------------------
// StoreTriple

func TestStoreTriple_NilCfg(t *testing.T) {
	_, err := StoreTriple(context.Background(), nil, StoreTripleOptions{
		Subject: "a", Predicate: "DEPENDS_ON", Object: "b",
	})
	if err == nil {
		t.Error("expected error on nil config")
	}
}

func TestStoreTriple_EmptySubject(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "", Predicate: "DEPENDS_ON", Object: "b",
	})
	if err == nil {
		t.Error("expected error on empty subject")
	}
}

func TestStoreTriple_EmptyObject(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "a", Predicate: "DEPENDS_ON", Object: "",
	})
	if err == nil {
		t.Error("expected error on empty object")
	}
}

func TestStoreTriple_PredicateNotInVocab(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "a", Predicate: "SUPERSEDES", Object: "b",
	})
	if err == nil {
		t.Fatal("expected error for out-of-vocab predicate")
	}
	if !strings.Contains(err.Error(), "SUPERSEDES") {
		t.Errorf("error should name the offending predicate, got: %v", err)
	}
}

func TestStoreTriple_UnknownBackend(t *testing.T) {
	_, err := StoreTriple(context.Background(), supabaseConfig(), StoreTripleOptions{
		Subject: "a", Predicate: "DEPENDS_ON", Object: "b",
	})
	if err == nil {
		t.Fatal("expected error for non-postgres backend")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should mention the postgres requirement, got: %v", err)
	}
}

func TestStoreTriple_BadFactIDUUID(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "a", Predicate: "DEPENDS_ON", Object: "b",
		SourceMemoryID: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error for malformed source_memory_id")
	}
}

func TestStoreTriple_ValidInputsReachNetwork(t *testing.T) {
	// Everything pre-network passes (vocab, backend, subject/object,
	// fact-id) -- the call should fail only once it tries to reach the
	// bogus postgres URL, not from a validation error.
	cfg := bogusPostgresConfig()
	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "a", Predicate: "DEPENDS_ON", Object: "b",
	})
	if err == nil {
		t.Fatal("expected a connection error against the bogus URL")
	}
	if strings.Contains(err.Error(), "vocabulary") || strings.Contains(err.Error(), "required") {
		t.Errorf("expected a network-stage error, got a validation error: %v", err)
	}
}

// -----------------------------------------------------------------------
// QueryJoin

func TestQueryJoin_NilCfg(t *testing.T) {
	_, err := QueryJoin(context.Background(), nil, QueryJoinOptions{StartEntity: "a", HopLimit: 1})
	if err == nil {
		t.Error("expected error on nil config")
	}
}

func TestQueryJoin_EmptyStartEntity(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{StartEntity: "", HopLimit: 1})
	if err == nil {
		t.Error("expected error on empty start_entity")
	}
}

func TestQueryJoin_HopLimitRequired(t *testing.T) {
	cfg := bogusPostgresConfig()
	for _, hl := range []int{0, -1, -5} {
		_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{StartEntity: "a", HopLimit: hl})
		if err == nil {
			t.Errorf("expected error for hop_limit=%d", hl)
		}
	}
}

func TestQueryJoin_HopLimitLessThanPathLength(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: "a", PredicatePath: []string{"DEPENDS_ON", "OWNS"}, HopLimit: 1,
	})
	if err == nil {
		t.Fatal("expected error when hop_limit < len(predicate_path)")
	}
}

func TestQueryJoin_PredicateNotInVocab(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: "a", PredicatePath: []string{"DEPENDS_ON", "SUPERSEDES"}, HopLimit: 2,
	})
	if err == nil {
		t.Fatal("expected error for out-of-vocab predicate in path")
	}
	if !strings.Contains(err.Error(), "SUPERSEDES") {
		t.Errorf("error should name the offending predicate, got: %v", err)
	}
}

func TestQueryJoin_BadDirection(t *testing.T) {
	cfg := bogusPostgresConfig()
	_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: "a", PredicatePath: []string{"DEPENDS_ON"}, HopLimit: 1, Direction: "sideways",
	})
	if err == nil {
		t.Fatal("expected error for invalid direction")
	}
}

func TestQueryJoin_UnknownBackend(t *testing.T) {
	_, err := QueryJoin(context.Background(), supabaseConfig(), QueryJoinOptions{StartEntity: "a", HopLimit: 1})
	if err == nil {
		t.Fatal("expected error for non-postgres backend")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should mention the postgres requirement, got: %v", err)
	}
}

func TestQueryJoin_EmptyPredicatePathReachesNetwork(t *testing.T) {
	// An empty predicate path is not itself invalid -- hop_limit >= 1 is
	// still satisfied trivially since len(path)=0. It should clear every
	// pre-network validation (including the default-to-outgoing direction
	// path) and fail only once it tries to reach the bogus postgres URL.
	// Guards against an over-eager validator rejecting a legitimate
	// zero-hop request before it reaches the resolver.
	cfg := bogusPostgresConfig()
	_, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{StartEntity: "a", HopLimit: 1})
	if err == nil {
		t.Fatal("expected a connection error against the bogus URL")
	}
	if strings.Contains(err.Error(), "vocabulary") ||
		strings.Contains(err.Error(), "hop_limit") ||
		strings.Contains(err.Error(), "direction") {
		t.Errorf("expected a network-stage error, got a validation error: %v", err)
	}
}
