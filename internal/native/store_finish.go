package native

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Issue #58. After the INSERT, the Python store path calls the server's
// auto_link_memory() and emits an audit `store` event. The native path did
// neither: it computed link candidates, returned them as if they had been
// written, and wrote nothing. Because hook capture routes through this path,
// it became the dominant writer and auto-linking effectively stopped.
//
// Linking is delegated to the same database function, with the same
// parameters, so the two writers cannot diverge again. It is NOT
// reimplemented here.

// shouldAutoLink reports whether a write from source joins the knowledge
// graph. Tool-call captures (hook:*) do not: thousands of them per week
// would swamp spreading activation and the graph-centrality boost.
func shouldAutoLink(source string) bool {
	return !strings.HasPrefix(source, "hook:")
}

// auditModelLabel turns an Embedder.Name() ("gemini/gemini-embedding-2",
// optionally "+cache") into the "provider:model" form the Python path
// writes, so audit rows from both writers read the same.
func auditModelLabel(embedderName string) string {
	name := strings.TrimSuffix(embedderName, "+cache")
	return strings.Replace(name, "/", ":", 1)
}

// finishStore runs the post-insert steps. Neither may fail the store: the
// row is already written, and an error here would make the caller retry and
// duplicate it. A link failure is put on the result and logged; an audit
// failure is logged -- the Python path treats audit as best-effort too.
func finishStore(ctx context.Context, cfg *Config, backend string, res *StoreResult, embedding []float32, source, embedderName string) {
	link := shouldAutoLink(source)
	audit := map[string]any{
		"importance": res.Importance,
		"surprise":   res.Surprise,
		// Source labels collide (a native write from Claude Code is labelled
		// "claude-code", same as the Python path), so name the writer.
		"client": "ogham-cli",
	}
	model := auditModelLabel(embedderName)

	var out finishOutcome
	switch backend {
	case "postgres":
		out = finishStorePostgres(ctx, cfg, res, embedding, source, model, audit, link)
	case "supabase":
		out = finishStoreSupabase(ctx, cfg, res, embedding, source, model, audit, link)
	default:
		return
	}

	if out.linkErr != nil {
		res.LinkError = out.linkErr.Error()
		slog.Warn("native store: auto-link failed; memory was written", "id", res.ID, "err", out.linkErr)
	} else {
		res.LinkedTo = out.linked
	}
	if out.auditErr != nil {
		slog.Warn("native store: audit event failed; memory was written", "id", res.ID, "err", out.auditErr)
	}
}

// finishOutcome carries the two post-insert results. They fail
// independently, so each keeps its own error.
type finishOutcome struct {
	linked   []AutoLink
	linkErr  error
	auditErr error
}

func finishStorePostgres(ctx context.Context, cfg *Config, res *StoreResult, embedding []float32, source, model string, audit map[string]any, link bool) finishOutcome {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		err = fmt.Errorf("connect: %w", err)
		return finishOutcome{linkErr: maybe(link, err), auditErr: err}
	}
	defer func() { _ = conn.Close(ctx) }()

	var linked []AutoLink
	var linkErr error
	if link {
		linked, linkErr = autoLinkPostgres(ctx, conn, res.ID, embedding, res.Profile)
	}

	var sourceArg any
	if source != "" {
		sourceArg = source
	}
	meta, _ := json.Marshal(audit)
	_, auditErr := conn.Exec(ctx, `
INSERT INTO audit_log (profile, operation, resource_id, outcome, source, embedding_model, metadata)
VALUES ($1, 'store', $2::uuid, 'success', $3, $4, $5::jsonb)`,
		res.Profile, res.ID, sourceArg, model, meta)
	if auditErr != nil {
		auditErr = fmt.Errorf("insert audit_log: %w", auditErr)
	}
	return finishOutcome{linked: linked, linkErr: linkErr, auditErr: auditErr}
}

// autoLinkPostgres calls the server's auto_link_memory() and returns the
// edges it actually wrote.
func autoLinkPostgres(ctx context.Context, conn *pgx.Conn, id string, embedding []float32, profile string) ([]AutoLink, error) {
	// Five explicitly typed arguments pin the current overload,
	// auto_link_memory(uuid, vector, float8, int, text). Some deployed
	// databases still carry a stale four-argument overload that inserts into
	// a column that no longer exists.
	if _, err := conn.Exec(ctx,
		`SELECT auto_link_memory($1::uuid, $2::vector, $3::float8, $4::int, $5::text)`,
		id, pgvectorLiteral(embedding), autoLinkThreshold, autoLinkMaxLinks, profile); err != nil {
		return nil, fmt.Errorf("auto_link_memory: %w", err)
	}
	rows, err := conn.Query(ctx, `
SELECT target_id::text, strength::float8 FROM memory_relationships
 WHERE source_id = $1::uuid AND relationship = 'similar'
 ORDER BY strength DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("read written links: %w", err)
	}
	links, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AutoLink, error) {
		var l AutoLink
		err := r.Scan(&l.ID, &l.Similarity)
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("read written links: %w", err)
	}
	return links, nil
}

func finishStoreSupabase(ctx context.Context, cfg *Config, res *StoreResult, embedding []float32, source, model string, audit map[string]any, link bool) finishOutcome {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		err = fmt.Errorf("supabase client: %w", err)
		return finishOutcome{linkErr: maybe(link, err), auditErr: err}
	}

	var linked []AutoLink
	var linkErr error
	if link {
		linked, linkErr = autoLinkSupabase(ctx, client, res.ID, embedding, res.Profile)
	}

	row := map[string]any{
		"profile":         res.Profile,
		"operation":       "store",
		"resource_id":     res.ID,
		"outcome":         "success",
		"embedding_model": model,
		"metadata":        audit,
	}
	if source != "" {
		row["source"] = source
	}
	var auditErr error
	if _, err := client.postJSON(ctx, "/audit_log", row, nil); err != nil {
		auditErr = fmt.Errorf("insert audit_log: %w", err)
	}
	return finishOutcome{linked: linked, linkErr: linkErr, auditErr: auditErr}
}

// autoLinkSupabase is autoLinkPostgres over PostgREST. Named arguments, as
// the Python Supabase backend sends them, select the current overload.
func autoLinkSupabase(ctx context.Context, client *supabaseClient, id string, embedding []float32, profile string) ([]AutoLink, error) {
	if _, err := client.callRPC(ctx, "auto_link_memory", map[string]any{
		"new_memory_id":  id,
		"new_embedding":  pgvectorLiteral(embedding),
		"link_threshold": autoLinkThreshold,
		"max_links":      autoLinkMaxLinks,
		"filter_profile": profile,
	}); err != nil {
		return nil, fmt.Errorf("auto_link_memory: %w", err)
	}
	q := url.Values{
		"source_id":    {"eq." + id},
		"relationship": {"eq.similar"},
		"select":       {"target_id,strength"},
		"order":        {"strength.desc"},
	}
	raw, err := client.getJSON(ctx, client.baseURL+"/memory_relationships?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("read written links: %w", err)
	}
	var rows []struct {
		TargetID string  `json:"target_id"`
		Strength float64 `json:"strength"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("read written links: %w", err)
	}
	links := make([]AutoLink, 0, len(rows))
	for _, r := range rows {
		links = append(links, AutoLink{ID: r.TargetID, Similarity: r.Strength})
	}
	return links, nil
}

// maybe returns err when the step it guards was going to run, else nil, so
// a connection failure is not reported as a link failure for a hook write
// that was never going to link.
func maybe(ran bool, err error) error {
	if ran {
		return err
	}
	return nil
}
