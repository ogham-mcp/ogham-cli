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

// UpdateOptions captures per-request update semantics. Pointer / nil-slice
// semantics let callers distinguish "field omitted" from "explicit clear":
//
//	Content  == nil        -> leave content untouched
//	Content  != nil        -> replace content (and re-embed)
//	Tags     == nil        -> leave tags untouched
//	Tags     != nil, len 0 -> clear tags (PostgreSQL empty array)
//	Metadata == nil        -> leave metadata untouched
//	Metadata != nil, len 0 -> clear metadata (jsonb '{}')
//
// Matches the Python update_memory tool contract (None vs [] vs {}).
type UpdateOptions struct {
	Content  *string
	Tags     []string
	Metadata map[string]any
	// Profile override; empty -> cfg.Profile -> "default".
	Profile string
}

// UpdateResult is returned on success. FieldsUpdated is the list of
// column names that were actually written, so callers can log audit.
type UpdateResult struct {
	ID            string    `json:"id"`
	Profile       string    `json:"profile"`
	UpdatedAt     time.Time `json:"updated_at"`
	FieldsUpdated []string  `json:"fields_updated"`
	ReEmbedded    bool      `json:"re_embedded"`
}

// Update re-writes an existing memory. When opts.Content is non-nil the
// content is re-embedded via the configured embedder before the row is
// written -- the old embedding would poison future similarity search
// if we left it behind.
//
// Returns an error when no fields are specified for update (parity with
// Python: raise ValueError("No updates provided")).
func Update(ctx context.Context, cfg *Config, id string, opts UpdateOptions) (*UpdateResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native update: nil config")
	}
	if id == "" {
		return nil, fmt.Errorf("native update: memory id required")
	}

	// Build the set of fields the caller wants to change. Order matters
	// for deterministic test output: keep it alphabetical.
	var fields []string
	if opts.Content != nil {
		fields = append(fields, "content", "embedding")
	}
	if opts.Metadata != nil {
		fields = append(fields, "metadata")
	}
	if opts.Tags != nil {
		fields = append(fields, "tags")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("native update: no fields specified (pass content, tags, or metadata)")
	}

	profile := opts.Profile
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}

	// Re-embed first so the DB never sees a transient mismatch between
	// content and embedding -- if the embedder fails we abort before the
	// UPDATE runs.
	var embedding []float32
	if opts.Content != nil {
		embedder, err := NewEmbedder(cfg)
		if err != nil {
			return nil, fmt.Errorf("native update: %w", err)
		}
		v, err := embedder.Embed(ctx, *opts.Content)
		if err != nil {
			return nil, fmt.Errorf("native update: re-embed: %w", err)
		}
		embedding = v
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		return updatePostgres(ctx, cfg, id, profile, opts, embedding, fields)
	case "supabase":
		return updateSupabase(ctx, cfg, id, profile, opts, embedding, fields)
	default:
		return nil, fmt.Errorf("native update: unknown backend %q", backend)
	}
}

func updatePostgres(ctx context.Context, cfg *Config, id, profile string, opts UpdateOptions, embedding []float32, fields []string) (*UpdateResult, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native update: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Build parameterised SET clause. pgx is safe against injection at
	// the placeholder boundary; we build positional args in fields order
	// so the test can assert the SQL shape without string matching on
	// random ordering.
	var setParts []string
	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if opts.Content != nil {
		setParts = append(setParts,
			"content = "+next(*opts.Content),
			"embedding = "+next(pgvectorLiteral(embedding))+"::vector")
	}
	if opts.Metadata != nil {
		raw, err := json.Marshal(opts.Metadata)
		if err != nil {
			return nil, fmt.Errorf("native update: marshal metadata: %w", err)
		}
		setParts = append(setParts, "metadata = "+next(raw)+"::jsonb")
	}
	if opts.Tags != nil {
		// Force non-nil slice so pgx writes '{}' instead of NULL on clear.
		tags := opts.Tags
		if tags == nil {
			tags = []string{}
		}
		setParts = append(setParts, "tags = "+next(tags))
	}
	// updated_at is always touched; schema may or may not have a trigger
	// for it, so we set it explicitly for parity across installs.
	setParts = append(setParts, "updated_at = now()")

	idPh := next(id)
	profilePh := next(profile)
	sql := fmt.Sprintf(
		"UPDATE memories SET %s WHERE id = %s::uuid AND profile = %s RETURNING id::text, updated_at",
		strings.Join(setParts, ", "), idPh, profilePh)

	var outID string
	var updatedAt time.Time
	err = conn.QueryRow(ctx, sql, args...).Scan(&outID, &updatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("no memory with id %q in profile %q", id, profile)
		}
		return nil, fmt.Errorf("native update: exec: %w", err)
	}
	return &UpdateResult{
		ID:            outID,
		Profile:       profile,
		UpdatedAt:     updatedAt,
		FieldsUpdated: fields,
		ReEmbedded:    opts.Content != nil,
	}, nil
}

func updateSupabase(ctx context.Context, cfg *Config, id, profile string, opts UpdateOptions, embedding []float32, fields []string) (*UpdateResult, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}

	// PostgREST PATCH body: only the columns we're changing.
	body := map[string]any{}
	if opts.Content != nil {
		body["content"] = *opts.Content
		body["embedding"] = pgvectorLiteral(embedding)
	}
	if opts.Metadata != nil {
		body["metadata"] = opts.Metadata
	}
	if opts.Tags != nil {
		// Supabase PostgREST accepts JSON arrays directly for text[] columns.
		tags := opts.Tags
		if tags == nil {
			tags = []string{}
		}
		body["tags"] = tags
	}
	body["updated_at"] = time.Now().UTC().Format(time.RFC3339)

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("native update: marshal: %w", err)
	}

	q := url.Values{}
	q.Set("id", "eq."+id)
	q.Set("profile", "eq."+profile)
	q.Set("select", "id,updated_at")
	endpoint := client.baseURL + "/memories?" + q.Encode()

	respRaw, err := client.doAuthed(ctx, http.MethodPatch, endpoint, raw,
		map[string]string{"Prefer": "return=representation"})
	if err != nil {
		return nil, err
	}

	var rows []struct {
		ID        string    `json:"id"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(respRaw, &rows); err != nil {
		return nil, fmt.Errorf("native update: parse: %w (body: %s)", err, truncateForError(respRaw))
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no memory with id %q in profile %q", id, profile)
	}
	return &UpdateResult{
		ID:            rows[0].ID,
		Profile:       profile,
		UpdatedAt:     rows[0].UpdatedAt,
		FieldsUpdated: fields,
		ReEmbedded:    opts.Content != nil,
	}, nil
}
