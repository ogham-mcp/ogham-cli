package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

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
}

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
	q.Set("select", "source,tags,expires_at")
	q.Set("profile", "eq."+profile)
	q.Set("or", "(expires_at.is.null,expires_at.gt.now())")
	q.Set("limit", "1000")
	endpoint := client.baseURL + "/memories?" + q.Encode()
	raw, err := client.getJSON(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Source    *string  `json:"source"`
		Tags      []string `json:"tags"`
		ExpiresAt *string  `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native stats: parse: %w (body: %s)", err, truncateForError(raw))
	}

	s := &Stats{Profile: profile, Total: int64(len(rows))}
	sources := map[string]int64{}
	tags := map[string]int64{}
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
	}
	s.Sources = topN(sources, 10)
	s.TopTags = topN(tags, 10)
	return s, nil
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
