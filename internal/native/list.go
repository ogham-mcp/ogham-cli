package native

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Memory is the native-mode view of a row from the memories table. Narrow
// on purpose -- native tools deliberately do not project every column;
// they ship the subset the CLI actually needs.
type Memory struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListOptions captures the optional filters for List. Mirrors Python's
// list_recent_memories signature with two dashboard-friendly additions:
//
//   - Before: if non-zero, restricts the result set to memories strictly
//     older than the given timestamp. Used by the Timeline view's HTMX
//     infinite scroll -- the client passes the oldest-visible created_at
//     back as `?before=...` and the server returns the next page.
//   - OnDate: if non-zero, restricts to memories whose created_at falls
//     on the same UTC calendar day as the given time. Used by the
//     Calendar heatmap drill-in (`/timeline?on=YYYY-MM-DD`).
//
// Before and OnDate are mutually exclusive in practice -- OnDate narrows
// to a single day, Before paginates. If both are set we apply both; the
// Timeline handler never sends both together.
type ListOptions struct {
	Limit  int
	Source string
	Tags   []string
	Before time.Time
	OnDate time.Time
}

// List returns the most recent memories for the given profile. Routes to
// the Supabase REST path or the pgx path depending on cfg.
func List(ctx context.Context, cfg *Config, opts ListOptions) ([]Memory, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native list: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native list: %w", err)
	}

	switch backend {
	case "supabase":
		return listSupabase(ctx, cfg, opts)
	case "postgres":
		return listPostgres(ctx, cfg, opts)
	default:
		return nil, fmt.Errorf("native list: unknown backend %q (expected supabase or postgres)", backend)
	}
}

func listPostgres(ctx context.Context, cfg *Config, opts ListOptions) ([]Memory, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "default"
	}

	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native list: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Build the WHERE clause incrementally so we only add filter
	// predicates that the caller actually requested.
	where := []string{
		"profile = $1",
		"(expires_at IS NULL OR expires_at > now())",
	}
	args := []any{profile}
	if opts.Source != "" {
		args = append(args, opts.Source)
		where = append(where, fmt.Sprintf("source = $%d", len(args)))
	}
	if len(opts.Tags) > 0 {
		args = append(args, opts.Tags)
		where = append(where, fmt.Sprintf("tags && $%d", len(args)))
	}
	if !opts.Before.IsZero() {
		args = append(args, opts.Before)
		where = append(where, fmt.Sprintf("created_at < $%d", len(args)))
	}
	if !opts.OnDate.IsZero() {
		// UTC day bucket: [floor_day, ceil_day). Independent of where
		// the server's local time zone lands. Timeline's ?on= query
		// always passes a UTC-normalised time.
		day := opts.OnDate.UTC().Truncate(24 * time.Hour)
		args = append(args, day)
		where = append(where, fmt.Sprintf("created_at >= $%d", len(args)))
		args = append(args, day.Add(24*time.Hour))
		where = append(where, fmt.Sprintf("created_at < $%d", len(args)))
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	sql := `SELECT id::text, content, coalesce(tags, '{}'::text[]), coalesce(source, ''), created_at
FROM memories
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY created_at DESC
LIMIT ` + limitPlaceholder

	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("native list: query: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Tags, &m.Source, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("native list: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("native list: rows: %w", err)
	}
	return out, nil
}
