// Package native lifecycle: read-only pipeline counts + decay factor.
// Writes (advance_stages, open_editing_window, strengthen_edges) remain
// Python-owned; the Go CLI only displays state for the dashboard.

package native

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Stage is a memory's current lifecycle stage.
type Stage string

const (
	StageFresh   Stage = "fresh"
	StageStable  Stage = "stable"
	StageEditing Stage = "editing"
)

// PipelineCounts returns the {stage: count} distribution for the dashboard
// Lifecycle card. Read-only -- does not advance anything.
//
// Mixed-version safe: if the memory_lifecycle table is absent (pre-026
// DB), returns all rows under 'fresh' without error.
func PipelineCounts(ctx context.Context, cfg *Config, profile string) (map[string]int64, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native lifecycle: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native lifecycle: %w", err)
	}
	switch backend {
	case "postgres":
		return pipelineCountsPostgres(ctx, cfg, profile)
	case "supabase":
		return pipelineCountsSupabase(ctx, cfg, profile)
	default:
		return nil, fmt.Errorf("native lifecycle: unknown backend %q", backend)
	}
}

func pipelineCountsPostgres(ctx context.Context, cfg *Config, profile string) (map[string]int64, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native lifecycle: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	out := map[string]int64{"fresh": 0, "stable": 0, "editing": 0}

	var hasLifecycle bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = 'memory_lifecycle'
		)
	`).Scan(&hasLifecycle); err != nil {
		return nil, fmt.Errorf("native lifecycle: probe table: %w", err)
	}

	if !hasLifecycle {
		// Pre-migration DB -- everything is implicitly 'fresh'.
		var total int64
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM memories WHERE profile = $1`, profile,
		).Scan(&total); err != nil {
			return nil, fmt.Errorf("native lifecycle: fallback count: %w", err)
		}
		out["fresh"] = total
		return out, nil
	}

	rows, err := conn.Query(ctx, `
		SELECT stage, count(*)::bigint
		  FROM memory_lifecycle
		 WHERE profile = $1
		 GROUP BY stage
	`, profile)
	if err != nil {
		return nil, fmt.Errorf("native lifecycle: group query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stage string
		var n int64
		if err := rows.Scan(&stage, &n); err != nil {
			return nil, fmt.Errorf("native lifecycle: scan row: %w", err)
		}
		out[stage] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native lifecycle: rows: %w", err)
	}
	return out, nil
}

// pipelineCountsSupabase retrieves counts via PostgREST. Issues one HEAD
// request per stage against memory_lifecycle, filtered by profile + stage,
// and pulls the count from the Content-Range response header (driven by
// the "Prefer: count=exact" request header).
//
// Mixed-version safe: first probes whether memory_lifecycle exists at all.
// Pre-migration-026 DBs don't have the table -- in that case we fall back
// to counting memories for the profile and returning the total as 'fresh'.
// Mirrors the Postgres path's existence check.
func pipelineCountsSupabase(ctx context.Context, cfg *Config, profile string) (map[string]int64, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("native lifecycle: supabase client: %w", err)
	}

	out := map[string]int64{"fresh": 0, "stable": 0, "editing": 0}

	// Probe: does memory_lifecycle exist on this DB?
	_, probeErr := client.headCountExact(ctx, "memory_lifecycle", nil)
	if probeErr != nil {
		if isRelationNotFound(probeErr) {
			// Pre-026 DB -- everything is implicitly fresh. Mirror the
			// Postgres fallback: count memories for the profile and stuff
			// the total under 'fresh'.
			total, fallbackErr := client.headCountExact(ctx, "memories", url.Values{"profile": []string{"eq." + profile}})
			if fallbackErr != nil {
				return nil, fmt.Errorf("native lifecycle: fallback count: %w", fallbackErr)
			}
			out["fresh"] = total
			return out, nil
		}
		return nil, fmt.Errorf("native lifecycle: memory_lifecycle probe failed: %w", probeErr)
	}

	for _, stage := range []string{"fresh", "stable", "editing"} {
		filters := url.Values{
			"profile": []string{"eq." + profile},
			"stage":   []string{"eq." + stage},
		}
		n, err := client.headCountExact(ctx, "memory_lifecycle", filters)
		if err != nil {
			return nil, fmt.Errorf("native lifecycle: count for stage=%s: %w", stage, err)
		}
		out[stage] = n
	}
	return out, nil
}

// headCountExact issues a HEAD request against /rest/v1/{table} with the
// given filters and returns the total row count from the Content-Range
// response header. PostgREST emits Content-Range in the form "0-N/TOTAL"
// (or "*/0" for empty results) whenever "Prefer: count=exact" is set.
//
// filters may be nil. Range: 0-0 keeps the response body empty -- we only
// care about the header.
func (c *supabaseClient) headCountExact(ctx context.Context, table string, filters url.Values) (int64, error) {
	q := url.Values{}
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	endpoint := c.baseURL + "/" + table
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "count=exact")
	req.Header.Set("Range-Unit", "items")
	req.Header.Set("Range", "0-0")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("supabase HEAD %s: http: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// PostgREST returns 200 for non-empty, 206 (Partial Content) when the
	// returned range is narrower than the full set, and 416 (Range Not
	// Satisfiable) for an empty table even with the count header set. All
	// three are valid for a count-only request.
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		// fallthrough: parse Content-Range
	case http.StatusNotFound:
		// PostgREST emits 404 with a JSON error body when the table or
		// view does not exist. The body is empty on HEAD, so we rely on
		// status alone here -- isRelationNotFound picks this up via the
		// wrapped error string below.
		return 0, fmt.Errorf("supabase HEAD %s: http 404: relation %q does not exist", endpoint, table)
	default:
		return 0, fmt.Errorf("supabase HEAD %s: http %d", endpoint, resp.StatusCode)
	}

	cr := resp.Header.Get("Content-Range")
	if cr == "" {
		return 0, fmt.Errorf("supabase HEAD %s: missing Content-Range header (status %d) -- Prefer: count=exact not honored", endpoint, resp.StatusCode)
	}
	// Shape: "0-N/TOTAL" or "*/0". The total is after the slash.
	slash := strings.LastIndex(cr, "/")
	if slash < 0 || slash == len(cr)-1 {
		return 0, fmt.Errorf("supabase HEAD %s: malformed Content-Range %q", endpoint, cr)
	}
	tail := cr[slash+1:]
	if tail == "*" {
		return 0, nil
	}
	n, err := strconv.ParseInt(tail, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("supabase HEAD %s: parse Content-Range total %q: %w", endpoint, tail, err)
	}
	return n, nil
}

// isRelationNotFound returns true for PostgREST "relation ... does not
// exist" errors (wraps Postgres 42P01) and HTTP 404s against PostgREST
// endpoints that reference a missing table.
func isRelationNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "does not exist") ||
		strings.Contains(s, "42P01") ||
		strings.Contains(s, "http 404")
}

// DecayFactor returns Shodh's hybrid decay multiplier for a memory of
// given age. Pure function -- no I/O.
//
//	0-3 days: exponential exp(-lambda * age)
//	3+ days:  power-law (age/3)^(-beta)
//
// Mirrors Python's src/ogham/lifecycle.py::hybrid_decay_factor for
// cross-stack parity.
func DecayFactor(ageDays, lambda, beta float64) float64 {
	if ageDays < 3 {
		return math.Exp(-lambda * ageDays)
	}
	return math.Pow(ageDays/3, -beta)
}
