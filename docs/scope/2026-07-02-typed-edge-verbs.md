# Scope: typed-edge verbs in ogham-cli (upstream Ogham v0.16)

**Prepared by**: Ogham dev-fleet orchestrator, 2026-07-02
**For**: ogham-cli's own coding agent
**Upstream Linear**: TBU-115 (`[v0.16] query_join MCP tool + backend method + Go native verb`)
**Upstream milestone target**: v0.16 landing on PyPI by 2026-07-08

## What Ogham v0.16 shipped upstream

The upstream memory server just landed a typed-edge context graph. Two new MCP tools + a controlled predicate vocabulary + write-time supersession. That's live in the Python codebase but ogham-cli has zero native support for it -- everything today falls back to the gateway/MCP path. This scope package covers building the two verbs natively so `ogham triple` and `ogham join` work without a gateway round-trip.

### The MCP tools you're mirroring

**`store_triple`** (Python signature from `src/ogham/tools/entity_graph.py`):

```python
@mcp.tool
def store_triple(
    subject: str,           # canonical name or alias
    predicate: str,         # must be in v1 vocab (see below)
    object_: str,           # canonical name or alias
    profile: str | None = None,       # None -> active profile
    source_memory_id: str | None = None,  # UUID string, cites source memory
    metadata: DictAny = None,             # free-form JSONB
) -> dict:  # {"edge_id": int}
```

Semantics:

- Predicate validated against vocab at write time (see V1_PREDICATES below).
- Subject/object resolve via `entities.canonical_name` first, then `entity_aliases` scoped to the profile.
- **Write-time supersession**: if a current edge exists for `(subject_id, predicate, object_id, profile)`, its `valid_to` is stamped `now()` and `superseded_by` points at the new row. Row stays, moves from current to historical partition.
- Errors on unresolvable subject/object (`ValueError` in Python -- return an error to the caller in Go).
- Returns `{"edge_id": <int>}` on success.

**`query_join`** (Python signature):

```python
@mcp.tool
def query_join(
    start_entity: str,               # canonical name or alias
    predicate_path: list[str],       # sequence of predicates
    hop_limit: int,                  # REQUIRED -- no default
    direction: str = "outgoing",     # "outgoing" or "incoming"
    profile: str | None = None,      # None -> active profile
) -> dict:                            # serialized JoinResult
```

Semantics:

- BFS traversal along the predicate path, per hop.
- `hop_limit` is required and must be >= 1 (Python raises `ValueError` on missing / < 1).
- `predicate_path` items validated against vocab.
- Path either resolves fully or returns the empty-result shape `{"entities": [], "edges": [], "citations": []}`.
- **Entity list is in BFS insertion order** (TBU-150 contract) -- start entity first, then each hop's discovered entities in traversal order. NOT sorted by id. `edges[0]` is hop 1, `edges[1]` is hop 2, etc.
- Reads only current edges (`valid_to IS NULL`).
- Cycle detection via visited set (`set[int]` in Python).
- Cross-verb asymmetry: **unresolvable `start_entity` returns `None` in Python** (empty-result shape at the tool boundary) -- NOT an error. A read against nothing is a legitimate empty result. Contrast with `store_triple` which raises.

## v1 predicate vocabulary (16 names, 10 concepts)

From `sql/migrations/042_entity_edge_predicates.sql` seed, `scope = 'entity'`:

| Predicate | Inverse | |
|---|---|---|
| DEPENDS_ON | DEPENDED_ON_BY | Structural dependency |
| OWNS | OWNED_BY | Authority / ownership |
| ASSIGNED_TO | HAS_ASSIGNEE | Task -> person, item -> box |
| DECIDED | -- | Agent -> decision fact |
| MENTIONS | -- | Subject mentions object |
| BLOCKS | BLOCKED_BY | Progress blocker |
| PART_OF | CONTAINS | Structural composition |
| SUPPORTS | CONTRADICTS | Evidence relation (entity-scope) |
| EVOLVED_INTO | -- | Object is a later version of subject |
| RELATED_TO | -- | Low-signal catchall -- prefer a specific predicate |

`SUPERSEDES` is deliberately absent -- write-time supersession via `valid_to` covers the temporal transition.

**Suggested Go representation**: `var V1Predicates = map[string]struct{}{...}` (16 entries). Validate at construction, before hitting Postgres. Return an error mentioning the offending predicate name.

## Schema you're writing SQL against

Migrations 041-043 land the tables. Read them for column names + constraints:

- `sql/migrations/041_entity_edges.sql` -- `entity_edges` (id bigint PK, subject_id, predicate text, object_id, profile, fact_id uuid, strength real, metadata jsonb, valid_from, valid_to, superseded_by)
- `sql/migrations/042_entity_edge_predicates.sql` -- `entity_edge_predicates` (predicate PK, label, description, inverse, scope)
- `sql/migrations/043_entity_aliases.sql` -- `entity_aliases` (id, entity_id, alias, profile, UNIQUE(alias, profile))

Partial unique index on `entity_edges(subject_id, predicate, object_id, profile) WHERE valid_to IS NULL` enforces "at most one current edge per (subject, predicate, object, profile)".

Read the Python backend implementations for the SQL shapes:

- `src/ogham/postgres/entity_graph.py::PostgresEntityGraph.store_triple` -- the write-time supersession SQL (BEGIN + stamp old + INSERT new + COMMIT, single transaction)
- `src/ogham/postgres/entity_graph.py::PostgresEntityGraph.query_join` -- the BFS loop against `entity_edges_subject_pred_current` / `entity_edges_object_pred_current` indexes

**Copy the SQL shape exactly** -- these were validated in TBU-122/123 integration tests against a real Postgres. Don't reinvent the queries.

## Files to create in ogham-cli

```
internal/native/entity_graph.go            # the two verbs + supporting types
internal/native/entity_graph_test.go       # unit tests (mocked pgx.Conn or in-memory)
internal/native/entity_graph_live_test.go  # integration tests against scratch DB
cmd/triple.go                              # `ogham triple` CLI command (or fold into cmd/entity_graph.go)
cmd/join.go                                # `ogham join` CLI command  (or fold into cmd/entity_graph.go)
```

Follow the shape of `internal/native/wiki_walk_knowledge.go` (261 lines, mirrors `walk_memory_graph`) -- it's the closest existing template for `query_join`. And `internal/native/store.go` for the write path (has options struct, config integration, extraction pipeline is optional, adjust for the typed-edge case).

## Verb signatures (Go)

```go
package native

// StoreTripleOptions captures a single write.
type StoreTripleOptions struct {
    Subject        string            // canonical name or alias
    Predicate      string            // must be in V1Predicates
    Object         string            // canonical name or alias
    Profile        string            // empty -> ActiveProfile()
    SourceMemoryID string            // empty or UUID string; empty -> nil in DB
    Metadata       map[string]any    // optional; empty -> "{}"
}

type StoreTripleResult struct {
    EdgeID int64 `json:"edge_id"`
}

func StoreTriple(ctx context.Context, cfg Config, opts StoreTripleOptions) (StoreTripleResult, error)
```

```go
// QueryJoinOptions captures a single traversal.
type QueryJoinOptions struct {
    StartEntity   string   // canonical name or alias
    PredicatePath []string // each must be in V1Predicates
    HopLimit      int      // REQUIRED, must be >= 1
    Direction     string   // "outgoing" (default) or "incoming"
    Profile       string   // empty -> ActiveProfile()
}

// Entity mirrors Ogham v0.16 domain module.
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
    FactID    *string        `json:"fact_id"`      // nil if no citation
    Strength  float64        `json:"strength"`
    Metadata  map[string]any `json:"metadata"`
    ValidFrom string         `json:"valid_from"`   // ISO 8601
    ValidTo   *string        `json:"valid_to"`     // nil for current
}

type QueryJoinResult struct {
    Entities  []Entity `json:"entities"`   // BFS insertion order, NOT sorted by id
    Edges     []Edge   `json:"edges"`      // hop order
    Citations []string `json:"citations"`  // fact_id strings, edge order
}

// QueryJoin returns an empty QueryJoinResult (not an error) when the
// path resolves to nothing -- read-against-nothing is legitimate.
// It returns an error only for: unknown predicate in path, hop_limit < 1,
// invalid direction, or a real DB error.
func QueryJoin(ctx context.Context, cfg Config, opts QueryJoinOptions) (QueryJoinResult, error)
```

## Contract details you MUST NOT get wrong

1. **`hop_limit` required, no default.** Return an error like `"hop_limit is required and must be >= 1"` if caller passes 0 or negative. Do NOT silently default.
2. **Predicate vocab is authoritative at write.** Validate `Predicate` (store_triple) and every element of `PredicatePath` (query_join) BEFORE issuing SQL. Return an error naming the offending predicate.
3. **Write-time supersession is a single transaction.** Wrap old-stamp + new-insert in one `pgx.Tx`. If one fails, both roll back.
4. **BFS insertion order.** As you discover new entities per hop, append to a slice in the order encountered. Do NOT sort by id at the end. TBU-150 shipped an integration test (`test_query_join_entities_returned_in_bfs_insertion_order`) that would fail if you sort.
5. **Cross-verb asymmetry.** `StoreTriple` returns an error on unresolvable subject/object. `QueryJoin` returns an empty result (no error) on unresolvable `start_entity`.
6. **`fact_id` is optional.** `source_memory_id` empty -> pass `nil` to Postgres, not empty string. `Edge.FactID` is `*string` for the same reason.
7. **profile defaulting.** Empty profile string -> `native.ActiveProfile()` (existing helper in `internal/native/active_profile.go`). Do NOT hardcode `"default"`.

## CLI command shape (Cobra)

```
ogham triple <subject> <predicate> <object> [flags]

Flags:
  --profile string          Ogham profile (default: active profile)
  --fact-id string          UUID of the memory that produced this claim
  --metadata string         JSON dict, free-form (default: {})
  --json                    Output as JSON (default: pretty-print edge_id)

ogham join <start-entity> [flags]

Flags:
  --path strings            Comma-separated predicate path, e.g. DEPENDS_ON,OWNS
  --hop-limit int           REQUIRED. Maximum hops.
  --direction string        outgoing (default) or incoming
  --profile string          Ogham profile (default: active profile)
  --json                    Output as JSON (default: pretty tree)
```

Copy Cobra registration pattern from `cmd/store.go`. Register in `cmd/root.go` or wherever the existing verbs register.

## Tests

Same shape as `internal/native/wiki_walk_knowledge_test.go` + `wiki_recall_live_test.go`:

- **Unit tests** (`entity_graph_test.go`): predicate-vocab validation, hop_limit validation, direction validation, options-defaulting. Fast, no DB.
- **Live integration tests** (`entity_graph_live_test.go`): the same 6 + 7 scenarios that upstream Python shipped in `tests/test_entity_graph_integration_store_triple.py` + `tests/test_entity_graph_integration_query_join.py`. Read those files for the exact scenarios -- port each. Use `t.Skip` if `DATABASE_URL` doesn't contain "scratch" (mirror the Python gate).

Integration test DB: the same Docker `postgres-scratch` container upstream Python uses (`postgresql://ogham:ogham@localhost:5433/ogham_scratch`, migrations 041-043 already applied). Coordinate on this via env var.

## What NOT to do

- **Do NOT modify SQL migrations.** Python owns the schema. If you find a bug in the SQL, file an upstream issue and hand it back.
- **Do NOT modify Python code.** Cross-repo change requires coordination.
- **Do NOT add SUPERSEDES to the vocab.** It's not in v1 by design.
- **Do NOT default `hop_limit`.** Callers declare intent -- this is a load-bearing contract from TBU-109.
- **Do NOT sort `Entities` by id.** BFS insertion order is contractual per TBU-150.
- **Do NOT use the gateway path for these verbs unless native fails.** Native goes straight to Postgres -- gateway is only for `ogham serve` / hosted deployments.

## Deployment / ship gate

Upstream Ogham v0.16 is targeting 2026-07-08 for PyPI. If Go verbs land in ogham-cli before then, they can ship together. If not, upstream ships without them and ogham-cli picks them up in v0.11 or v0.16.1 with a `ogham triple` / `ogham join` note in the CHANGELOG.

Regression gate: `go test ./internal/native/... -count=1` must still pass after your changes. Live tests require `DATABASE_URL='postgresql://ogham:ogham@localhost:5433/ogham_scratch' go test -tags integration ./internal/native/entity_graph_live_test.go` or equivalent.

## Commit style

Follow the ogham-cli convention (see recent git log):

```
feat(entity-graph): typed-edge verbs (store_triple, query_join)

Ports upstream Ogham v0.16 (TBU-114/116) MCP tools to native Go verbs.
Direct Postgres path via pgx -- no gateway round-trip. Predicate
validated against v1 vocab at write time; hop_limit required; BFS
insertion order on entity list per TBU-150. Live tests mirror upstream
TBU-122/123 integration scenarios against a scratch DB.

Upstream: TBU-115. Ship gate: v0.16 on PyPI (targeting 2026-07-08).
```

## References (read these upstream files before starting)

All in `/Users/kevinburns/Developer/web-projects/openbrain-sharedmemory`:

- `src/ogham/tools/entity_graph.py` -- MCP tool wrapper shapes
- `src/ogham/postgres/entity_graph.py` -- SQL shapes to copy
- `src/ogham/entity_graph.py` -- domain types + V1_PREDICATES constant
- `tests/test_entity_graph_integration_store_triple.py` -- 6 integration scenarios to port
- `tests/test_entity_graph_integration_query_join.py` -- 7 integration scenarios to port
- `sql/migrations/041_entity_edges.sql` + `042_entity_edge_predicates.sql` + `043_entity_aliases.sql` -- schema
- `docs/superpowers/plans/2026-07-02-typed-edge-v0.16-alpha.md` -- full upstream plan (has TBU-114 / TBU-116 / TBU-122 / TBU-123 sections that describe the exact behaviour to mirror)

## Open questions for Kevin

1. CLI command names: `ogham triple` / `ogham join` (short) or `ogham store-triple` / `ogham query-join` (mirrors MCP tool names). Recommend short.
2. Gateway path fallback: should `ogham triple` try the gateway if native Postgres isn't configured? Existing pattern in `cmd/store.go` is native-first, gateway-fallback -- follow that or opt native-only?
3. Cadence: ship Go verbs in v0.16 (bundle with upstream), or defer to v0.16.1 and ship upstream without CLI parity?
