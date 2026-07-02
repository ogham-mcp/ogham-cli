//go:build live

// Live integration tests for StoreTriple/QueryJoin -- ports the 6 + 7
// scenarios from upstream Python's
// tests/test_entity_graph_integration_store_triple.py and
// tests/test_entity_graph_integration_query_join.py.
//
// Requires a Postgres instance with migrations 041-043 applied (entities,
// entity_edges, entity_edge_predicates, entity_aliases). Opt in via:
//
//	DATABASE_URL="postgresql://ogham:ogham@localhost:5433/ogham_scratch" \
//	    go test -tags live -run TestLiveEntityGraph -v ./internal/native/
//
// Skipped automatically unless DATABASE_URL is set AND contains "scratch"
// -- mirrors the Python-side safety gate (these tests WRITE). Each test
// seeds entities with a unique canonical name (nanosecond suffix) and
// scopes edges/aliases to a unique profile, then cleans up via t.Cleanup
// so the shared scratch DB stays tidy across runs, regardless of order.

package native

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// entityGraphLiveConfig returns a Config wired to the scratch DB or skips
// the test. Stricter gate than liveCfg (wiki_recall_live_test.go): these
// tests write permanent entities/entity_edges/entity_aliases rows outside
// the memories-scoped cleanup that helper already knows how to do, so we
// additionally require "scratch" in DATABASE_URL -- the same safety net
// upstream Python's postgres_integration marker enforces.
func entityGraphLiveConfig(t *testing.T) (*Config, string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" || !strings.Contains(url, "scratch") {
		t.Skip(`DATABASE_URL not set to a "scratch" database; skipping live entity-graph test`)
	}
	profile := fmt.Sprintf("entity_graph_live_%d", time.Now().UnixNano())
	cfg := &Config{
		Database: Database{Backend: "postgres", URL: url},
		Profile:  profile,
	}
	return cfg, profile
}

// entityGraphUID returns a nanosecond-based suffix so seeded canonical
// names never collide across runs on a long-lived shared scratch DB.
func entityGraphUID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func entityGraphConnect(t *testing.T, cfg *Config) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

func seedEntity(t *testing.T, cfg *Config, name, entityType string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(ctx) }()

	var id int64
	err := conn.QueryRow(ctx,
		`INSERT INTO entities(canonical_name, entity_type) VALUES ($1, $2) RETURNING id`,
		name, entityType,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed entity %q: %v", name, err)
	}
	return id
}

func seedEntityAlias(t *testing.T, cfg *Config, entityID int64, alias, profile string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(ctx) }()

	_, err := conn.Exec(ctx,
		`INSERT INTO entity_aliases(entity_id, alias, profile) VALUES ($1, $2, $3)`,
		entityID, alias, profile,
	)
	if err != nil {
		t.Fatalf("seed alias %q: %v", alias, err)
	}
}

// entityGraphCleanup deletes every row this test's profile(s) and entity
// ids touched. entity_edges/entity_aliases cascade off entities via FK
// (ON DELETE CASCADE), but we delete by profile too in case a test spans
// more than one profile (e.g. profile isolation).
func entityGraphCleanup(t *testing.T, cfg *Config, profiles []string, entityIDs []int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			t.Logf("cleanup: connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()

		for _, p := range profiles {
			if _, err := conn.Exec(ctx, `DELETE FROM entity_edges WHERE profile = $1`, p); err != nil {
				t.Logf("cleanup entity_edges profile=%s: %v", p, err)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM entity_aliases WHERE profile = $1`, p); err != nil {
				t.Logf("cleanup entity_aliases profile=%s: %v", p, err)
			}
		}
		for _, id := range entityIDs {
			if _, err := conn.Exec(ctx, `DELETE FROM entities WHERE id = $1`, id); err != nil {
				t.Logf("cleanup entities id=%d: %v", id, err)
			}
		}
	})
}

// -----------------------------------------------------------------------
// store_triple (6 scenarios, ports
// tests/test_entity_graph_integration_store_triple.py)

func TestLiveEntityGraph_StoreTriple_NewTriple(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	a := seedEntity(t, cfg, "AuthService-"+uid, "service")
	b := seedEntity(t, cfg, "LoginModule-"+uid, "module")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	res, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "AuthService-" + uid, Predicate: "DEPENDS_ON", Object: "LoginModule-" + uid, Profile: profile,
	})
	if err != nil {
		t.Fatalf("StoreTriple: %v", err)
	}

	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(context.Background()) }()
	var count int
	if err := conn.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM entity_edges WHERE id=$1 AND valid_to IS NULL`, res.EdgeID,
	).Scan(&count); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 current row for the new edge, got %d", count)
	}
}

func TestLiveEntityGraph_StoreTriple_DuplicateSupersedes(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	a := seedEntity(t, cfg, "A-"+uid, "s")
	b := seedEntity(t, cfg, "B-"+uid, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	opts := StoreTripleOptions{Subject: "A-" + uid, Predicate: "DEPENDS_ON", Object: "B-" + uid, Profile: profile}
	e1, err := StoreTriple(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("StoreTriple #1: %v", err)
	}
	e2, err := StoreTriple(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("StoreTriple #2: %v", err)
	}
	if e1.EdgeID == e2.EdgeID {
		t.Fatal("expected a new edge id on the second write")
	}

	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(context.Background()) }()
	var count int
	if err := conn.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM entity_edges WHERE subject_id=$1 AND object_id=$2 AND profile=$3 AND valid_to IS NULL`,
		a, b, profile,
	).Scan(&count); err != nil {
		t.Fatalf("verify count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 current row, got %d", count)
	}

	var validToIsNull bool
	var supersededBy *int64
	if err := conn.QueryRow(context.Background(),
		`SELECT valid_to IS NULL, superseded_by FROM entity_edges WHERE id=$1`, e1.EdgeID,
	).Scan(&validToIsNull, &supersededBy); err != nil {
		t.Fatalf("verify e1: %v", err)
	}
	if validToIsNull {
		t.Error("e1.valid_to should be set (superseded)")
	}
	if supersededBy == nil || *supersededBy != e2.EdgeID {
		t.Errorf("e1.superseded_by = %v, want %d", supersededBy, e2.EdgeID)
	}
}

func TestLiveEntityGraph_StoreTriple_DifferentObjectsDoNotSupersede(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	a := seedEntity(t, cfg, "A-"+uid, "s")
	b := seedEntity(t, cfg, "B-"+uid, "s")
	c := seedEntity(t, cfg, "C-"+uid, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b, c})

	e1, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "A-" + uid, Predicate: "OWNS", Object: "B-" + uid, Profile: profile,
	})
	if err != nil {
		t.Fatalf("StoreTriple #1: %v", err)
	}
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "A-" + uid, Predicate: "OWNS", Object: "C-" + uid, Profile: profile,
	}); err != nil {
		t.Fatalf("StoreTriple #2: %v", err)
	}

	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(context.Background()) }()

	var validToIsNull bool
	var supersededBy *int64
	if err := conn.QueryRow(context.Background(),
		`SELECT valid_to IS NULL, superseded_by FROM entity_edges WHERE id=$1`, e1.EdgeID,
	).Scan(&validToIsNull, &supersededBy); err != nil {
		t.Fatalf("verify e1: %v", err)
	}
	if !validToIsNull {
		t.Error("e1 should still be current")
	}
	if supersededBy != nil {
		t.Error("e1 should never have been superseded")
	}

	var count int
	if err := conn.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM entity_edges WHERE subject_id=$1 AND predicate='OWNS' AND profile=$2 AND valid_to IS NULL`,
		a, profile,
	).Scan(&count); err != nil {
		t.Fatalf("verify count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 current OWNS edges from A, got %d", count)
	}
}

func TestLiveEntityGraph_StoreTriple_AliasResolution(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	a := seedEntity(t, cfg, "AuthService-"+uid, "s")
	b := seedEntity(t, cfg, "LoginModule-"+uid, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	alias := "auth-" + uid
	seedEntityAlias(t, cfg, a, alias, profile)

	res, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: alias, Predicate: "DEPENDS_ON", Object: "LoginModule-" + uid, Profile: profile,
	})
	if err != nil {
		t.Fatalf("StoreTriple: %v", err)
	}

	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(context.Background()) }()
	var subjectID int64
	if err := conn.QueryRow(context.Background(),
		`SELECT subject_id FROM entity_edges WHERE id=$1`, res.EdgeID,
	).Scan(&subjectID); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if subjectID != a {
		t.Errorf("subject_id = %d, want %d (canonical entity via alias)", subjectID, a)
	}
}

func TestLiveEntityGraph_StoreTriple_ProfileIsolation(t *testing.T) {
	cfg, _ := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	a := seedEntity(t, cfg, "A-"+uid, "s")
	b := seedEntity(t, cfg, "B-"+uid, "s")
	profileWork := "work-" + uid
	profilePersonal := "personal-" + uid
	entityGraphCleanup(t, cfg, []string{profileWork, profilePersonal}, []int64{a, b})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "A-" + uid, Predicate: "DEPENDS_ON", Object: "B-" + uid, Profile: profileWork,
	}); err != nil {
		t.Fatalf("StoreTriple work: %v", err)
	}
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "A-" + uid, Predicate: "DEPENDS_ON", Object: "B-" + uid, Profile: profilePersonal,
	}); err != nil {
		t.Fatalf("StoreTriple personal: %v", err)
	}

	conn := entityGraphConnect(t, cfg)
	defer func() { _ = conn.Close(context.Background()) }()
	rows, err := conn.Query(context.Background(),
		`SELECT profile FROM entity_edges WHERE subject_id=$1 AND object_id=$2 AND valid_to IS NULL ORDER BY profile`,
		a, b,
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	defer rows.Close()
	var profiles []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		profiles = append(profiles, p)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 current rows (one per profile), got %d: %v", len(profiles), profiles)
	}
}

func TestLiveEntityGraph_StoreTriple_UnresolvableSubjectErrors(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	b := seedEntity(t, cfg, "B-"+uid, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{b})

	_, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: "ghost-" + uid, Predicate: "DEPENDS_ON", Object: "B-" + uid, Profile: profile,
	})
	if err == nil {
		t.Fatal("expected error for unresolvable subject")
	}
	if !strings.Contains(err.Error(), "cannot resolve") {
		t.Errorf("error should mention 'cannot resolve', got: %v", err)
	}
}

// -----------------------------------------------------------------------
// query_join (7 scenarios, ports
// tests/test_entity_graph_integration_query_join.py)

func TestLiveEntityGraph_QueryJoin_SingleHop(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	aName, bName := "A-"+uid, "B-"+uid
	a := seedEntity(t, cfg, aName, "s")
	b := seedEntity(t, cfg, bName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: aName, Predicate: "DEPENDS_ON", Object: bName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: aName, PredicatePath: []string{"DEPENDS_ON"}, HopLimit: 1, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	if len(res.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(res.Edges))
	}
	names := map[string]struct{}{}
	for _, e := range res.Entities {
		names[e.CanonicalName] = struct{}{}
	}
	if _, ok := names[aName]; !ok {
		t.Errorf("entities missing %q: %+v", aName, res.Entities)
	}
	if _, ok := names[bName]; !ok {
		t.Errorf("entities missing %q: %+v", bName, res.Entities)
	}

	// JSON parity guard: Edge.ValidFrom must be T-separated RFC3339Nano
	// (matches Python's datetime.isoformat() in _serialize_result), not
	// Postgres's space-separated ::text default. ValidTo must be nil for
	// a current (unsuperseded) edge.
	edge := res.Edges[0]
	if _, err := time.Parse(time.RFC3339Nano, edge.ValidFrom); err != nil {
		t.Errorf("ValidFrom %q does not parse as RFC3339Nano: %v", edge.ValidFrom, err)
	}
	if !strings.Contains(edge.ValidFrom, "T") {
		t.Errorf("ValidFrom %q is not T-separated ISO-8601", edge.ValidFrom)
	}
	if edge.ValidTo != nil {
		t.Errorf("ValidTo = %v, want nil for a current edge", *edge.ValidTo)
	}
}

func TestLiveEntityGraph_QueryJoin_MultiHop(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	aName, bName, cName := "A-"+uid, "B-"+uid, "C-"+uid
	a := seedEntity(t, cfg, aName, "s")
	b := seedEntity(t, cfg, bName, "s")
	c := seedEntity(t, cfg, cName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b, c})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: aName, Predicate: "DEPENDS_ON", Object: bName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 1: %v", err)
	}
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: bName, Predicate: "OWNS", Object: cName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 2: %v", err)
	}

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: aName, PredicatePath: []string{"DEPENDS_ON", "OWNS"}, HopLimit: 2, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	if len(res.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(res.Edges))
	}
	names := map[string]struct{}{}
	for _, e := range res.Entities {
		names[e.CanonicalName] = struct{}{}
	}
	for _, want := range []string{aName, bName, cName} {
		if _, ok := names[want]; !ok {
			t.Errorf("entities missing %q: %+v", want, res.Entities)
		}
	}
}

// TestLiveEntityGraph_QueryJoin_BFSOrder asserts the exact ordered
// [start, b, c] entity-name sequence (TBU-150 contract) -- NOT a set
// membership check, which wouldn't catch a regression that silently
// sorted the list by id.
func TestLiveEntityGraph_QueryJoin_BFSOrder(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	aName, bName, cName := "A-"+uid, "B-"+uid, "C-"+uid
	a := seedEntity(t, cfg, aName, "s")
	b := seedEntity(t, cfg, bName, "s")
	c := seedEntity(t, cfg, cName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b, c})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: aName, Predicate: "DEPENDS_ON", Object: bName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 1: %v", err)
	}
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: bName, Predicate: "OWNS", Object: cName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 2: %v", err)
	}

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: aName, PredicatePath: []string{"DEPENDS_ON", "OWNS"}, HopLimit: 2, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	if len(res.Entities) != 3 {
		t.Fatalf("expected 3 entities, got %d: %+v", len(res.Entities), res.Entities)
	}
	got := []string{res.Entities[0].CanonicalName, res.Entities[1].CanonicalName, res.Entities[2].CanonicalName}
	want := []string{aName, bName, cName}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entities[%d] = %q, want %q (BFS insertion order, not sorted): got %v", i, got[i], want[i], got)
			break
		}
	}
}

func TestLiveEntityGraph_QueryJoin_NoPathReturnsEmpty(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	aName, bName := "A-"+uid, "B-"+uid
	a := seedEntity(t, cfg, aName, "s")
	b := seedEntity(t, cfg, bName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: aName, Predicate: "DEPENDS_ON", Object: bName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: aName, PredicatePath: []string{"OWNS"}, HopLimit: 1, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	if len(res.Entities) != 0 || len(res.Edges) != 0 || len(res.Citations) != 0 {
		t.Errorf("expected the empty result, got %+v", res)
	}
}

func TestLiveEntityGraph_QueryJoin_CycleDetectionTerminates(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	aName, bName := "A-"+uid, "B-"+uid
	a := seedEntity(t, cfg, aName, "s")
	b := seedEntity(t, cfg, bName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: aName, Predicate: "RELATED_TO", Object: bName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 1: %v", err)
	}
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: bName, Predicate: "RELATED_TO", Object: aName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge 2: %v", err)
	}

	// Walk A -[RELATED_TO]-> B -[RELATED_TO]-> ?? -- A is already visited
	// by hop 2, so there's nowhere new to go and the traversal terminates
	// with the empty result instead of looping back to A.
	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: aName, PredicatePath: []string{"RELATED_TO", "RELATED_TO"}, HopLimit: 2, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	if len(res.Entities) != 0 || len(res.Edges) != 0 {
		t.Errorf("expected the empty result (dead-end cycle), got %+v", res)
	}
}

func TestLiveEntityGraph_QueryJoin_AliasStartEntity(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()
	authName, loginName := "AuthService-"+uid, "LoginModule-"+uid
	a := seedEntity(t, cfg, authName, "s")
	b := seedEntity(t, cfg, loginName, "s")
	entityGraphCleanup(t, cfg, []string{profile}, []int64{a, b})

	alias := "auth-" + uid
	seedEntityAlias(t, cfg, a, alias, profile)
	if _, err := StoreTriple(context.Background(), cfg, StoreTripleOptions{
		Subject: authName, Predicate: "DEPENDS_ON", Object: loginName, Profile: profile,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: alias, PredicatePath: []string{"DEPENDS_ON"}, HopLimit: 1, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin: %v", err)
	}
	names := map[string]struct{}{}
	for _, e := range res.Entities {
		names[e.CanonicalName] = struct{}{}
	}
	if _, ok := names[authName]; !ok {
		t.Errorf("entities missing %q: %+v", authName, res.Entities)
	}
	if _, ok := names[loginName]; !ok {
		t.Errorf("entities missing %q: %+v", loginName, res.Entities)
	}
}

func TestLiveEntityGraph_QueryJoin_UnresolvableStartReturnsEmpty(t *testing.T) {
	cfg, profile := entityGraphLiveConfig(t)
	uid := entityGraphUID()

	res, err := QueryJoin(context.Background(), cfg, QueryJoinOptions{
		StartEntity: "ghost-" + uid, PredicatePath: []string{"DEPENDS_ON"}, HopLimit: 1, Profile: profile,
	})
	if err != nil {
		t.Fatalf("QueryJoin should not error on unresolvable start (that's StoreTriple's contract, not QueryJoin's): %v", err)
	}
	if len(res.Entities) != 0 || len(res.Edges) != 0 || len(res.Citations) != 0 {
		t.Errorf("expected the empty result, got %+v", res)
	}
}
