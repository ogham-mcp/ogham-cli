package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

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

// statsSupabase aggregates via PostgREST. The headline scalars (Total,
// Untagged, WithTTL, Expiring, DecayCount) come from HEAD + Prefer:
// count=exact round-trips -- reading len(response_array) would silently
// cap at managed Supabase's 1000-row per-request ceiling, which is what
// produced the "1,000 memories" dashboard bug that motivated this file.
// The top-N source / tag aggregates + the active-id set for ConnectedPct
// still need row data; we page through in 1000-row chunks via Range.
func statsSupabase(ctx context.Context, cfg *Config, profile string) (*Stats, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}

	// Shared filter set: profile + active (not-expired) predicate mirrors
	// the Postgres CTE in statsPostgres. PostgREST composes `or=(...)`
	// correctly when URL-escaped, so keep it raw here.
	activeFilter := func() url.Values {
		v := url.Values{}
		v.Set("profile", "eq."+profile)
		v.Set("or", "(expires_at.is.null,expires_at.gt.now())")
		return v
	}

	s := &Stats{Profile: profile}

	// Total active memories -- HEAD with count=exact. This is the headline
	// number rendered as the Overview "Memories" card.
	total, err := client.headCountExact(ctx, "memories", activeFilter())
	if err != nil {
		return nil, fmt.Errorf("native stats: total: %w", err)
	}
	s.Total = total

	// Untagged: active memories with tags IS NULL OR tags = {}. PostgREST's
	// `is.null` handles NULL; `eq.{}` matches the empty text[] literal.
	// Wrapping in or= preserves the active filter at top-level.
	untaggedFilter := activeFilter()
	untaggedFilter.Set("tags", "is.null")
	if n, err := client.headCountExact(ctx, "memories", untaggedFilter); err != nil {
		return nil, fmt.Errorf("native stats: untagged: %w", err)
	} else {
		s.Untagged = n
	}
	// Add the "tags = {}" bucket: PostgREST filters compose as AND, so we
	// issue a second HEAD and sum. Splitting into two keeps the filter
	// string simple -- a single `or=(tags.is.null,tags.eq.{})` inside the
	// existing `or=(expires_at...)` would require nested-or escaping.
	emptyTagsFilter := activeFilter()
	emptyTagsFilter.Set("tags", "eq.{}")
	if n, err := client.headCountExact(ctx, "memories", emptyTagsFilter); err != nil {
		return nil, fmt.Errorf("native stats: empty-tags: %w", err)
	} else {
		s.Untagged += n
	}

	// WithTTL: any expires_at set (NULL excluded).
	ttlFilter := activeFilter()
	ttlFilter.Set("expires_at", "not.is.null")
	if n, err := client.headCountExact(ctx, "memories", ttlFilter); err != nil {
		return nil, fmt.Errorf("native stats: with-ttl: %w", err)
	} else {
		s.WithTTL = n
	}

	// Expiring within 7 days: TTL set AND expires_at < now+7d.
	expiringFilter := activeFilter()
	expiringFilter.Set("expires_at", "not.is.null")
	expiringFilter.Add("expires_at", "lt."+time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339))
	if n, err := client.headCountExact(ctx, "memories", expiringFilter); err != nil {
		return nil, fmt.Errorf("native stats: expiring: %w", err)
	} else {
		s.Expiring = n
	}

	// DecayCount: active + confidence < floor. nil confidence must NOT
	// count as decayed (absence is not decay) -- `lt.` in PostgREST does
	// NOT match NULL, so the semantics match the Postgres path by default.
	decayFilter := activeFilter()
	decayFilter.Set("confidence", fmt.Sprintf("lt.%v", DecayThreshold))
	if n, err := client.headCountExact(ctx, "memories", decayFilter); err != nil {
		return nil, fmt.Errorf("native stats: decay: %w", err)
	} else {
		s.DecayCount = n
	}

	// Top-N sources + tags: still need the row data. Page in 1000-row
	// chunks via Range so we cover the full active set (managed Supabase
	// caps a single GET at 1000 rows regardless of explicit ?limit=).
	// Cap the scan at 50 pages (50k rows) to bound worst-case latency --
	// beyond that, prefer the direct Postgres backend.
	const rowPageSize = 1000
	const rowMaxPages = 50

	sources := map[string]int64{}
	tags := map[string]int64{}
	activeIDs := make(map[string]struct{}, int(s.Total))

	baseQ := activeFilter()
	baseQ.Set("select", "id,source,tags")
	baseQ.Set("order", "id.asc") // deterministic paging
	endpoint := client.baseURL + "/memories?" + baseQ.Encode()

	for page := 0; page < rowMaxPages; page++ {
		start := page * rowPageSize
		raw, err := client.getJSONRange(ctx, endpoint, start, start+rowPageSize-1)
		if err != nil {
			return nil, fmt.Errorf("native stats: rows page %d: %w", page, err)
		}
		var rows []struct {
			ID     string   `json:"id"`
			Source *string  `json:"source"`
			Tags   []string `json:"tags"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("native stats: parse: %w (body: %s)", err, truncateForError(raw))
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			src := "(unknown)"
			if r.Source != nil && *r.Source != "" {
				src = *r.Source
			}
			sources[src]++
			for _, t := range r.Tags {
				tags[t]++
			}
			if r.ID != "" {
				activeIDs[r.ID] = struct{}{}
			}
		}
		if len(rows) < rowPageSize {
			break
		}
	}
	s.Sources = topN(sources, 10)
	s.TopTags = topN(tags, 10)

	// ConnectedPct: intersect the paginated relationships table with the
	// full active-id set. Now that activeIDs covers every active memory
	// (not just the first 1000), the numerator is accurate on profiles
	// that grow past the per-request cap.
	if len(activeIDs) > 0 && s.Total > 0 {
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
// no JOIN primitive, and stuffing the whole active-id set into an
// `in.(…)` query-string filter blows past Supabase's ~16 KB URL cap once
// the corpus exceeds a few hundred memories. Instead we page through the
// unfiltered relationship table and intersect against the active set
// client-side. For corpuses with >50k relationships this would need a
// stored function on the server side; the prototype caps at 50k edges
// which comfortably covers the current managed-tier working ceiling.
func connectedCountSupabase(ctx context.Context, client *supabaseClient, active map[string]struct{}) (int64, error) {
	const pageSize = 1000
	const maxPages = 50 // 50 000 edges ceiling for the prototype

	touched := make(map[string]struct{}, len(active))
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("select", "source_id,target_id")
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(page*pageSize))
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
			if id, ok := r["source_id"]; ok {
				if _, inActive := active[id]; inActive {
					touched[id] = struct{}{}
				}
			}
			if id, ok := r["target_id"]; ok {
				if _, inActive := active[id]; inActive {
					touched[id] = struct{}{}
				}
			}
		}
		if len(rows) < pageSize {
			// no more pages
			break
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

// DayCount is a single cell in the Calendar heatmap: "on this UTC day,
// the active profile stored N memories." Day is always UTC-normalised
// to midnight so the dashboard renderer doesn't have to worry about
// server-local-tz drift.
type DayCount struct {
	Day   time.Time `json:"day"`
	Count int64     `json:"count"`
}

// StoreCountsByDay returns per-day memory counts for the active profile
// covering the trailing `days` calendar days (inclusive of today). Zero
// or missing days are NOT filled in -- the Calendar renderer walks a
// complete 365-day grid and overlays the returned counts via a map
// lookup, defaulting missing days to zero.
//
// days <= 0 is treated as 365 (the Calendar heatmap default).
func StoreCountsByDay(ctx context.Context, cfg *Config, days int) ([]DayCount, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native daycounts: nil config")
	}
	if days <= 0 {
		days = 365
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native daycounts: %w", err)
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "default"
	}
	switch backend {
	case "postgres":
		return storeCountsByDayPostgres(ctx, cfg, profile, days)
	case "supabase":
		return storeCountsByDaySupabase(ctx, cfg, profile, days)
	default:
		return nil, fmt.Errorf("native daycounts: unknown backend %q", backend)
	}
}

func storeCountsByDayPostgres(ctx context.Context, cfg *Config, profile string, days int) ([]DayCount, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native daycounts: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// date_trunc('day', ...) at UTC so the bucket boundary is stable
	// regardless of the server's TimeZone setting. Casting the interval
	// through make_interval keeps the parameter typed.
	sql := `
SELECT date_trunc('day', created_at AT TIME ZONE 'UTC') AS day, count(*) AS cnt
FROM memories
WHERE profile = $1
  AND (expires_at IS NULL OR expires_at > now())
  AND created_at >= (now() AT TIME ZONE 'UTC') - make_interval(days => $2)
GROUP BY 1
ORDER BY 1`
	rows, err := conn.Query(ctx, sql, profile, days)
	if err != nil {
		return nil, fmt.Errorf("native daycounts: query: %w", err)
	}
	defer rows.Close()
	var out []DayCount
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, fmt.Errorf("native daycounts: scan: %w", err)
		}
		// Strip any timezone on the Go side -- date_trunc returns
		// timestamp without time zone but pgx scans to the session
		// location. UTC the Day so callers can compare by equality.
		d.Day = d.Day.UTC().Truncate(24 * time.Hour)
		out = append(out, d)
	}
	return out, rows.Err()
}

func storeCountsByDaySupabase(ctx context.Context, cfg *Config, profile string, days int) ([]DayCount, error) {
	// PostgREST can't GROUP BY, so we pull the created_at column for the
	// last `days` days and bucket client-side. Managed Supabase caps each
	// GET at 1000 rows regardless of explicit ?limit=, so an explicit
	// ?limit=50000 silently truncates at 1000 -- that is the root cause of
	// the "1000 memories across N days" Calendar bug. Page via the Range
	// request header until a short page signals end-of-set.
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Truncate(24 * time.Hour)

	q := url.Values{}
	q.Set("select", "created_at")
	q.Set("profile", "eq."+profile)
	q.Add("created_at", "gte."+cutoff.Format(time.RFC3339))
	q.Set("or", "(expires_at.is.null,expires_at.gt.now())")
	q.Set("order", "created_at.asc")

	endpoint := client.baseURL + "/memories?" + q.Encode()

	const pageSize = 1000
	const maxPages = 100 // 100k rows -- one year at 3 memories/minute sustained

	buckets := map[int64]int64{}
	for page := 0; page < maxPages; page++ {
		start := page * pageSize
		raw, err := client.getJSONRange(ctx, endpoint, start, start+pageSize-1)
		if err != nil {
			return nil, fmt.Errorf("native daycounts: page %d: %w", page, err)
		}
		var rows []struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("native daycounts: parse: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			day := r.CreatedAt.UTC().Truncate(24 * time.Hour)
			buckets[day.Unix()]++
		}
		if len(rows) < pageSize {
			break
		}
	}

	out := make([]DayCount, 0, len(buckets))
	for ts, c := range buckets {
		out = append(out, DayCount{Day: time.Unix(ts, 0).UTC(), Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out, nil
}
