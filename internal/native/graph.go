package native

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Batch E: graph walk absorbed natively. Wraps the SQL-side functions
// explore_memory_graph, get_related_memories, link_unlinked_memories.
// suggest_connections uses an inline recursive-CTE query -- the Python
// side hard-coded the SQL, we do the same here.
//
// These depend on the memory_relationships table + auto_link_memory()
// function being present in the schema. Both shipped in v0.3+ installs.

// -----------------------------------------------------------------------
// link_unlinked

// LinkUnlinkedResult is the payload for the MCP tool.
type LinkUnlinkedResult struct {
	Status    string `json:"status"` // "linked" | "nothing_to_link"
	Profile   string `json:"profile"`
	Processed int    `json:"processed"`
	BatchSize int    `json:"batch_size"`
}

// LinkUnlinkedOptions mirrors the Python tool. Defaults match Python:
// batch_size=100, threshold=0.85, max_links=5.
type LinkUnlinkedOptions struct {
	BatchSize int
	Threshold float64
	MaxLinks  int
	Profile   string
}

// LinkUnlinked backfills auto-links for memories that don't have any
// yet. Returns the count of memories processed (not links created).
// Call repeatedly until processed=0 after a batch import.
func LinkUnlinked(ctx context.Context, cfg *Config, opts LinkUnlinkedOptions) (*LinkUnlinkedResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native link_unlinked: nil config")
	}
	profile := opts.Profile
	if profile == "" {
		profile = ActiveProfile(cfg)
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = 0.85
	}
	maxLinks := opts.MaxLinks
	if maxLinks <= 0 {
		maxLinks = 5
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	var processed int
	switch backend {
	case "postgres":
		processed, err = linkUnlinkedPostgres(ctx, cfg, profile, threshold, maxLinks, batchSize)
	case "supabase":
		processed, err = linkUnlinkedSupabase(ctx, cfg, profile, threshold, maxLinks, batchSize)
	default:
		return nil, fmt.Errorf("native link_unlinked: unknown backend %q", backend)
	}
	if err != nil {
		return nil, err
	}

	status := "linked"
	if processed == 0 {
		status = "nothing_to_link"
	}
	return &LinkUnlinkedResult{
		Status:    status,
		Profile:   profile,
		Processed: processed,
		BatchSize: batchSize,
	}, nil
}

func linkUnlinkedPostgres(ctx context.Context, cfg *Config, profile string, threshold float64, maxLinks, batchSize int) (int, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return 0, fmt.Errorf("link_unlinked: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var n int
	err = conn.QueryRow(ctx,
		"SELECT link_unlinked_memories($1, $2, $3, $4)",
		profile, threshold, maxLinks, batchSize).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("link_unlinked: rpc: %w", err)
	}
	return n, nil
}

func linkUnlinkedSupabase(ctx context.Context, cfg *Config, profile string, threshold float64, maxLinks, batchSize int) (int, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return 0, err
	}
	raw, err := client.callRPC(ctx, "link_unlinked_memories", map[string]any{
		"filter_profile": profile,
		"link_threshold": threshold,
		"max_links":      maxLinks,
		"batch_size":     batchSize,
	})
	if err != nil {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("link_unlinked: parse: %w", err)
	}
	return n, nil
}

// -----------------------------------------------------------------------
// create_relationship (write path for store_decision.related_memories)

// Relationship types the schema accepts. Matches the PostgreSQL
// `relationship_type` enum defined in schema_postgres.sql. Kept as
// a sorted slice so clients can discover the valid set.
var ValidRelationshipTypes = []string{
	"supports", "contradicts", "supersedes", "refines", "references",
}

// CreateRelationshipOptions captures one edge creation. strength gets
// clamped to [0, 1]; createdBy defaults to "user" for tool-driven
// writes (auto-linker uses "auto" directly).
type CreateRelationshipOptions struct {
	SourceID     string
	TargetID     string
	Relationship string  // one of ValidRelationshipTypes
	Strength     float64 // default 1.0
	CreatedBy    string  // default "user"
	Metadata     map[string]any
}

// CreateRelationship inserts a single edge into memory_relationships.
// Used by store_decision.related_memories to materialise the supporting
// links after the new decision is stored. Idempotent on the unique key
// (source_id, target_id, relationship) -- repeat calls return the
// existing edge rather than erroring.
func CreateRelationship(ctx context.Context, cfg *Config, opts CreateRelationshipOptions) error {
	if cfg == nil {
		return fmt.Errorf("native create_relationship: nil config")
	}
	if opts.SourceID == "" || opts.TargetID == "" {
		return fmt.Errorf("native create_relationship: source_id + target_id required")
	}
	if opts.SourceID == opts.TargetID {
		return fmt.Errorf("native create_relationship: self-link rejected")
	}
	rel := opts.Relationship
	if rel == "" {
		rel = "supports"
	}
	valid := false
	for _, v := range ValidRelationshipTypes {
		if rel == v {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("native create_relationship: relationship must be one of %v; got %q",
			ValidRelationshipTypes, rel)
	}
	strength := opts.Strength
	if strength == 0 {
		strength = 1.0
	}
	if strength < 0 || strength > 1 {
		return fmt.Errorf("native create_relationship: strength must be in [0, 1]; got %v", strength)
	}
	createdBy := opts.CreatedBy
	if createdBy == "" {
		createdBy = "user"
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return err
	}
	switch backend {
	case "postgres":
		return createRelationshipPostgres(ctx, cfg, opts.SourceID, opts.TargetID, rel, strength, createdBy, opts.Metadata)
	case "supabase":
		return createRelationshipSupabase(ctx, cfg, opts.SourceID, opts.TargetID, rel, strength, createdBy, opts.Metadata)
	default:
		return fmt.Errorf("native create_relationship: unknown backend %q", backend)
	}
}

func createRelationshipPostgres(ctx context.Context, cfg *Config, source, target, rel string, strength float64, createdBy string, metadata map[string]any) error {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("create_relationship: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var metaJSON []byte
	if len(metadata) > 0 {
		metaJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("create_relationship: marshal: %w", err)
		}
	}
	// ON CONFLICT DO NOTHING honours the unique_relationship constraint
	// so repeat calls are idempotent.
	_, err = conn.Exec(ctx, `
INSERT INTO memory_relationships (source_id, target_id, relationship, strength, created_by, metadata)
VALUES ($1::uuid, $2::uuid, $3::relationship_type, $4, $5, COALESCE($6::jsonb, '{}'::jsonb))
ON CONFLICT ON CONSTRAINT unique_relationship DO NOTHING`,
		source, target, rel, strength, createdBy, metaJSON)
	if err != nil {
		return fmt.Errorf("create_relationship: insert: %w", err)
	}
	return nil
}

func createRelationshipSupabase(ctx context.Context, cfg *Config, source, target, rel string, strength float64, createdBy string, metadata map[string]any) error {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return err
	}
	body := map[string]any{
		"source_id":    source,
		"target_id":    target,
		"relationship": rel,
		"strength":     strength,
		"created_by":   createdBy,
	}
	if metadata != nil {
		body["metadata"] = metadata
	} else {
		body["metadata"] = map[string]any{}
	}
	// PostgREST upsert via resolution=merge-duplicates handles the
	// unique constraint idempotency the same way the pgx path does.
	_, err = client.postJSON(ctx, "/memory_relationships", body, map[string]string{
		"Prefer": "return=minimal,resolution=merge-duplicates",
	})
	return err
}

// -----------------------------------------------------------------------
// explore_knowledge

// GraphMemory is a memory row returned from graph-walk queries. The
// extra fields (Depth, Relationship, EdgeStrength, ConnectedFrom)
// describe how this row was reached -- depth 0 means direct seed match,
// depth > 0 means we traversed N edges to get here.
type GraphMemory struct {
	ID             string         `json:"id"`
	Content        string         `json:"content"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Source         string         `json:"source,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	Relevance      float64        `json:"relevance,omitempty"`
	Confidence     float64        `json:"confidence,omitempty"`
	Depth          int            `json:"depth"`
	Relationship   string         `json:"relationship,omitempty"`
	EdgeStrength   float64        `json:"edge_strength,omitempty"`
	ConnectedFrom  string         `json:"connected_from,omitempty"`
}

// ExploreOptions configures the graph-walk seed + traversal.
type ExploreOptions struct {
	Depth       int      // default 1
	MinStrength float64  // default 0.5
	Limit       int      // default 5
	Tags        []string // filter seed results
	Source      string   // filter seed results
	Profile     string
}

// ExploreKnowledge runs hybrid search for query, then walks the
// relationship graph Depth hops deep. Returns seed matches at depth 0
// and connected memories at depth 1+.
func ExploreKnowledge(ctx context.Context, cfg *Config, query string, opts ExploreOptions) ([]GraphMemory, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native explore_knowledge: nil config")
	}
	if query == "" {
		return nil, fmt.Errorf("native explore_knowledge: query is required")
	}
	profile := opts.Profile
	if profile == "" {
		profile = ActiveProfile(cfg)
	}
	depth := opts.Depth
	if depth < 0 {
		depth = 1
	}
	minStrength := opts.MinStrength
	if minStrength == 0 {
		minStrength = 0.5
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}

	// Graph walk needs a query embedding as the seed. Embed client-side
	// so we can cache through the shared embedder path.
	embedder, err := NewEmbedder(cfg)
	if err != nil {
		return nil, fmt.Errorf("native explore_knowledge: %w", err)
	}
	embedding, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("native explore_knowledge: embed: %w", err)
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return exploreKnowledgePostgres(ctx, cfg, query, embedding, profile, depth, minStrength, limit, opts.Tags, opts.Source)
	case "supabase":
		return exploreKnowledgeSupabase(ctx, cfg, query, embedding, profile, depth, minStrength, limit, opts.Tags, opts.Source)
	default:
		return nil, fmt.Errorf("native explore_knowledge: unknown backend %q", backend)
	}
}

func exploreKnowledgePostgres(ctx context.Context, cfg *Config, query string, embedding []float32, profile string, depth int, minStrength float64, limit int, tags []string, source string) ([]GraphMemory, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("explore_knowledge: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var sourceArg any
	if source != "" {
		sourceArg = source
	}
	// explore_memory_graph signature (see schema line 642):
	// (query_text, query_embedding, filter_profile, match_count,
	//  traversal_depth, min_strength, filter_tags, filter_source)
	rows, err := conn.Query(ctx, `
SELECT id::text, content, metadata, source, tags, relevance, depth,
       COALESCE(relationship, ''), COALESCE(edge_strength, 0.0),
       COALESCE(connected_from::text, '')
FROM explore_memory_graph($1, $2::vector, $3, $4, $5, $6, $7, $8)`,
		query, pgvectorLiteral(embedding), profile, limit, depth, minStrength, tags, sourceArg)
	if err != nil {
		return nil, fmt.Errorf("explore_knowledge: query: %w", err)
	}
	defer rows.Close()

	return scanGraphMemories(rows, true /* withRelevance */)
}

func exploreKnowledgeSupabase(ctx context.Context, cfg *Config, query string, embedding []float32, profile string, depth int, minStrength float64, limit int, tags []string, source string) ([]GraphMemory, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"query_text":       query,
		"query_embedding":  pgvectorLiteral(embedding),
		"filter_profile":   profile,
		"match_count":      limit,
		"traversal_depth":  depth,
		"min_strength":     minStrength,
	}
	if tags != nil {
		args["filter_tags"] = tags
	}
	if source != "" {
		args["filter_source"] = source
	}
	raw, err := client.callRPC(ctx, "explore_memory_graph", args)
	if err != nil {
		return nil, err
	}
	return parseGraphMemoriesJSON(raw)
}

// -----------------------------------------------------------------------
// find_related

// FindRelatedOptions tunes the graph walk from a known memory.
type FindRelatedOptions struct {
	Depth             int
	MinStrength       float64
	RelationshipTypes []string
	Limit             int
}

// FindRelated traverses the relationship graph from memory_id and
// returns reachable memories with depth + edge strength annotations.
func FindRelated(ctx context.Context, cfg *Config, memoryID string, opts FindRelatedOptions) ([]GraphMemory, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native find_related: nil config")
	}
	if memoryID == "" {
		return nil, fmt.Errorf("native find_related: memory_id required")
	}
	depth := opts.Depth
	if depth <= 0 {
		depth = 1
	}
	minStrength := opts.MinStrength
	if minStrength == 0 {
		minStrength = 0.5
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return findRelatedPostgres(ctx, cfg, memoryID, depth, minStrength, opts.RelationshipTypes, limit)
	case "supabase":
		return findRelatedSupabase(ctx, cfg, memoryID, depth, minStrength, opts.RelationshipTypes, limit)
	default:
		return nil, fmt.Errorf("native find_related: unknown backend %q", backend)
	}
}

func findRelatedPostgres(ctx context.Context, cfg *Config, memoryID string, depth int, minStrength float64, types []string, limit int) ([]GraphMemory, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("find_related: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// get_related_memories expects relationship_type[] (an enum array).
	// nil for "no filter"; otherwise a text array that postgres casts.
	var typesArg any
	if len(types) > 0 {
		typesArg = types // pgx will encode as text[]; schema casts.
	}
	rows, err := conn.Query(ctx, `
SELECT id::text, content, metadata, source, tags, confidence, depth,
       COALESCE(relationship, ''), COALESCE(edge_strength, 0.0),
       COALESCE(connected_from::text, '')
FROM get_related_memories($1::uuid, $2, $3, $4::relationship_type[], $5)`,
		memoryID, depth, minStrength, typesArg, limit)
	if err != nil {
		return nil, fmt.Errorf("find_related: query: %w", err)
	}
	defer rows.Close()

	return scanGraphMemories(rows, false /* confidence in place of relevance */)
}

func findRelatedSupabase(ctx context.Context, cfg *Config, memoryID string, depth int, minStrength float64, types []string, limit int) ([]GraphMemory, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"start_id":     memoryID,
		"max_depth":    depth,
		"min_strength": minStrength,
		"result_limit": limit,
	}
	if len(types) > 0 {
		args["filter_types"] = types
	}
	raw, err := client.callRPC(ctx, "get_related_memories", args)
	if err != nil {
		return nil, err
	}
	return parseGraphMemoriesJSON(raw)
}

// -----------------------------------------------------------------------
// suggest_connections

// ConnectionSuggestion is a memory that shares entities with a target
// but has no explicit relationship edge. Surfaces "hidden" connections
// through the entity graph.
type ConnectionSuggestion struct {
	ID             string    `json:"id"`
	Content        string    `json:"content"`
	SharedCount    int       `json:"shared_count"`
	SharedEntities []string  `json:"shared_entities"`
	CreatedAt      time.Time `json:"created_at"`
	Tags           []string  `json:"tags,omitempty"`
}

// SuggestConnections finds memories that share at least min_shared
// entities with memory_id but have no explicit relationship to it yet.
// Python-parity inline SQL: joins memory_entities against a target's
// entity set and excludes anything already in memory_relationships.
func SuggestConnections(ctx context.Context, cfg *Config, memoryID string, minShared, limit int) ([]ConnectionSuggestion, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native suggest_connections: nil config")
	}
	if memoryID == "" {
		return nil, fmt.Errorf("native suggest_connections: memory_id required")
	}
	if minShared <= 0 {
		minShared = 2
	}
	if limit <= 0 {
		limit = 10
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	// suggest_connections is postgres-only for now -- Supabase PostgREST
	// can't run arbitrary SQL and we'd need an RPC wrapper. Error
	// clearly so users know to route via the sidecar until the RPC
	// lands server-side.
	if backend != "postgres" {
		return nil, fmt.Errorf("native suggest_connections: postgres-only for now (route via sidecar on supabase; tracked for Batch E follow-up)")
	}
	return suggestConnectionsPostgres(ctx, cfg, memoryID, ActiveProfile(cfg), minShared, limit)
}

func suggestConnectionsPostgres(ctx context.Context, cfg *Config, memoryID, profile string, minShared, limit int) ([]ConnectionSuggestion, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("suggest_connections: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Recursive-CTE query lifted verbatim from Python's inline SQL
	// (memory.py suggest_connections). Keeping the same shape guarantees
	// identical results so users can round-trip between stacks.
	const q = `
WITH target_entities AS (
    SELECT entity_id FROM memory_entities
    WHERE memory_id = $1::uuid
),
shared AS (
    SELECT me.memory_id,
           count(*)::int AS shared_count,
           array_agg(e.entity_type || ':' || e.canonical_name) AS shared_entities
    FROM memory_entities me
    JOIN target_entities te ON te.entity_id = me.entity_id
    JOIN entities e ON e.id = me.entity_id
    WHERE me.memory_id != $1::uuid
      AND me.profile = $2
    GROUP BY me.memory_id
    HAVING count(*) >= $3
),
unlinked AS (
    SELECT s.*
    FROM shared s
    WHERE NOT EXISTS (
        SELECT 1 FROM memory_relationships mr
        WHERE (mr.source_id = $1::uuid AND mr.target_id = s.memory_id)
           OR (mr.target_id = $1::uuid AND mr.source_id = s.memory_id)
    )
)
SELECT u.memory_id::text, u.shared_count, u.shared_entities,
       m.content, m.created_at, m.tags
FROM unlinked u
JOIN memories m ON m.id = u.memory_id
WHERE m.expires_at IS NULL OR m.expires_at > now()
ORDER BY u.shared_count DESC, m.created_at DESC
LIMIT $4`

	rows, err := conn.Query(ctx, q, memoryID, profile, minShared, limit)
	if err != nil {
		return nil, fmt.Errorf("suggest_connections: query: %w", err)
	}
	defer rows.Close()

	var out []ConnectionSuggestion
	for rows.Next() {
		var s ConnectionSuggestion
		if err := rows.Scan(&s.ID, &s.SharedCount, &s.SharedEntities,
			&s.Content, &s.CreatedAt, &s.Tags); err != nil {
			return nil, fmt.Errorf("suggest_connections: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------
// scan helpers

// scanGraphMemories reads rows from either explore_memory_graph or
// get_related_memories. The former returns relevance; the latter
// returns confidence. withRelevance picks which column to populate.
func scanGraphMemories(rows pgx.Rows, withRelevance bool) ([]GraphMemory, error) {
	var out []GraphMemory
	for rows.Next() {
		var m GraphMemory
		var metaJSON []byte
		// source is nullable in the memories table, so scan into a
		// pointer and flatten on the way out. connFrom uses COALESCE in
		// the SQL so it's always a string.
		var source *string
		var connFrom string
		var scoreCol float64
		if err := rows.Scan(
			&m.ID, &m.Content, &metaJSON, &source, &m.Tags,
			&scoreCol, &m.Depth,
			&m.Relationship, &m.EdgeStrength, &connFrom); err != nil {
			return nil, fmt.Errorf("scan graph row: %w", err)
		}
		if source != nil {
			m.Source = *source
		}
		if withRelevance {
			m.Relevance = scoreCol
		} else {
			m.Confidence = scoreCol
		}
		if connFrom != "" {
			m.ConnectedFrom = connFrom
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &m.Metadata)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// parseGraphMemoriesJSON decodes the Supabase RPC response shape
// (JSON array of rows) into GraphMemory. The field names in the
// JSON match the RPC return-table column names.
func parseGraphMemoriesJSON(raw []byte) ([]GraphMemory, error) {
	var rows []struct {
		ID            string         `json:"id"`
		Content       string         `json:"content"`
		Metadata      map[string]any `json:"metadata,omitempty"`
		Source        string         `json:"source,omitempty"`
		Tags          []string       `json:"tags,omitempty"`
		Relevance     float64        `json:"relevance,omitempty"`
		Confidence    float64        `json:"confidence,omitempty"`
		Depth         int            `json:"depth"`
		Relationship  string         `json:"relationship,omitempty"`
		EdgeStrength  float64        `json:"edge_strength,omitempty"`
		ConnectedFrom string         `json:"connected_from,omitempty"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("graph: parse supabase response: %w", err)
	}
	out := make([]GraphMemory, 0, len(rows))
	for _, r := range rows {
		out = append(out, GraphMemory{
			ID: r.ID, Content: r.Content, Metadata: r.Metadata,
			Source: r.Source, Tags: r.Tags,
			Relevance: r.Relevance, Confidence: r.Confidence,
			Depth: r.Depth, Relationship: r.Relationship,
			EdgeStrength: r.EdgeStrength, ConnectedFrom: r.ConnectedFrom,
		})
	}
	return out, nil
}
