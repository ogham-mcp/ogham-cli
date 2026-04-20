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
// list_recent_memories signature.
type ListOptions struct {
	Limit  int
	Source string
	Tags   []string
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
