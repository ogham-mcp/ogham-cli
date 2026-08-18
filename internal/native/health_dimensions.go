package native

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Zone is a stoplight bucket for a dimension score.
//
// Mirrors Python ogham.health_dimensions.Zone:
//
//	GREEN  score >= 8.0  -- healthy, no action needed
//	AMBER  score >= 5.0  -- degraded, worth investigating
//	RED    score <  5.0  -- broken or near-broken
type Zone string

const (
	ZoneGreen Zone = "GREEN"
	ZoneAmber Zone = "AMBER"
	ZoneRed   Zone = "RED"
)

// DimensionResult is one row of the extended health report. Mirrors
// Python ogham.health_dimensions.DimensionResult so the JSON wire
// format is identical between Python sidecar and Go native paths.
type DimensionResult struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Zone   Zone    `json:"zone"`
	Detail string  `json:"detail"`
}

// ZoneOf maps a 0-10 score to its stoplight bucket.
func ZoneOf(score float64) Zone {
	if score >= 8.0 {
		return ZoneGreen
	}
	if score >= 5.0 {
		return ZoneAmber
	}
	return ZoneRed
}

// roundDecimal rounds a float to one decimal place. Matches Python
// round(x, 1) behaviour closely enough for stable display values.
func roundDecimal(v float64) float64 {
	return math.Round(v*10) / 10
}

// humanizeAge renders an age-in-hours as a short human string:
//
//	< 1h    => "45m"
//	< 24h   => "3.5h"
//	>= 24h  => "12.3d"
func humanizeAge(hours float64) string {
	if hours < 1 {
		return fmt.Sprintf("%.0fm", hours*60)
	}
	if hours < 24 {
		return fmt.Sprintf("%.1fh", hours)
	}
	return fmt.Sprintf("%.1fd", hours/24)
}

// resolveHealthProfile picks the effective profile for a dimension
// call. Explicit profile arg wins; else cfg.Profile; else "default".
// Kept separate from native.Search's profile resolution to avoid
// coupling extended-health behaviour to that hot path.
func resolveHealthProfile(cfg *Config, profile string) string {
	if profile != "" {
		return profile
	}
	if cfg != nil && cfg.Profile != "" {
		return cfg.Profile
	}
	return "default"
}

// ----------------------------------------------------------------------
// 1. DB freshness -- when did this profile last write a memory?
// ----------------------------------------------------------------------

// ComputeDBFreshness scores how recently the given profile wrote a
// memory. The scoring curve matches Python ogham.health_dimensions:
//
//	age <= 24h   => 10.0           (GREEN)
//	24h-72h      => 7.99 -> 5.0    (AMBER, linear)
//	72h-30d      => 4.99 -> 0.0    (RED, linear; capped at 30d excess)
//	no rows      => 0.0            (RED -- "dead writer")
//
// A query error surfaces as RED with the error in Detail.
func ComputeDBFreshness(ctx context.Context, cfg *Config, profile string) DimensionResult {
	const dimName = "DB freshness"
	profile = resolveHealthProfile(cfg, profile)

	last, err := lastMemoryWrite(ctx, cfg, profile)
	if err != nil {
		return DimensionResult{
			Name:   dimName,
			Score:  0.0,
			Zone:   ZoneRed,
			Detail: fmt.Sprintf("query failed: %v", err),
		}
	}
	if last == nil {
		return DimensionResult{
			Name:   dimName,
			Score:  0.0,
			Zone:   ZoneRed,
			Detail: "no memories in profile (dead writer)",
		}
	}

	ageHours := time.Now().UTC().Sub(*last).Hours()

	var score float64
	switch {
	case ageHours <= 24:
		score = 10.0
	case ageHours <= 72:
		// 24h -> ~7.99, 72h -> 5.0 (linear)
		score = 7.99 - (ageHours-24)*(2.99/48.0)
	default:
		// 72h -> 4.99, 72h+30d -> ~0.0 (linear, capped)
		excess := math.Min(ageHours-72, 30*24)
		score = math.Max(0.0, 4.99-excess*(4.99/(30*24)))
	}
	score = roundDecimal(score)

	return DimensionResult{
		Name:   dimName,
		Score:  score,
		Zone:   ZoneOf(score),
		Detail: fmt.Sprintf("last write %s ago", humanizeAge(ageHours)),
	}
}

// lastMemoryWrite returns the MAX(created_at) for the profile, or
// nil when the profile has no rows. Backend-dispatched.
func lastMemoryWrite(ctx context.Context, cfg *Config, profile string) (*time.Time, error) {
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return lastMemoryWritePostgres(ctx, cfg, profile)
	case "supabase":
		return lastMemoryWriteSupabase(ctx, cfg, profile)
	default:
		return nil, fmt.Errorf("unknown backend: %s", backend)
	}
}

func lastMemoryWritePostgres(ctx context.Context, cfg *Config, profile string) (*time.Time, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var last *time.Time
	row := conn.QueryRow(ctx,
		"SELECT MAX(created_at) FROM memories WHERE profile = $1",
		profile,
	)
	if err := row.Scan(&last); err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return last, nil
}

// lastMemoryWriteSupabase is a placeholder until the Supabase /
// PostgREST path lands in the follow-up session. Returning nil
// causes ComputeDBFreshness to report "no memories in profile",
// which is the safest stand-in -- explicit + visible to operators.
func lastMemoryWriteSupabase(ctx context.Context, cfg *Config, profile string) (*time.Time, error) {
	return nil, errors.New("supabase backend: not yet ported (use postgres backend or wait for v0.X.Y)")
}

// ----------------------------------------------------------------------
// 4. Corpus size -- how many memories does this profile hold?
// ----------------------------------------------------------------------

// ComputeCorpusSize scores the profile's memory count. Scoring:
//
//	count >= 100   => 10.0                 (GREEN)
//	count 10-99    => 5.0 -> 7.99 (linear) (AMBER)
//	count 1-9      => 0.x -> 4.99 (linear) (RED)
//	count == 0     => 0.0                  (RED, "empty profile")
//
// Uses GetStats for both backends -- already battle-tested across
// Supabase / Postgres in the rest of the CLI.
func ComputeCorpusSize(ctx context.Context, cfg *Config, profile string) DimensionResult {
	const dimName = "Corpus size"
	profile = resolveHealthProfile(cfg, profile)

	// GetStats reads cfg.Profile internally; override so this dim
	// honours an explicit profile arg without mutating the caller's
	// config pointer permanently.
	scratch := *cfg
	scratch.Profile = profile
	stats, err := GetStats(ctx, &scratch)
	if err != nil {
		return DimensionResult{
			Name:   dimName,
			Score:  0.0,
			Zone:   ZoneRed,
			Detail: fmt.Sprintf("count failed: %v", err),
		}
	}

	count := stats.Total
	var score float64
	switch {
	case count >= 100:
		score = 10.0
	case count >= 10:
		score = 5.0 + float64(count-10)/89.0*2.99
	case count > 0:
		score = float64(count) / 9.0 * 4.99
	default:
		score = 0.0
	}
	score = roundDecimal(score)

	detail := "empty profile"
	if count > 0 {
		detail = fmt.Sprintf("%d memories", count)
	}
	return DimensionResult{
		Name:   dimName,
		Score:  score,
		Zone:   ZoneOf(score),
		Detail: detail,
	}
}

// ----------------------------------------------------------------------
// 8. E2E probe -- store -> hybrid_search -> delete round-trip.
// ----------------------------------------------------------------------

// ComputeE2EProbe scores a synthetic round-trip against the live store.
// Writes a single memory with a unique tag, searches for it, then
// deletes it. Any failure -> RED 0.0. Success -> GREEN 10.0 with the
// total wall-clock duration in Detail.
//
// Uses native.Store + native.Search + native.Delete -- no new SQL.
// Cleanup runs in a defer so a failed search still removes the row.
func ComputeE2EProbe(ctx context.Context, cfg *Config, profile string) DimensionResult {
	const dimName = "E2E probe"
	profile = resolveHealthProfile(cfg, profile)

	ok, totalMs, err := runE2EProbe(ctx, cfg, profile)
	if ok {
		return DimensionResult{
			Name:   dimName,
			Score:  10.0,
			Zone:   ZoneGreen,
			Detail: fmt.Sprintf("store->search->delete OK in %.0fms", totalMs),
		}
	}
	return DimensionResult{
		Name:   dimName,
		Score:  0.0,
		Zone:   ZoneRed,
		Detail: fmt.Sprintf("failed: %v", err),
	}
}

func runE2EProbe(ctx context.Context, cfg *Config, profile string) (bool, float64, error) {
	probeID := uuid.NewString()[:8]
	tag := fmt.Sprintf("health-probe-%s", probeID)
	content := fmt.Sprintf("Health probe round-trip %s", probeID)

	t0 := time.Now()
	var storedID string

	defer func() {
		// Best-effort cleanup if delete didn't run on the happy path.
		// Failures here are logged via the slog default -- we don't
		// want a cleanup error to mask the original probe outcome.
		if storedID != "" {
			_, _ = Delete(ctx, cfg, storedID, profile)
		}
	}()

	stored, err := Store(ctx, cfg, content, StoreOptions{
		Profile: profile,
		Source:  "health-probe",
		Tags:    []string{tag},
	})
	if err != nil {
		return false, msSince(t0), fmt.Errorf("store: %w", err)
	}
	storedID = stored.ID

	results, err := Search(ctx, cfg, content, SearchOptions{
		Limit:   3,
		Profile: profile,
		Tags:    []string{tag},
	})
	if err != nil {
		return false, msSince(t0), fmt.Errorf("search: %w", err)
	}
	if len(results) == 0 {
		return false, msSince(t0), fmt.Errorf("search returned 0 results for unique tag")
	}

	if _, err := Delete(ctx, cfg, storedID, profile); err != nil {
		return false, msSince(t0), fmt.Errorf("delete: %w", err)
	}
	storedID = "" // mark cleanup unnecessary
	return true, msSince(t0), nil
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}

// ----------------------------------------------------------------------
// compose -- run the dimensions implemented in this Path-B batch.
// ----------------------------------------------------------------------

// ExtendedHealthReport is the JSON payload `ogham health --extended`
// emits. Mirrors the shape Python's compose_health produces, with the
// caveat that this Path-B batch ships only 3 of the 8 dimensions.
// PortedDimensions / TotalDimensions make the partial state explicit
// so users (and dashboards) don't read this as a stable 8-row format
// until the follow-up session lands.
type ExtendedHealthReport struct {
	Profile          string            `json:"profile"`
	OverallScore     float64           `json:"overall_score"`
	OverallZone      Zone              `json:"overall_zone"`
	Dimensions       []DimensionResult `json:"dimensions"`
	PortedDimensions int               `json:"ported_dimensions"`
	TotalDimensions  int               `json:"total_dimensions"`
	DeferredNotice   string            `json:"deferred_notice,omitempty"`
	DurationMs       float64           `json:"duration_ms"`
}

// ComposeExtendedHealth runs every dimension in this batch and returns
// a single report. Each dimension is wrapped in a recover so one
// panicking probe doesn't take the whole report down -- matches the
// Python exception-catching pattern in compose_health.
func ComposeExtendedHealth(ctx context.Context, cfg *Config, profile string) ExtendedHealthReport {
	profile = resolveHealthProfile(cfg, profile)
	t0 := time.Now()

	dims := []DimensionResult{
		safeCompute("DB freshness", func() DimensionResult {
			return ComputeDBFreshness(ctx, cfg, profile)
		}),
		safeCompute("Corpus size", func() DimensionResult {
			return ComputeCorpusSize(ctx, cfg, profile)
		}),
		safeCompute("E2E probe", func() DimensionResult {
			return ComputeE2EProbe(ctx, cfg, profile)
		}),
	}

	overall := OverallScore(dims)
	return ExtendedHealthReport{
		Profile:          profile,
		OverallScore:     overall,
		OverallZone:      ZoneOf(overall),
		Dimensions:       dims,
		PortedDimensions: 3,
		TotalDimensions:  8,
		DeferredNotice:   "5 of 8 dimensions deferred to a follow-up port (schema integrity, hybrid search latency, wiki coverage, profile health, concurrency)",
		DurationMs:       msSince(t0),
	}
}

// OverallScore is the mean of dimension scores, rounded to one decimal.
// Empty slice yields 0.0 -- mirrors Python's overall_score behaviour.
func OverallScore(dims []DimensionResult) float64 {
	if len(dims) == 0 {
		return 0.0
	}
	var total float64
	for _, d := range dims {
		total += d.Score
	}
	return roundDecimal(total / float64(len(dims)))
}

// safeCompute wraps a dimension function so a panic surfaces as a RED
// row instead of crashing the report. Doesn't swallow errors -- those
// are still represented in the dimension's Detail field per its own
// contract; the recover only catches programmer mistakes (nil deref,
// index out of range, etc.) so the rest of the report still renders.
func safeCompute(name string, fn func() DimensionResult) (out DimensionResult) {
	defer func() {
		if r := recover(); r != nil {
			out = DimensionResult{
				Name:   name,
				Score:  0.0,
				Zone:   ZoneRed,
				Detail: fmt.Sprintf("panic: %v", r),
			}
		}
	}()
	return fn()
}
