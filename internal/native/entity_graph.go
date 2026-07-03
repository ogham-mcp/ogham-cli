// Package native: entity_graph -- typed subject/predicate/object edges
// with write-time supersession. Ports Ogham v0.16's
// src/ogham/postgres/entity_graph.py (StoreTriple ~ store_triple,
// QueryJoin ~ query_join) plus the domain types in src/ogham/entity_graph.py
// and the MCP-boundary shapes in src/ogham/tools/entity_graph.py.
//
// StoreTriple introduces this repo's first pgx transaction: write-time
// supersession (stamp the old current row, insert the new row, back-fill
// superseded_by) must be atomic or a crash between steps could leave two
// "current" rows or an orphaned superseded_by pointer. See
// docs/scope/2026-07-02-typed-edge-verbs.md for the full design.
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// V1Predicates is the v1 predicate vocabulary -- mirrors the 16 seed rows
// in sql/migrations/042_entity_edge_predicates.sql exactly. SUPERSEDES is
// deliberately absent: write-time supersession via valid_to already covers
// the temporal transition, so a SUPERSEDES edge would be redundant.
var V1Predicates = map[string]struct{}{
	"DEPENDS_ON":     {},
	"DEPENDED_ON_BY": {},
	"OWNS":           {},
	"OWNED_BY":       {},
	"ASSIGNED_TO":    {},
	"HAS_ASSIGNEE":   {},
	"DECIDED":        {},
	"MENTIONS":       {},
	"BLOCKS":         {},
	"BLOCKED_BY":     {},
	"PART_OF":        {},
	"CONTAINS":       {},
	"SUPPORTS":       {},
	"CONTRADICTS":    {},
	"EVOLVED_INTO":   {},
	"RELATED_TO":     {},
}

// validatePredicate errors, naming the offender, if p is not in the v1
// vocabulary. Called before any SQL touches entity_edges.
func validatePredicate(p string) error {
	if _, ok := V1Predicates[p]; !ok {
		return fmt.Errorf("predicate %q not in v1 vocabulary", p)
	}
	return nil
}

// Entity mirrors Ogham v0.16's domain Entity dataclass.
type Entity struct {
	ID            int64  `json:"id"`
	CanonicalName string `json:"canonical_name"`
	EntityType    string `json:"entity_type"`
}

// Edge is the serialized shape from Python's _serialize_result.
type Edge struct {
	ID        int64          `json:"id"`
	SubjectID int64          `json:"subject_id"`
	Predicate string         `json:"predicate"`
	ObjectID  int64          `json:"object_id"`
	Profile   string         `json:"profile"`
	FactID    *string        `json:"fact_id"` // nil if no citation
	Strength  float64        `json:"strength"`
	Metadata  map[string]any `json:"metadata"`
	ValidFrom string         `json:"valid_from"` // ISO 8601
	ValidTo   *string        `json:"valid_to"`   // nil for current
}

// StoreTripleOptions captures a single write.
type StoreTripleOptions struct {
	Subject        string         // canonical name or alias
	Predicate      string         // must be in V1Predicates
	Object         string         // canonical name or alias
	Profile        string         // empty -> ActiveProfile(cfg)
	SourceMemoryID string         // empty or UUID string; empty -> SQL NULL
	Metadata       map[string]any // optional; nil/empty -> "{}"
}

// StoreTripleResult is returned by StoreTriple on success.
type StoreTripleResult struct {
	EdgeID int64 `json:"edge_id"`
}

// QueryJoinOptions captures a single traversal.
type QueryJoinOptions struct {
	StartEntity   string   // canonical name or alias
	PredicatePath []string // each must be in V1Predicates
	HopLimit      int      // REQUIRED, must be >= 1
	Direction     string   // "outgoing" (default) or "incoming"
	Profile       string   // empty -> ActiveProfile(cfg)
}

// QueryJoinResult is returned by QueryJoin. The three slices are always
// non-nil (even when empty) so JSON serializes "[]", never "null" --
// callers scripting against the CLI's JSON output should not have to
// special-case a null field.
type QueryJoinResult struct {
	Entities  []Entity `json:"entities"`  // BFS insertion order, NOT sorted by id
	Edges     []Edge   `json:"edges"`     // hop order
	Citations []string `json:"citations"` // fact_id strings, edge order
}

// emptyQueryJoinResult is the canonical "no path resolves" shape --
// {"entities":[],"edges":[],"citations":[]} -- returned (not an error) for
// an unresolvable start entity or a dead-end hop.
func emptyQueryJoinResult() QueryJoinResult {
	return QueryJoinResult{Entities: []Entity{}, Edges: []Edge{}, Citations: []string{}}
}

// StoreTriple stores a (subject, predicate, object) triple, superseding
// any current edge for the same (subject, predicate, object, profile).
// Faithful port of PostgresEntityGraph.store_triple.
//
// Unlike QueryJoin, StoreTriple errors on an unresolvable subject/object --
// a write against a target that doesn't exist is a caller mistake, not a
// legitimate empty result.
func StoreTriple(ctx context.Context, cfg *Config, opts StoreTripleOptions) (StoreTripleResult, error) {
	if cfg == nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: nil config")
	}
	if strings.TrimSpace(opts.Subject) == "" {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: subject is required")
	}
	if strings.TrimSpace(opts.Object) == "" {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: object is required")
	}
	if err := validatePredicate(opts.Predicate); err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: %w", err)
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return StoreTripleResult{}, err
	}
	if backend != "postgres" {
		return StoreTripleResult{}, fmt.Errorf("native triple/join require the postgres backend")
	}

	profile := opts.Profile
	if profile == "" {
		profile = ActiveProfile(cfg)
	}

	// source_memory_id is optional; empty means "no citation" -> SQL NULL,
	// not the empty string. Validate the UUID shape here (pre-network) so
	// a typo in --fact-id fails fast instead of surfacing as an opaque
	// Postgres cast error.
	var factID any
	if opts.SourceMemoryID != "" {
		if _, err := uuid.Parse(opts.SourceMemoryID); err != nil {
			return StoreTripleResult{}, fmt.Errorf("native store_triple: source_memory_id: %w", err)
		}
		factID = opts.SourceMemoryID
	}

	metadataJSON, err := json.Marshal(opts.Metadata)
	if err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: marshal metadata: %w", err)
	}
	if opts.Metadata == nil {
		metadataJSON = []byte("{}")
	}

	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	subjID, ok, err := resolveToID(ctx, conn, opts.Subject, profile)
	if err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: resolve subject: %w", err)
	}
	if !ok {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: cannot resolve subject=%q or object=%q", opts.Subject, opts.Object)
	}
	objID, ok, err := resolveToID(ctx, conn, opts.Object, profile)
	if err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: resolve object: %w", err)
	}
	if !ok {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: cannot resolve subject=%q or object=%q", opts.Subject, opts.Object)
	}
	// entity_edges has CHECK (subject_id <> object_id); reject here for a
	// clean domain error instead of an opaque constraint-violation.
	if subjID == objID {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: self-referential edges are not allowed")
	}

	// First transaction in this repo (see package doc comment): the three
	// statements below must commit or roll back together, or a crash mid-
	// sequence could leave two "current" rows (partial unique index
	// violated on the next write) or a superseded row with no
	// superseded_by pointer.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// (a) Stamp the old current row, if any. MUST run before the INSERT --
	// the partial unique index on (subject_id, predicate, object_id,
	// profile) WHERE valid_to IS NULL would otherwise reject the new row.
	if _, err := tx.Exec(ctx, `
UPDATE entity_edges
   SET valid_to = now()
 WHERE subject_id = $1 AND predicate = $2 AND object_id = $3
   AND profile = $4 AND valid_to IS NULL`,
		subjID, opts.Predicate, objID, profile,
	); err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: stamp old: %w", err)
	}

	// (b) Insert the new current row. strength is hardcoded to 1.0 -- v1
	// has no confidence-weighting mechanism yet.
	var newID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO entity_edges(
    subject_id, predicate, object_id, profile,
    fact_id, strength, metadata, valid_from, valid_to
) VALUES ($1, $2, $3, $4, $5::uuid, $6, $7::jsonb, now(), NULL)
RETURNING id`,
		subjID, opts.Predicate, objID, profile, factID, 1.0, metadataJSON,
	).Scan(&newID); err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: insert: %w", err)
	}

	// (c) Back-fill superseded_by on the row(s) just stamped in (a).
	if _, err := tx.Exec(ctx, `
UPDATE entity_edges
   SET superseded_by = $1
 WHERE subject_id = $2 AND predicate = $3 AND object_id = $4
   AND profile = $5 AND valid_to IS NOT NULL AND superseded_by IS NULL`,
		newID, subjID, opts.Predicate, objID, profile,
	); err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: backfill superseded_by: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return StoreTripleResult{}, fmt.Errorf("native store_triple: commit: %w", err)
	}

	return StoreTripleResult{EdgeID: newID}, nil
}

// QueryJoin walks the entity graph along PredicatePath starting at
// StartEntity, breadth-first, one predicate per hop. Faithful port of
// PostgresEntityGraph.query_join.
//
// Cross-verb asymmetry vs StoreTriple: an unresolvable StartEntity returns
// the empty result (no error) -- a read against nothing is a legitimate
// empty result, not a caller mistake.
func QueryJoin(ctx context.Context, cfg *Config, opts QueryJoinOptions) (QueryJoinResult, error) {
	empty := emptyQueryJoinResult()

	if cfg == nil {
		return empty, fmt.Errorf("native query_join: nil config")
	}
	if strings.TrimSpace(opts.StartEntity) == "" {
		return empty, fmt.Errorf("native query_join: start_entity is required")
	}
	// hop_limit has no default -- callers must declare intent (TBU-109).
	if opts.HopLimit < 1 {
		return empty, fmt.Errorf("native query_join: hop_limit is required and must be >= 1")
	}
	if opts.HopLimit < len(opts.PredicatePath) {
		return empty, fmt.Errorf("native query_join: hop_limit=%d smaller than predicate_path length %d", opts.HopLimit, len(opts.PredicatePath))
	}
	for _, p := range opts.PredicatePath {
		if err := validatePredicate(p); err != nil {
			return empty, fmt.Errorf("native query_join: %w", err)
		}
	}
	direction := opts.Direction
	if direction == "" {
		direction = "outgoing"
	}
	if direction != "outgoing" && direction != "incoming" {
		return empty, fmt.Errorf("native query_join: direction must be outgoing or incoming, got %q", direction)
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return empty, err
	}
	if backend != "postgres" {
		return empty, fmt.Errorf("native triple/join require the postgres backend")
	}

	profile := opts.Profile
	if profile == "" {
		profile = ActiveProfile(cfg)
	}

	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return empty, fmt.Errorf("native query_join: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	startID, ok, err := resolveToID(ctx, conn, opts.StartEntity, profile)
	if err != nil {
		return empty, fmt.Errorf("native query_join: resolve start_entity: %w", err)
	}
	if !ok {
		// Cross-verb asymmetry vs StoreTriple: an unresolvable start is a
		// legitimate empty read, not an error.
		return empty, nil
	}
	startEntity, err := fetchEntity(ctx, conn, startID)
	if err != nil {
		return empty, fmt.Errorf("native query_join: fetch start entity: %w", err)
	}

	// entities is built in BFS insertion order (start first, then each
	// hop's newly-discovered entities in encounter order) -- do NOT sort
	// by id (TBU-150). entitiesSeen just prevents a double-fetch/double-
	// append when a node is reached again via a different edge.
	entities := []Entity{startEntity}
	entitiesSeen := map[int64]struct{}{startID: {}}
	visited := map[int64]struct{}{startID: {}}
	// Declared as empty (not nil) slices so a successful result with no
	// citations still serializes "citations":[] rather than null -- the
	// same non-nil contract emptyQueryJoinResult() applies to the "no
	// path resolves" case.
	edges := []Edge{}
	citations := []string{}

	currentIDs := []int64{startID}
	for _, predicate := range opts.PredicatePath {
		var nextIDs []int64
		for _, curID := range currentIDs {
			hopEdges, err := queryHopEdges(ctx, conn, curID, predicate, profile, direction)
			if err != nil {
				return empty, fmt.Errorf("native query_join: query edges: %w", err)
			}
			for _, e := range hopEdges {
				edges = append(edges, e)
				if e.FactID != nil {
					citations = append(citations, *e.FactID)
				}
				var neighbour int64
				if direction == "outgoing" {
					neighbour = e.ObjectID
				} else {
					neighbour = e.SubjectID
				}
				if _, seen := visited[neighbour]; seen {
					continue // cycle -- edge recorded above, don't re-queue
				}
				visited[neighbour] = struct{}{}
				nextIDs = append(nextIDs, neighbour)
				if _, seen := entitiesSeen[neighbour]; !seen {
					ent, err := fetchEntity(ctx, conn, neighbour)
					if err != nil {
						return empty, fmt.Errorf("native query_join: fetch entity: %w", err)
					}
					entitiesSeen[neighbour] = struct{}{}
					entities = append(entities, ent)
				}
			}
		}
		if len(nextIDs) == 0 {
			// Dead-end hop: discard partials, faithful to Python's
			// `return None` -- the whole traversal is void, not a
			// truncated result.
			return empty, nil
		}
		currentIDs = nextIDs
	}

	return QueryJoinResult{Entities: entities, Edges: edges, Citations: citations}, nil
}

// queryHopEdges runs one hop's SELECT (outgoing or incoming) for a single
// current-id and returns the matched edges. Column order matches
// PostgresEntityGraph.query_join exactly; fact_id is cast to text (mirrors
// the id::text convention used elsewhere in this package) so it scans
// directly into Edge.FactID. valid_from/valid_to are left as timestamptz
// and scanned into time.Time so they can be formatted as RFC3339Nano
// (T-separated) below -- matching Python's datetime.isoformat() output in
// _serialize_result, NOT Postgres's ::text cast (which yields the
// space-separated "2006-01-02 15:04:05.999999+00" default form).
func queryHopEdges(ctx context.Context, conn *pgx.Conn, curID int64, predicate, profile, direction string) ([]Edge, error) {
	var query string
	if direction == "outgoing" {
		query = `
SELECT id, subject_id, predicate, object_id, profile,
       fact_id::text, strength, metadata, valid_from, valid_to
  FROM entity_edges
 WHERE subject_id = $1 AND predicate = $2
   AND profile = $3 AND valid_to IS NULL`
	} else {
		query = `
SELECT id, subject_id, predicate, object_id, profile,
       fact_id::text, strength, metadata, valid_from, valid_to
  FROM entity_edges
 WHERE object_id = $1 AND predicate = $2
   AND profile = $3 AND valid_to IS NULL`
	}

	rows, err := conn.Query(ctx, query, curID, predicate, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var (
			e            Edge
			metadataJSON []byte
			validFrom    time.Time
			validTo      *time.Time
		)
		if err := rows.Scan(
			&e.ID, &e.SubjectID, &e.Predicate, &e.ObjectID, &e.Profile,
			&e.FactID, &e.Strength, &metadataJSON, &validFrom, &validTo,
		); err != nil {
			return nil, err
		}
		e.ValidFrom = validFrom.Format(time.RFC3339Nano)
		if validTo != nil {
			s := validTo.Format(time.RFC3339Nano)
			e.ValidTo = &s
		}
		if len(metadataJSON) > 0 {
			var meta map[string]any
			if err := json.Unmarshal(metadataJSON, &meta); err != nil {
				return nil, fmt.Errorf("native query_join: unmarshal metadata (edge id=%d): %w", e.ID, err)
			}
			e.Metadata = meta
		}
		if e.Metadata == nil {
			e.Metadata = map[string]any{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// querier is satisfied by *pgx.Conn and pgx.Tx, letting resolveToID /
// fetchEntity run against either a bare connection (QueryJoin's read-only
// path) or inside StoreTriple's write transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolveToID ports PostgresEntityGraph._resolve_to_id: canonical name
// first (NOT profile-scoped -- canonical names are global), then alias
// fallback (profile-scoped, since the same surface form can mean different
// entities in different profiles). Returns ok=false, err=nil when neither
// lookup matches -- callers decide whether that's an error (StoreTriple)
// or a legitimate empty result (QueryJoin).
func resolveToID(ctx context.Context, q querier, nameOrID, profile string) (int64, bool, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM entities WHERE canonical_name = $1 LIMIT 1`, nameOrID).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}

	err = q.QueryRow(ctx, `
SELECT entity_id FROM entity_aliases
 WHERE alias = $1 AND profile = $2 LIMIT 1`, nameOrID, profile).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, err
}

// fetchEntity ports PostgresEntityGraph._fetch_entity.
func fetchEntity(ctx context.Context, q querier, id int64) (Entity, error) {
	var e Entity
	err := q.QueryRow(ctx,
		`SELECT id, canonical_name, entity_type FROM entities WHERE id = $1`, id,
	).Scan(&e.ID, &e.CanonicalName, &e.EntityType)
	if err != nil {
		return Entity{}, err
	}
	return e, nil
}
