package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditEvent mirrors a row from the audit_log table (projected to the
// columns the CLI displays).
type AuditEvent struct {
	EventTime time.Time `json:"event_time"`
	Profile   string    `json:"profile"`
	Operation string    `json:"operation"`
	MemoryID  *string   `json:"memory_id,omitempty"`
	Details   any       `json:"details,omitempty"`
}

// DeleteResult is returned by Delete on success.
type DeleteResult struct {
	ID      string `json:"id"`
	Profile string `json:"profile"`
}

// -----------------------------------------------------------------------
// Delete a memory by id + profile

func Delete(ctx context.Context, cfg *Config, id, profile string) (*DeleteResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native delete: nil config")
	}
	if id == "" {
		return nil, fmt.Errorf("native delete: memory id required")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return deletePostgres(ctx, cfg, id, profile)
	case "supabase":
		return deleteSupabase(ctx, cfg, id, profile)
	default:
		return nil, fmt.Errorf("native delete: unknown backend %q", backend)
	}
}

func deletePostgres(ctx context.Context, cfg *Config, id, profile string) (*DeleteResult, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native delete: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tag, err := conn.Exec(ctx,
		"DELETE FROM memories WHERE id = $1::uuid AND profile = $2",
		id, profile)
	if err != nil {
		return nil, fmt.Errorf("native delete: exec: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("no memory with id %q in profile %q", id, profile)
	}
	return &DeleteResult{ID: id, Profile: profile}, nil
}

func deleteSupabase(ctx context.Context, cfg *Config, id, profile string) (*DeleteResult, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("id", "eq."+id)
	q.Set("profile", "eq."+profile)
	endpoint := client.baseURL + "/memories?" + q.Encode()

	raw, err := client.doAuthed(ctx, "DELETE", endpoint, nil, map[string]string{
		"Prefer": "return=representation",
	})
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native delete: parse: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no memory with id %q in profile %q", id, profile)
	}
	return &DeleteResult{ID: id, Profile: profile}, nil
}

// -----------------------------------------------------------------------
// Cleanup expired memories

type CleanupResult struct {
	Profile     string `json:"profile"`
	ExpiredSeen int    `json:"expired_seen"`
	Deleted     int    `json:"deleted"`
}

// CountExpired is a dry-run count of expired memories in a profile.
func CountExpired(ctx context.Context, cfg *Config, profile string) (int, error) {
	if cfg == nil {
		return 0, fmt.Errorf("native count_expired: nil config")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return 0, err
	}
	switch backend {
	case "postgres":
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close(ctx) }()
		var n int
		err = conn.QueryRow(ctx, "SELECT count_expired_memories($1)", profile).Scan(&n)
		return n, err
	case "supabase":
		client, err := newSupabaseClient(cfg)
		if err != nil {
			return 0, err
		}
		raw, err := client.callRPC(ctx, "count_expired_memories", map[string]any{"target_profile": profile})
		if err != nil {
			return 0, err
		}
		// The RPC returns a scalar integer.
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, fmt.Errorf("parse count: %w (body: %s)", err, truncateForError(raw))
		}
		return n, nil
	}
	return 0, fmt.Errorf("unknown backend")
}

// Cleanup deletes expired memories in a profile via cleanup_expired_memories RPC.
func Cleanup(ctx context.Context, cfg *Config, profile string) (*CleanupResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native cleanup: nil config")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	seen, _ := CountExpired(ctx, cfg, profile) // best-effort
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	var deleted int
	switch backend {
	case "postgres":
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return nil, fmt.Errorf("native cleanup: connect: %w", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if err := conn.QueryRow(ctx,
			"SELECT cleanup_expired_memories($1)", profile).Scan(&deleted); err != nil {
			return nil, fmt.Errorf("native cleanup: rpc: %w", err)
		}
	case "supabase":
		client, err := newSupabaseClient(cfg)
		if err != nil {
			return nil, err
		}
		raw, err := client.callRPC(ctx, "cleanup_expired_memories", map[string]any{"target_profile": profile})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &deleted); err != nil {
			return nil, fmt.Errorf("native cleanup: parse: %w", err)
		}
	default:
		return nil, fmt.Errorf("native cleanup: unknown backend %q", backend)
	}
	return &CleanupResult{Profile: profile, ExpiredSeen: seen, Deleted: deleted}, nil
}

// -----------------------------------------------------------------------
// Hebbian decay

type DecayResult struct {
	Profile  string `json:"profile"`
	Decayed  int    `json:"decayed"`
	DryRun   bool   `json:"dry_run"`
	Eligible int    `json:"eligible"`
}

// CountDecayEligible returns the number of memories that would be affected
// by a decay pass. Mirrors Python's Hebbian-decay predicate.
func CountDecayEligible(ctx context.Context, cfg *Config, profile string) (int, error) {
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return 0, err
	}
	const sql = `SELECT count(*)::integer FROM memories
WHERE profile = $1
  AND (expires_at IS NULL OR expires_at > now())
  AND confidence > 0.05
  AND (last_accessed_at IS NULL OR last_accessed_at < now() - interval '7 days')`

	switch backend {
	case "postgres":
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close(ctx) }()
		var n int
		err = conn.QueryRow(ctx, sql, profile).Scan(&n)
		return n, err
	case "supabase":
		// PostgREST can't run arbitrary SQL. Count via GET with the same
		// filters. url.Values doesn't support repeated keys (later call
		// overwrites), so the two `or=` clauses need manual assembly.
		client, err := newSupabaseClient(cfg)
		if err != nil {
			return 0, err
		}
		sevenDaysAgo := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
		params := []string{
			"select=id",
			"profile=eq." + url.QueryEscape(profile),
			"or=" + url.QueryEscape("(expires_at.is.null,expires_at.gt.now())"),
			"confidence=gt.0.05",
			"or=" + url.QueryEscape("(last_accessed_at.is.null,last_accessed_at.lt."+sevenDaysAgo+")"),
		}
		endpoint := client.baseURL + "/memories?" + strings.Join(params, "&")
		raw, err := client.getJSON(ctx, endpoint)
		if err != nil {
			return 0, err
		}
		var rows []struct{}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return 0, err
		}
		return len(rows), nil
	}
	return 0, fmt.Errorf("unknown backend")
}

// Decay applies Hebbian decay for a profile. When dryRun is true, only counts.
func Decay(ctx context.Context, cfg *Config, profile string, batchSize int, dryRun bool) (*DecayResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native decay: nil config")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if dryRun {
		n, err := CountDecayEligible(ctx, cfg, profile)
		if err != nil {
			return nil, err
		}
		return &DecayResult{Profile: profile, DryRun: true, Eligible: n}, nil
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	var decayed int
	switch backend {
	case "postgres":
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return nil, fmt.Errorf("native decay: connect: %w", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if err := conn.QueryRow(ctx,
			"SELECT apply_hebbian_decay($1, $2)", profile, batchSize).Scan(&decayed); err != nil {
			return nil, fmt.Errorf("native decay: rpc: %w", err)
		}
	case "supabase":
		client, err := newSupabaseClient(cfg)
		if err != nil {
			return nil, err
		}
		raw, err := client.callRPC(ctx, "apply_hebbian_decay", map[string]any{
			"target_profile": profile,
			"batch_size":     batchSize,
		})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &decayed); err != nil {
			return nil, fmt.Errorf("native decay: parse: %w", err)
		}
	default:
		return nil, fmt.Errorf("native decay: unknown backend %q", backend)
	}
	return &DecayResult{Profile: profile, Decayed: decayed}, nil
}

// -----------------------------------------------------------------------
// Confidence (reinforce / contradict)

// ConfidenceResult carries the post-update confidence score so callers
// can surface the new value to the user. Mirrors the Python tool
// shape -- reinforce/contradict both return the same payload, only the
// "status" label differs.
type ConfidenceResult struct {
	ID         string  `json:"id"`
	Profile    string  `json:"profile"`
	Confidence float64 `json:"confidence"`
}

// UpdateConfidence applies a signal to an existing memory's confidence
// via the database's update_confidence function. Both reinforce_memory
// (strength 0.5-1.0) and contradict_memory (strength 0.0-0.5) route
// through here -- the SQL function handles the math (EMA-style blend
// of the existing confidence with the new signal).
//
// The strength parameter is validated at the MCP handler layer so we
// don't reject legitimate migration / backfill calls from direct Go
// callers that may want to force-set a value.
func UpdateConfidence(ctx context.Context, cfg *Config, id string, signal float64, profile string) (*ConfidenceResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native update_confidence: nil config")
	}
	if id == "" {
		return nil, fmt.Errorf("native update_confidence: memory id required")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return updateConfidencePostgres(ctx, cfg, id, signal, profile)
	case "supabase":
		return updateConfidenceSupabase(ctx, cfg, id, signal, profile)
	default:
		return nil, fmt.Errorf("native update_confidence: unknown backend %q", backend)
	}
}

func updateConfidencePostgres(ctx context.Context, cfg *Config, id string, signal float64, profile string) (*ConfidenceResult, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native update_confidence: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var conf float64
	err = conn.QueryRow(ctx,
		"SELECT update_confidence($1::uuid, $2::float, $3)",
		id, signal, profile).Scan(&conf)
	if err != nil {
		return nil, fmt.Errorf("native update_confidence: exec: %w", err)
	}
	return &ConfidenceResult{ID: id, Profile: profile, Confidence: conf}, nil
}

func updateConfidenceSupabase(ctx context.Context, cfg *Config, id string, signal float64, profile string) (*ConfidenceResult, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	// The RPC uses "memory_profile" (not "profile") to avoid colliding
	// with PostgREST's built-in `profile` query parameter. Mirrors what
	// the Python Supabase backend does.
	raw, err := client.callRPC(ctx, "update_confidence", map[string]any{
		"memory_id":      id,
		"signal":         signal,
		"memory_profile": profile,
	})
	if err != nil {
		return nil, err
	}
	var conf float64
	if err := json.Unmarshal(raw, &conf); err != nil {
		return nil, fmt.Errorf("native update_confidence: parse: %w (body: %s)", err, truncateForError(raw))
	}
	return &ConfidenceResult{ID: id, Profile: profile, Confidence: conf}, nil
}

// -----------------------------------------------------------------------
// Audit

// Audit returns the most recent audit events for a profile.
func Audit(ctx context.Context, cfg *Config, profile, operation string, limit int) ([]AuditEvent, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native audit: nil config")
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	if limit <= 0 {
		limit = 20
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		conn, err := pgx.Connect(ctx, cfg.Database.URL)
		if err != nil {
			return nil, err
		}
		defer func() { _ = conn.Close(ctx) }()

		where := []string{"profile = $1"}
		args := []any{profile}
		if operation != "" {
			args = append(args, operation)
			where = append(where, fmt.Sprintf("operation = $%d", len(args)))
		}
		args = append(args, limit)
		// Schema renames since v0.3: resource_id (was memory_id),
		// metadata (was details). We alias both in the projection so
		// the Go + JSON field names stay stable for callers who've
		// wired against the v0.3 shape. This keeps API parity while
		// the schema evolves underneath.
		sql := `SELECT event_time, profile, operation, resource_id::text AS memory_id, metadata AS details
FROM audit_log
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY event_time DESC
LIMIT $` + fmt.Sprintf("%d", len(args))

		rows, err := conn.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("native audit: query: %w", err)
		}
		defer rows.Close()

		var out []AuditEvent
		for rows.Next() {
			var e AuditEvent
			var memID *string
			var raw []byte
			if err := rows.Scan(&e.EventTime, &e.Profile, &e.Operation, &memID, &raw); err != nil {
				return nil, fmt.Errorf("native audit: scan: %w", err)
			}
			e.MemoryID = memID
			if len(raw) > 0 {
				var d any
				if err := json.Unmarshal(raw, &d); err == nil {
					e.Details = d
				}
			}
			out = append(out, e)
		}
		return out, rows.Err()
	case "supabase":
		client, err := newSupabaseClient(cfg)
		if err != nil {
			return nil, err
		}
		q := url.Values{}
		// Schema renames since v0.3: resource_id (was memory_id),
		// metadata (was details). PostgREST projection aliases both
		// back to the v0.3 names for backward-compatibility.
		q.Set("select", "event_time,profile,operation,memory_id:resource_id,details:metadata")
		q.Set("profile", "eq."+profile)
		if operation != "" {
			q.Set("operation", "eq."+operation)
		}
		q.Set("order", "event_time.desc")
		q.Set("limit", fmt.Sprintf("%d", limit))

		endpoint := client.baseURL + "/audit_log?" + q.Encode()
		raw, err := client.getJSON(ctx, endpoint)
		if err != nil {
			// Nicer message when the schema hasn't been migrated to include
			// the audit_log table (older self-host Supabase installs).
			if strings.Contains(err.Error(), "PGRST205") || strings.Contains(err.Error(), "audit_log") {
				return nil, fmt.Errorf("native audit: audit_log table not present in this Supabase schema -- apply sql/migrations/004_audit_log.sql (or whichever introduced it) and retry")
			}
			return nil, err
		}
		var out []AuditEvent
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("native audit: parse: %w", err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("native audit: unknown backend")
}

// -----------------------------------------------------------------------
// Config (masked) for `ogham config show`

// MaskedConfig is a safe-to-print view of Config with secret values
// replaced by "<redacted>".
type MaskedConfig struct {
	Profile   string           `json:"profile"`
	Database  MaskedDatabase   `json:"database"`
	Embedding MaskedEmbedding  `json:"embedding"`
	Paths     map[string]string `json:"paths"`
}

type MaskedDatabase struct {
	Backend     string `json:"backend"`
	URL         string `json:"url,omitempty"`
	SupabaseURL string `json:"supabase_url,omitempty"`
	SupabaseKey string `json:"supabase_key,omitempty"`
}

type MaskedEmbedding struct {
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	Dimension int    `json:"dimension"`
	APIKey    string `json:"api_key,omitempty"`
}

// Mask returns a redacted view of cfg. URL passwords and all API keys
// are replaced with "<redacted>". Empty fields are left empty so the
// reader can see which settings are missing.
func Mask(cfg *Config) MaskedConfig {
	m := MaskedConfig{Profile: cfg.Profile}
	m.Database.Backend = cfg.Database.Backend
	if cfg.Database.URL != "" {
		m.Database.URL = redactURL(cfg.Database.URL)
	}
	m.Database.SupabaseURL = cfg.Database.SupabaseURL
	if cfg.Database.SupabaseKey != "" {
		m.Database.SupabaseKey = maskSecret(cfg.Database.SupabaseKey)
	}
	m.Embedding.Provider = cfg.Embedding.Provider
	m.Embedding.Model = cfg.Embedding.Model
	m.Embedding.Dimension = cfg.Embedding.Dimension
	if cfg.Embedding.APIKey != "" {
		m.Embedding.APIKey = maskSecret(cfg.Embedding.APIKey)
	}
	return m
}

// maskSecret shows the first 4 and last 4 chars to help users match
// against sources (e.g. "sb_secret_abcd…wxyz") without exposing the rest.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "<redacted>"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// -----------------------------------------------------------------------
// doAuthed is a generic authenticated request helper so Delete can issue
// a DELETE verb (no PostgREST convenience wrapper for that) without
// duplicating the auth-header boilerplate. Keeps the GET/POST helpers
// focused on their common cases.
func (c *supabaseClient) doAuthed(ctx context.Context, method, endpoint string, body []byte, extraHeaders map[string]string) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase %s %s: %w", method, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("supabase %s %s: read: %w", method, endpoint, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("supabase %s %s: http %d: %s", method, endpoint, resp.StatusCode, truncateForError(rawBody))
	}
	return rawBody, nil
}
