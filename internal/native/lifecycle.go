// Package native lifecycle: read-only pipeline counts + decay factor.
// Writes (advance_stages, open_editing_window, strengthen_edges) remain
// Python-owned; the Go CLI only displays state for the dashboard.

package native

import (
	"context"
	"fmt"
	"math"

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

// pipelineCountsSupabase retrieves counts via PostgREST. Stubbed out
// pending follow-up -- the Postgres path covers scratch + BEAM + most
// self-hosted users, which is the MVP target for Phase 6. A fuller
// Supabase path (table-existence probe via /rest/v1/memory_lifecycle?limit=0
// with 404 fallback to memories count) can land once the Postgres
// read-only flow is validated end-to-end.
func pipelineCountsSupabase(ctx context.Context, cfg *Config, profile string) (map[string]int64, error) {
	return nil, fmt.Errorf("native lifecycle: supabase PipelineCounts not yet implemented -- use postgres backend or wait for follow-up")
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
