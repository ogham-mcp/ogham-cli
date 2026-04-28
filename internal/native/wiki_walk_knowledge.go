// Package native: walk_knowledge -- direction-aware graph walk over
// memory_relationships. Mirrors src/ogham/database.py::walk_memory_graph
// and the wiki_walk_graph RPC (migration 031). SQL-only port (recursive
// CTE on the server side); no LLM, no embedding round-trip.
//
// Direction: "outgoing" follows source_id -> target_id, "incoming"
// follows target_id -> source_id, "both" walks either edge. The CTE
// tracks a visited[] path so dense graphs at depth=5 don't blow up via
// cycles or diamond shapes.
//
// This is the recall fast path -- a Lambda thin client can pull a graph
// neighbourhood in one DB round-trip.

package native

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// WalkKnowledgeOptions tunes the recursive walk. Defaults match the
// Python wiki tool layer.
type WalkKnowledgeOptions struct {
	// Depth is the maximum number of edges to traverse. Server clamps to
	// 0..5 (anything beyond explodes the rowset on dense profiles). 0
	// means "no walk" -- you'd just get the seed node back.
	Depth int

	// Direction is "outgoing", "incoming", or "both". Empty -> "both".
	Direction string

	// MinStrength filters edges by strength (0..1). Default 0.0 = keep all.
	MinStrength float64

	// RelationshipTypes optionally restricts to a set of relationship
	// labels (e.g. ["depends_on", "cites"]). Empty -> no filter.
	RelationshipTypes []string

	// Limit caps the result rowset. Default 50 mirrors Python.
	Limit int
}

// GraphNode is a row returned from wiki_walk_graph. Carries the path
// metadata (depth, edge strength, direction) alongside the memory's own
// fields. Distinct from GraphMemory (used by hybrid-search-driven graph
// walks) because the wiki walk has direction_used, no relevance, and
// confidence is canonically present.
type GraphNode struct {
	ID            string         `json:"id"`
	Content       string         `json:"content"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Source        string         `json:"source,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Confidence    float64        `json:"confidence,omitempty"`
	Depth         int            `json:"depth"`
	Relationship  string         `json:"relationship,omitempty"`
	EdgeStrength  float64        `json:"edge_strength,omitempty"`
	ConnectedFrom string         `json:"connected_from,omitempty"`
	DirectionUsed string         `json:"direction_used,omitempty"`
}

// WalkKnowledgeResult mirrors the Python tool wrapper:
//
//	{ start_id, depth, direction, node_count, nodes }
type WalkKnowledgeResult struct {
	StartID   string      `json:"start_id"`
	Depth     int         `json:"depth"`
	Direction string      `json:"direction"`
	NodeCount int         `json:"node_count"`
	Nodes     []GraphNode `json:"nodes"`
}

// WalkKnowledge runs the recursive graph walk from startID. Returns the
// flattened set of reachable memories with depth/edge metadata.
func WalkKnowledge(ctx context.Context, cfg *Config, startID string, opts WalkKnowledgeOptions) (*WalkKnowledgeResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native walk_knowledge: nil config")
	}
	if startID == "" {
		return nil, fmt.Errorf("native walk_knowledge: start_id is required")
	}

	// Defaults match the Python wiki_walk_graph contract.
	depth := opts.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 5 {
		// Mirror the server-side guard so a Go caller learns about the
		// cap at the client boundary instead of getting a SQL error.
		return nil, fmt.Errorf("native walk_knowledge: depth must be 0..5, got %d", depth)
	}
	direction := opts.Direction
	if direction == "" {
		direction = "both"
	}
	switch direction {
	case "outgoing", "incoming", "both":
		// ok
	default:
		return nil, fmt.Errorf("native walk_knowledge: direction must be outgoing/incoming/both, got %q", direction)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	var nodes []GraphNode
	switch backend {
	case "postgres":
		nodes, err = walkKnowledgePostgres(ctx, cfg, startID, depth, direction, opts.MinStrength, opts.RelationshipTypes, limit)
	case "supabase":
		nodes, err = walkKnowledgeSupabase(ctx, cfg, startID, depth, direction, opts.MinStrength, opts.RelationshipTypes, limit)
	default:
		return nil, fmt.Errorf("native walk_knowledge: unknown backend %q", backend)
	}
	if err != nil {
		return nil, err
	}
	return &WalkKnowledgeResult{
		StartID:   startID,
		Depth:     depth,
		Direction: direction,
		NodeCount: len(nodes),
		Nodes:     nodes,
	}, nil
}

func walkKnowledgePostgres(ctx context.Context, cfg *Config, startID string, depth int, direction string, minStrength float64, types []string, limit int) ([]GraphNode, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("walk_knowledge: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var typesArg any
	if len(types) > 0 {
		typesArg = types
	}

	rows, err := conn.Query(ctx, `
SELECT id::text, content, metadata, source, tags, confidence, depth,
       COALESCE(relationship, ''),
       COALESCE(edge_strength, 0.0),
       COALESCE(connected_from::text, ''),
       COALESCE(direction_used, '')
FROM wiki_walk_graph($1::uuid, $2, $3, $4, $5::text[], $6)`,
		startID, depth, direction, minStrength, typesArg, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("walk_knowledge: query: %w", err)
	}
	defer rows.Close()

	out := make([]GraphNode, 0, limit)
	for rows.Next() {
		var (
			n            GraphNode
			metadataJSON []byte
			tags         []string
		)
		if err := rows.Scan(
			&n.ID, &n.Content, &metadataJSON, &n.Source, &tags, &n.Confidence,
			&n.Depth, &n.Relationship, &n.EdgeStrength,
			&n.ConnectedFrom, &n.DirectionUsed,
		); err != nil {
			return nil, fmt.Errorf("walk_knowledge: scan: %w", err)
		}
		if len(metadataJSON) > 0 {
			var meta map[string]any
			if err := json.Unmarshal(metadataJSON, &meta); err == nil {
				n.Metadata = meta
			}
		}
		n.Tags = tags
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walk_knowledge: rows: %w", err)
	}
	return out, nil
}

func walkKnowledgeSupabase(ctx context.Context, cfg *Config, startID string, depth int, direction string, minStrength float64, types []string, limit int) ([]GraphNode, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"p_start_id":     startID,
		"p_max_depth":    depth,
		"p_direction":    direction,
		"p_min_strength": minStrength,
		"p_result_limit": limit,
	}
	if len(types) > 0 {
		args["p_relationship_types"] = types
	}
	raw, err := client.callRPC(ctx, "wiki_walk_graph", args)
	if err != nil {
		return nil, err
	}
	var rows []supabaseGraphRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("walk_knowledge: parse RPC response: %w (body: %s)", err, truncateForError(raw))
	}
	out := make([]GraphNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toGraphNode())
	}
	return out, nil
}

// supabaseGraphRow mirrors the JSON shape PostgREST returns for one
// wiki_walk_graph row.
type supabaseGraphRow struct {
	ID            string         `json:"id"`
	Content       string         `json:"content"`
	Metadata      map[string]any `json:"metadata"`
	Source        string         `json:"source"`
	Tags          []string       `json:"tags"`
	Confidence    float64        `json:"confidence"`
	Depth         int            `json:"depth"`
	Relationship  *string        `json:"relationship"`
	EdgeStrength  *float64       `json:"edge_strength"`
	ConnectedFrom *string        `json:"connected_from"`
	DirectionUsed *string        `json:"direction_used"`
}

func (r supabaseGraphRow) toGraphNode() GraphNode {
	gn := GraphNode{
		ID:         r.ID,
		Content:    r.Content,
		Metadata:   r.Metadata,
		Source:     r.Source,
		Tags:       r.Tags,
		Confidence: r.Confidence,
		Depth:      r.Depth,
	}
	if r.Relationship != nil {
		gn.Relationship = *r.Relationship
	}
	if r.EdgeStrength != nil {
		gn.EdgeStrength = *r.EdgeStrength
	}
	if r.ConnectedFrom != nil {
		gn.ConnectedFrom = *r.ConnectedFrom
	}
	if r.DirectionUsed != nil {
		gn.DirectionUsed = *r.DirectionUsed
	}
	return gn
}

