// Package native: advance_lifecycle -- batched lifecycle promotions.
//
// Mirrors src/ogham/lifecycle.py::advance_stages and the
// advance_lifecycle MCP tool. Two batched UPDATEs:
//
//  1. fresh -> stable: rows that have dwelled >= DwellHours AND clear
//     the dual-signal gate (surprise OR importance over their thresholds)
//  2. editing -> stable: rows whose editing window has expired
//
// SQL-only, no LLM. Sub-100ms cold-start friendly: one connection,
// two RETURNING-counting UPDATEs.
//
// Mixed-version safe: pre-026 DBs without memory_lifecycle return zeros
// without error, mirroring the Python contract for clusters that
// haven't migrated yet.
//
// Defaults match Python (lifecycle.py module-level constants):
//
//	DwellHours              1.0
//	SurpriseGate            0.3
//	ImportanceGate          0.5
//	EditingWindowMinutes    30

package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdvanceLifecycleDefaults match Python's lifecycle.py constants.
const (
	AdvanceDefaultDwellHours           = 1.0
	AdvanceDefaultSurpriseGate         = 0.3
	AdvanceDefaultImportanceGate       = 0.5
	AdvanceDefaultEditingWindowMinutes = 30
)

// AdvanceLifecycleOptions tunes the gates. Zero values pick the
// Python-matching defaults.
type AdvanceLifecycleOptions struct {
	DwellHours           float64
	SurpriseGate         float64
	ImportanceGate       float64
	EditingWindowMinutes int
}

// StageReport is the {fresh_to_stable, editing_closed} pair Python
// returns. Mirrors lifecycle.StageReport so the JSON shape is identical.
type StageReport struct {
	Profile         string `json:"profile"`
	FreshToStable   int    `json:"fresh_to_stable"`
	EditingClosed   int    `json:"editing_closed"`
	LifecycleAbsent bool   `json:"lifecycle_absent,omitempty"`
}

// AdvanceLifecycle runs the two-batch sweep for a profile. Returns
// counts for each transition.
func AdvanceLifecycle(ctx context.Context, cfg *Config, profile string, opts AdvanceLifecycleOptions) (*StageReport, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native advance_lifecycle: nil config")
	}
	if profile == "" {
		profile = ActiveProfile(cfg)
	}

	dwellHours := opts.DwellHours
	if dwellHours <= 0 {
		dwellHours = AdvanceDefaultDwellHours
	}
	surpriseGate := opts.SurpriseGate
	if surpriseGate <= 0 {
		surpriseGate = AdvanceDefaultSurpriseGate
	}
	importanceGate := opts.ImportanceGate
	if importanceGate <= 0 {
		importanceGate = AdvanceDefaultImportanceGate
	}
	editingWindow := opts.EditingWindowMinutes
	if editingWindow <= 0 {
		editingWindow = AdvanceDefaultEditingWindowMinutes
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return advanceLifecyclePostgres(ctx, cfg, profile, dwellHours, surpriseGate, importanceGate, editingWindow)
	case "supabase":
		return advanceLifecycleSupabase(ctx, cfg, profile, dwellHours, surpriseGate, importanceGate, editingWindow)
	default:
		return nil, fmt.Errorf("native advance_lifecycle: unknown backend %q", backend)
	}
}

func advanceLifecyclePostgres(ctx context.Context, cfg *Config, profile string, dwellHours, surpriseGate, importanceGate float64, editingWindow int) (*StageReport, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("advance_lifecycle: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Mixed-version probe: the Python lifecycle module assumes the
	// memory_lifecycle table exists (post-026). On a pre-026 cluster
	// the Python tool would raise; the Go path returns a zeroed report
	// flagged with LifecycleAbsent so dashboards don't show an error
	// for installs that haven't migrated yet. Same shape PipelineCounts
	// uses elsewhere in this package.
	var hasLifecycle bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = 'memory_lifecycle'
		)
	`).Scan(&hasLifecycle); err != nil {
		return nil, fmt.Errorf("advance_lifecycle: probe lifecycle table: %w", err)
	}
	if !hasLifecycle {
		return &StageReport{Profile: profile, LifecycleAbsent: true}, nil
	}

	report := &StageReport{Profile: profile}

	// 1. fresh -> stable.
	freshCutoff := time.Now().UTC().Add(-time.Duration(dwellHours * float64(time.Hour)))
	rows, err := conn.Query(ctx, `
UPDATE memory_lifecycle AS ml
   SET stage = 'stable',
       stage_entered_at = now(),
       updated_at = now()
  FROM memories AS m
 WHERE ml.memory_id = m.id
   AND ml.profile = $1
   AND ml.stage = 'fresh'
   AND ml.stage_entered_at <= $2
   AND (m.surprise >= $3 OR m.importance >= $4)
RETURNING ml.memory_id`,
		profile, freshCutoff, surpriseGate, importanceGate,
	)
	if err != nil {
		return nil, fmt.Errorf("advance_lifecycle fresh->stable: %w", err)
	}
	for rows.Next() {
		report.FreshToStable++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("advance_lifecycle fresh->stable rows: %w", err)
	}

	// 2. editing -> stable (window expired).
	editCutoff := time.Now().UTC().Add(-time.Duration(editingWindow) * time.Minute)
	rows2, err := conn.Query(ctx, `
UPDATE memory_lifecycle
   SET stage = 'stable',
       stage_entered_at = now(),
       updated_at = now()
 WHERE profile = $1
   AND stage = 'editing'
   AND stage_entered_at <= $2
RETURNING memory_id`,
		profile, editCutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("advance_lifecycle editing-close: %w", err)
	}
	for rows2.Next() {
		report.EditingClosed++
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return nil, fmt.Errorf("advance_lifecycle editing-close rows: %w", err)
	}
	return report, nil
}

// advanceLifecycleSupabase posts to a server-side wrapper RPC if it
// exists; otherwise falls back to issuing two PostgREST PATCH requests.
//
// Implementation note: the Python codepath calls _execute() with raw
// SQL, which Supabase backends route through PostgREST RPC. There is
// no `wiki_advance_lifecycle` RPC -- the Python supabase backend calls
// the stored procedure `advance_lifecycle_sql` if present, otherwise
// errors. Rather than ship a half-implemented Supabase path, the Go
// CLI returns an explicit error when the absorbed backend is supabase
// and steers the caller to --sidecar. The plan calls advance_lifecycle
// SQL-only, but the SQL is owned by the Postgres connection. Lambda
// thin-client deployments use Postgres directly.
func advanceLifecycleSupabase(ctx context.Context, cfg *Config, profile string, dwellHours, surpriseGate, importanceGate float64, editingWindow int) (*StageReport, error) {
	// Compute cutoffs for the PATCH bodies. These are RFC3339 because
	// PostgREST parses ISO-8601 with offset.
	now := time.Now().UTC()
	freshCutoff := now.Add(-time.Duration(dwellHours * float64(time.Hour))).Format(time.RFC3339Nano)
	editCutoff := now.Add(-time.Duration(editingWindow) * time.Minute).Format(time.RFC3339Nano)

	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	report := &StageReport{Profile: profile}

	// Probe table existence -- mixed-version safe like the postgres path.
	if _, err := client.headCountExact(ctx, "memory_lifecycle", url.Values{
		"profile": []string{"eq." + profile},
	}); err != nil {
		if isRelationNotFound(err) {
			return &StageReport{Profile: profile, LifecycleAbsent: true}, nil
		}
		return nil, fmt.Errorf("advance_lifecycle supabase: probe: %w", err)
	}

	// Step 1: fresh -> stable. PostgREST can't express the OR-gate
	// JOIN-against-memories logic in a single PATCH; we two-step:
	//   a) GET candidate memory_ids whose memories table row clears
	//      either gate AND lifecycle is fresh past the dwell cutoff
	//   b) PATCH lifecycle rows by id IN (...) flipping stage
	// This trades one round-trip for a cleaner client-side audit.
	candIDs, err := fetchFreshToStableCandidates(ctx, client, profile, freshCutoff, surpriseGate, importanceGate)
	if err != nil {
		return nil, err
	}
	if len(candIDs) > 0 {
		n, err := patchLifecycleStage(ctx, client, profile, "fresh", "stable", candIDs)
		if err != nil {
			return nil, err
		}
		report.FreshToStable = n
	}

	// Step 2: editing -> stable (cutoff-based, no candidates query).
	n, err := patchLifecycleEditingClose(ctx, client, profile, editCutoff)
	if err != nil {
		return nil, err
	}
	report.EditingClosed = n
	return report, nil
}

// fetchFreshToStableCandidates returns memory_ids whose lifecycle row is
// fresh past the cutoff AND whose memories row clears either gate.
// Two GETs joined client-side: PostgREST doesn't support cross-table
// row filters in a single query without a SQL view, and we'd rather
// not require schema additions on the user's project.
func fetchFreshToStableCandidates(ctx context.Context, client *supabaseClient, profile, freshCutoff string, surpriseGate, importanceGate float64) ([]string, error) {
	// Lifecycle side: stage=fresh, stage_entered_at <= cutoff, profile=...
	q := url.Values{}
	q.Set("profile", "eq."+profile)
	q.Set("stage", "eq.fresh")
	q.Set("stage_entered_at", "lte."+freshCutoff)
	q.Set("select", "memory_id")
	endpoint := client.baseURL + "/memory_lifecycle?" + q.Encode()
	raw, err := client.getJSON(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("advance_lifecycle: fetch fresh candidates: %w", err)
	}
	var lifeRows []struct {
		MemoryID string `json:"memory_id"`
	}
	if err := decodeJSON(raw, &lifeRows); err != nil {
		return nil, fmt.Errorf("advance_lifecycle: parse fresh candidates: %w", err)
	}
	if len(lifeRows) == 0 {
		return nil, nil
	}
	candidate := make([]string, 0, len(lifeRows))
	idSet := make(map[string]struct{}, len(lifeRows))
	for _, r := range lifeRows {
		idSet[r.MemoryID] = struct{}{}
	}

	// Memories side: surprise>=g OR importance>=g, IN (candidate ids).
	// PostgREST OR syntax: or=(surprise.gte.X,importance.gte.Y).
	// `in.(a,b,c)` is the IN clause.
	idList := make([]string, 0, len(idSet))
	for k := range idSet {
		idList = append(idList, k)
	}
	q2 := url.Values{}
	q2.Set("profile", "eq."+profile)
	q2.Set("id", "in.("+strings.Join(idList, ",")+")")
	q2.Set("or", fmt.Sprintf("(surprise.gte.%g,importance.gte.%g)", surpriseGate, importanceGate))
	q2.Set("select", "id")
	endpoint = client.baseURL + "/memories?" + q2.Encode()
	raw, err = client.getJSON(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("advance_lifecycle: fetch gate-clearing memories: %w", err)
	}
	var memRows []struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(raw, &memRows); err != nil {
		return nil, fmt.Errorf("advance_lifecycle: parse gate-clearing memories: %w", err)
	}
	for _, r := range memRows {
		candidate = append(candidate, r.ID)
	}
	return candidate, nil
}

// patchLifecycleStage flips stage=fromStage rows in the given id set to
// toStage. Returns the count of rows updated.
func patchLifecycleStage(ctx context.Context, client *supabaseClient, profile, fromStage, toStage string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q := url.Values{}
	q.Set("profile", "eq."+profile)
	q.Set("stage", "eq."+fromStage)
	q.Set("memory_id", "in.("+strings.Join(ids, ",")+")")
	endpoint := client.baseURL + "/memory_lifecycle?" + q.Encode()

	body := map[string]any{
		"stage":            toStage,
		"stage_entered_at": time.Now().UTC().Format(time.RFC3339Nano),
		"updated_at":       time.Now().UTC().Format(time.RFC3339Nano),
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("advance_lifecycle PATCH marshal: %w", err)
	}
	raw, err := client.doAuthed(ctx, http.MethodPatch, endpoint, bodyJSON, map[string]string{
		"Prefer": "return=representation",
	})
	if err != nil {
		return 0, fmt.Errorf("advance_lifecycle PATCH %s->%s: %w", fromStage, toStage, err)
	}
	var rows []map[string]any
	if err := decodeJSON(raw, &rows); err != nil {
		return 0, fmt.Errorf("advance_lifecycle PATCH parse: %w", err)
	}
	return len(rows), nil
}

// patchLifecycleEditingClose closes editing windows past the cutoff.
func patchLifecycleEditingClose(ctx context.Context, client *supabaseClient, profile, editCutoff string) (int, error) {
	q := url.Values{}
	q.Set("profile", "eq."+profile)
	q.Set("stage", "eq.editing")
	q.Set("stage_entered_at", "lte."+editCutoff)
	endpoint := client.baseURL + "/memory_lifecycle?" + q.Encode()

	body := map[string]any{
		"stage":            "stable",
		"stage_entered_at": time.Now().UTC().Format(time.RFC3339Nano),
		"updated_at":       time.Now().UTC().Format(time.RFC3339Nano),
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("advance_lifecycle close-editing marshal: %w", err)
	}
	raw, err := client.doAuthed(ctx, http.MethodPatch, endpoint, bodyJSON, map[string]string{
		"Prefer": "return=representation",
	})
	if err != nil {
		return 0, fmt.Errorf("advance_lifecycle close-editing: %w", err)
	}
	var rows []map[string]any
	if err := decodeJSON(raw, &rows); err != nil {
		return 0, fmt.Errorf("advance_lifecycle close-editing parse: %w", err)
	}
	return len(rows), nil
}

// decodeJSON is a thin wrapper around json.Unmarshal that no-ops on
// empty bodies. PostgREST returns "" for some no-op PATCH replies.
func decodeJSON(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}
