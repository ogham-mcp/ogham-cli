package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Stats is a lightweight summary of the active profile. Narrower than
// Python's get_stats (which also includes decay counters, orphan counts,
// cache stats, and configuration echo) -- the CLI uses of stats are
// "how many memories", "what's my top sources/tags", and "am I on the
// right profile", so this covers the headline numbers without the
// heavier aggregations.
type Stats struct {
	Profile  string      `json:"profile"`
	Total    int64       `json:"total"`
	Sources  []Breakdown `json:"sources"`
	TopTags  []Breakdown `json:"top_tags"`
	Untagged int64       `json:"untagged_count"`
	WithTTL  int64       `json:"with_ttl_count"`
	Expiring int64       `json:"expiring_within_7d"`

	// ConnectedPct is the percentage (0-100) of active memories that
	// appear as either source_id or target_id in memory_relationships.
	// Answers "how much of my graph is wired up?" -- a profile at 5%
	// connected is effectively a flat list; one at 60%+ is a real graph.
	// When Total is 0 we return 0 (no rows, no percentage to compute).
	ConnectedPct float64 `json:"connected_pct"`

	// DecayCount is the number of active memories whose confidence has
	// fallen below DecayThreshold. Confidence decays over time in the
	// schema (see memories.confidence + the nightly decay job); memories
	// below the floor are nearly invisible to hybrid_search because the
	// relevance formula multiplies by confidence. Surfacing this as a
	// headline number lets operators spot profiles that need pruning or
	// reinforcement.
	DecayCount int64 `json:"decay_count"`
}

// DecayThreshold is the confidence floor below which a memory is
// considered "decayed" for the DecayCount stat. 0.25 sits well below
// the schema default of 0.5 and is empirically near-invisible in
// hybrid_search ranking -- memories that reach it are candidates for
// cleanup or reinforcement.
const DecayThreshold = 0.25

type Breakdown struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// GetStats aggregates counts for the active profile. Picks backend from
// cfg.ResolveBackend().
func GetStats(ctx context.Context, cfg *Config) (*Stats, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native stats: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native stats: %w", err)
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "default"
	}
	switch backend {
	case "postgres":
		return statsPostgres(ctx, cfg, profile)
	case "supabase":
		return statsSupabase(ctx, cfg, profile)
	default:
		return nil, fmt.Errorf("native stats: unknown backend %q", backend)
	}
}

func statsPostgres(ctx context.Context, cfg *Config, profile string) (*Stats, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native stats: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	s := &Stats{Profile: profile}

	// Total + untagged + with-ttl + expiring -- one trip via a CTE so
	// the connection is held for a single statement.
	err = conn.QueryRow(ctx, `
		WITH m AS (
		  SELECT * FROM memories
		  WHERE profile = $1 AND (expires_at IS NULL OR expires_at > now())
		)
		SELECT
		  count(*),
		  count(*) FILTER (WHERE tags IS NULL OR cardinality(tags) = 0),
		  count(*) FILTER (WHERE expires_at IS NOT NULL),
		  count(*) FILTER (WHERE expires_at IS NOT NULL AND expires_at < now() + interval '7 days')
		FROM m`,
		profile).Scan(&s.Total, &s.Untagged, &s.WithTTL, &s.Expiring)
	if err != nil {
		return nil, fmt.Errorf("native stats: counts: %w", err)
	}

	s.Sources, err = topBreakdown(ctx, conn, `
		SELECT coalesce(source, '(unknown)') AS name, count(*) AS count
		FROM memories
		WHERE profile = $1 AND (expires_at IS NULL OR expires_at > now())
		GROUP BY source
		ORDER BY count DESC
		LIMIT 10`, profile)
	if err != nil {
		return nil, err
	}

	s.TopTags, err = topBreakdown(ctx, conn, `
		SELECT tag AS name, count(*) AS count
		FROM memories, unnest(tags) AS tag
		WHERE profile = $1 AND (expires_at IS NULL OR expires_at > now())
		GROUP BY tag
		ORDER BY count DESC
		LIMIT 10`, profile)
	if err != nil {
		return nil, err
	}

	// ConnectedPct: fraction of active memories touched by at least one
	// relationship row (as source or target). UNION DISTINCT counts each
	// memory once even if it's wired into many edges.
	//
	// Guarding on Total == 0 avoids a div-by-zero: we short-circuit to 0
	// and skip the query on a fresh/empty profile.
	if s.Total > 0 {
		var connected int64
		err = conn.QueryRow(ctx, `
			WITH connected_ids AS (
			  SELECT mr.source_id AS id
			  FROM memory_relationships mr
			  JOIN memories m ON m.id = mr.source_id
			  WHERE m.profile = $1 AND (m.expires_at IS NULL OR m.expires_at > now())
			  UNION
			  SELECT mr.target_id AS id
			  FROM memory_relationships mr
			  JOIN memories m ON m.id = mr.target_id
			  WHERE m.profile = $1 AND (m.expires_at IS NULL OR m.expires_at > now())
			)
			SELECT count(*) FROM connected_ids`,
			profile).Scan(&connected)
		if err != nil {
			return nil, fmt.Errorf("native stats: connected: %w", err)
		}
		s.ConnectedPct = float64(connected) * 100.0 / float64(s.Total)
	}

	// DecayCount: count of active memories below the confidence floor.
	// The schema.sql we ship always has memories.confidence (float
	// default 0.5), so we query it directly without a column-existence
	// probe. If a future fork removes the column, the query will surface
	// a clear "column does not exist" error rather than silently drift.
	err = conn.QueryRow(ctx, `
		SELECT count(*) FROM memories
		WHERE profile = $1
		  AND (expires_at IS NULL OR expires_at > now())
		  AND confidence < $2`,
		profile, DecayThreshold).Scan(&s.DecayCount)
	if err != nil {
		return nil, fmt.Errorf("native stats: decay: %w", err)
	}

	return s, nil
}

func topBreakdown(ctx context.Context, conn *pgx.Conn, sql, profile string) ([]Breakdown, error) {
	rows, err := conn.Query(ctx, sql, profile)
	if err != nil {
		return nil, fmt.Errorf("native stats: top query: %w", err)
	}
	defer rows.Close()
	var out []Breakdown
	for rows.Next() {
		var b Breakdown
		if err := rows.Scan(&b.Name, &b.Count); err != nil {
			return nil, fmt.Errorf("native stats: scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// statsSupabase aggregates via PostgREST. No RPC for this, so we pull a
// capped sample of rows and aggregate client-side. 1000 is the default
// PostgREST limit and covers the "top 10 sources/tags" question with
// enough signal for typical profiles. For very large profiles (>10k
// memories) prefer the direct Postgres backend for accurate counts.
func statsSupabase(ctx context.Context, cfg *Config, profile string) (*Stats, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("select", "id,source,tags,expires_at,confidence")
	q.Set("profile", "eq."+profile)
	q.Set("or", "(expires_at.is.null,expires_at.gt.now())")
	q.Set("limit", "1000")
	endpoint := client.baseURL + "/memories?" + q.Encode()
	raw, err := client.getJSON(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID         string   `json:"id"`
		Source     *string  `json:"source"`
		Tags       []string `json:"tags"`
		ExpiresAt  *string  `json:"expires_at"`
		Confidence *float64 `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native stats: parse: %w (body: %s)", err, truncateForError(raw))
	}

	s := &Stats{Profile: profile, Total: int64(len(rows))}
	sources := map[string]int64{}
	tags := map[string]int64{}
	activeIDs := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		src := "(unknown)"
		if r.Source != nil && *r.Source != "" {
			src = *r.Source
		}
		sources[src]++
		if len(r.Tags) == 0 {
			s.Untagged++
		}
		for _, t := range r.Tags {
			tags[t]++
		}
		if r.ExpiresAt != nil {
			s.WithTTL++
		}
		if r.Confidence != nil && *r.Confidence < DecayThreshold {
			s.DecayCount++
		}
		if r.ID != "" {
			activeIDs[r.ID] = struct{}{}
		}
	}
	s.Sources = topN(sources, 10)
	s.TopTags = topN(tags, 10)

	// ConnectedPct: pull the profile's relationship edges via PostgREST
	// and count distinct source/target ids that land in the active set.
	// Same 1000-row cap as the memories query -- parity with the "for
	// very large profiles prefer direct Postgres" caveat above.
	if len(activeIDs) > 0 {
		connected, err := connectedCountSupabase(ctx, client, activeIDs)
		if err != nil {
			return nil, err
		}
		s.ConnectedPct = float64(connected) * 100.0 / float64(s.Total)
	}
	return s, nil
}

// connectedCountSupabase returns the number of ids in active that appear
// as either source_id or target_id in memory_relationships. PostgREST has
// no JOIN primitive, so we filter edges to the active set in two passes
// and union the result client-side.
func connectedCountSupabase(ctx context.Context, client *supabaseClient, active map[string]struct{}) (int64, error) {
	// Build the PostgREST `in.(id1,id2,...)` filter once and reuse it
	// for both passes.
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	inFilter := "in.(" + strings.Join(ids, ",") + ")"

	touched := make(map[string]struct{}, len(active))
	for _, col := range []string{"source_id", "target_id"} {
		q := url.Values{}
		q.Set("select", col)
		q.Set(col, inFilter)
		q.Set("limit", "1000")
		endpoint := client.baseURL + "/memory_relationships?" + q.Encode()
		raw, err := client.getJSON(ctx, endpoint)
		if err != nil {
			return 0, err
		}
		var rows []map[string]string
		if err := json.Unmarshal(raw, &rows); err != nil {
			return 0, fmt.Errorf("native stats: parse relationships: %w (body: %s)", err, truncateForError(raw))
		}
		for _, r := range rows {
			if id, ok := r[col]; ok {
				touched[id] = struct{}{}
			}
		}
	}
	return int64(len(touched)), nil
}

func topN(m map[string]int64, n int) []Breakdown {
	out := make([]Breakdown, 0, len(m))
	for k, v := range m {
		out = append(out, Breakdown{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
