package native

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SearchResult mirrors the column set returned by hybrid_search_memories.
// Narrower projection than the memories table -- only what the CLI surfaces.
type SearchResult struct {
	ID          string    `json:"id"`
	Content     string    `json:"content"`
	Source      string    `json:"source,omitempty"`
	Profile     string    `json:"profile"`
	Tags        []string  `json:"tags"`
	Similarity  float64   `json:"similarity"`
	KeywordRank float64   `json:"keyword_rank"`
	Relevance   float64   `json:"relevance"`
	CreatedAt   time.Time `json:"created_at"`
}

// SearchOptions captures the optional arguments hybrid_search_memories
// accepts. Nil slices mean "no filter". Zero values mean "use RPC default".
type SearchOptions struct {
	Limit      int
	Tags       []string
	Source     string
	Profile    string // single-profile override; empty => use cfg.Profile
	Profiles   []string
	EntityTags []string
	// RecencyDecay > 0 tilts scoring toward newer memories. 0 = no decay.
	RecencyDecay float64
}

// Search runs hybrid_search_memories using whichever backend is configured:
//   - "supabase": PostgREST RPC call (no direct DB connection needed)
//   - "postgres": pgx direct connection to Postgres
//
// Both paths embed the query first via the configured provider.
func Search(ctx context.Context, cfg *Config, query string, opts SearchOptions) ([]SearchResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native search: nil config")
	}
	if query = strings.TrimSpace(query); query == "" {
		return nil, fmt.Errorf("native search: empty query")
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native search: %w", err)
	}

	switch backend {
	case "supabase":
		return searchSupabase(ctx, cfg, query, opts)
	case "postgres":
		return searchPostgres(ctx, cfg, query, opts)
	default:
		return nil, fmt.Errorf("native search: unknown backend %q (expected supabase or postgres)", backend)
	}
}

// searchPostgres is the direct-pgx path. Kept as a named function so the
// backend-switch in Search is easy to scan.
func searchPostgres(ctx context.Context, cfg *Config, query string, opts SearchOptions) ([]SearchResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	profile := opts.Profile
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}

	embedder, err := NewEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	embedding, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("native search: embed: %w", err)
	}

	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native search: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	return runHybridSearch(ctx, conn, query, embedding, profile, limit, opts)
}

// runHybridSearch is factored out of Search so tests can exercise the SQL
// layer with a real *pgx.Conn (via pgxmock or a live test DB) without
// also going through the Gemini HTTP path.
func runHybridSearch(
	ctx context.Context,
	conn *pgx.Conn,
	query string,
	embedding []float32,
	profile string,
	limit int,
	opts SearchOptions,
) ([]SearchResult, error) {
	embeddingLiteral := pgvectorLiteral(embedding)

	// Mirror Python's call signature exactly. Hardcoded RRF weights (0.3 /
	// 0.7) and rrf_k (10) match the Python tool defaults so the two paths
	// behave identically.
	const sql = `
SELECT id::text, content, coalesce(source, ''), profile, coalesce(tags, '{}'::text[]),
       similarity, keyword_rank, relevance, created_at
FROM hybrid_search_memories(
    $1::text,          -- query_text
    $2::vector,        -- query_embedding
    $3::integer,       -- match_count
    $4::text,          -- filter_profile
    $5::text[],        -- filter_tags
    $6::text,          -- filter_source
    0.3::float,        -- full_text_weight
    0.7::float,        -- semantic_weight
    10::integer,       -- rrf_k
    $7::text[],        -- filter_profiles
    $8::text[],        -- query_entity_tags
    $9::float          -- recency_decay
)`

	var filterSource any
	if opts.Source != "" {
		filterSource = opts.Source
	}

	rows, err := conn.Query(ctx, sql,
		query,
		embeddingLiteral,
		limit,
		profile,
		nullableStringSlice(opts.Tags),
		filterSource,
		nullableStringSlice(opts.Profiles),
		nullableStringSlice(opts.EntityTags),
		opts.RecencyDecay,
	)
	if err != nil {
		return nil, fmt.Errorf("native search: query: %w", err)
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(
			&r.ID, &r.Content, &r.Source, &r.Profile, &r.Tags,
			&r.Similarity, &r.KeywordRank, &r.Relevance, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("native search: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native search: rows: %w", err)
	}
	return out, nil
}

// pgvectorLiteral formats a []float32 into the text representation pgvector
// accepts: '[0.1,0.2,0.3]'. Using a text literal (cast to ::vector in SQL)
// avoids pulling in a pgvector-specific binary codec.
func pgvectorLiteral(v []float32) string {
	var sb strings.Builder
	sb.Grow(len(v)*8 + 2)
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

// nullableStringSlice turns an empty slice into nil so the driver binds
// SQL NULL (which the function defaults handle). Passing an empty text[]
// would match zero rows under `tags && filter_tags`.
func nullableStringSlice(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}
