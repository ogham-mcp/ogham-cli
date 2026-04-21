package native

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/ogham-mcp/ogham-cli/internal/native/extraction"
)

// Auto-link threshold. Matches Python ogham's settings.ogham_link_threshold
// default (0.85 dense-cosine after provider-scaled calibration). Above this,
// the top-N nearest memories become link candidates for the new row.
const autoLinkThreshold = 0.70

// Surprise fallback when no existing memory is close enough to compare
// against -- matches the Python "surprise unknown, default middle of band"
// fallback in src/ogham/service.py.
const surpriseFallback = 0.5

// StoreOptions captures the per-request parameters Store accepts.
// Nil/empty fields use the config default.
type StoreOptions struct {
	Tags    []string
	Source  string
	Profile string // empty -> cfg.Profile (which itself falls back to "default")
	// DryRun skips the actual DB write. Still runs extraction + embedding
	// + surprise so the caller sees what would happen. Useful for the
	// Day 4 --native-store-preview flag where we want to confirm the
	// pipeline is green without mutating the store.
	DryRun bool
}

// StoreResult is returned by Store. ID is empty when DryRun=true.
type StoreResult struct {
	ID         string         `json:"id,omitempty"`
	Profile    string         `json:"profile"`
	Tags       []string       `json:"tags"`
	Entities   []string       `json:"entities"`
	Dates      []string       `json:"dates"`
	Importance float64        `json:"importance"`
	Surprise   float64        `json:"surprise"`
	LinkedTo   []AutoLink     `json:"linked_to,omitempty"`
	Elapsed    time.Duration  `json:"elapsed"`
	DryRun     bool           `json:"dry_run,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// AutoLink is a prospective link candidate surfaced at store time.
// v0.5 preview does not write the memory_links row; that lands in a
// follow-up commit so the orchestrator + extraction + surprise path
// can ship independently.
type AutoLink struct {
	ID         string  `json:"id"`
	Similarity float64 `json:"similarity"`
	Content    string  `json:"content,omitempty"`
}

// Store is the Day 4 native orchestrator. Mirrors the shape of Python's
// store_memory tool:
//  1. Serial extraction (entities, dates, importance) -- ~microseconds
//  2. errgroup parallel:
//       - embedder.Embed(content)
//       - searchByText(content[:200])  used to compute surprise
//  3. surprise = 1.0 - max(similarity from step 2); default 0.5 on empty
//  4. auto-link: top-N above threshold become links in the result; the
//     actual INSERT into memory_links is deferred to the next commit
//  5. DB write via backend (postgres direct; supabase still goes through
//     the Python sidecar until a native PostgREST write lands)
//
// The returned StoreResult is safe to marshal to JSON; cmd/store.go
// emits it to --json users.
func Store(ctx context.Context, cfg *Config, content string, opts StoreOptions) (*StoreResult, error) {
	start := time.Now()
	if cfg == nil {
		return nil, fmt.Errorf("native store: nil config")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("native store: empty content")
	}

	// Serial extraction: runs in ~100us for typical paragraph input.
	// Parallelising buys nothing and adds goroutine overhead we'd rather
	// spend on the HTTP embed call.
	entities := extraction.Entities(content)
	dates := extraction.DatesAt(content, time.Now())
	importance := extraction.Importance(content, opts.Tags)

	profile := opts.Profile
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}

	embedder, err := NewEmbedder(cfg)
	if err != nil {
		return nil, fmt.Errorf("native store: %w", err)
	}

	// Parallel fan-out. Both legs depend only on content, not on each
	// other, so errgroup cuts end-to-end latency by ~200 ms (the search
	// path roughly overlaps the embed call).
	var (
		embedding []float32
		neighbors []SearchResult
	)
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		v, err := embedder.Embed(egCtx, content)
		if err != nil {
			return fmt.Errorf("embed: %w", err)
		}
		embedding = v
		return nil
	})
	eg.Go(func() error {
		// Truncate to the first ~200 chars so the search query is short
		// enough for the embedder but representative of the content.
		// Mirrors Python's service.py:152 slice.
		probe := content
		if len(probe) > 200 {
			probe = probe[:200]
		}
		n, err := Search(egCtx, cfg, probe, SearchOptions{
			Limit:   5,
			Profile: profile,
		})
		// Search errors are not fatal at store time: if the backend is
		// temporarily unreachable, we can still store without a surprise
		// signal. Log via return-and-ignore so the fork group doesn't
		// cancel the embed leg.
		if err != nil {
			return nil
		}
		neighbors = n
		return nil
	})
	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("native store: %w", err)
	}

	surprise := computeSurprise(neighbors)
	links := pickAutoLinks(neighbors, autoLinkThreshold, 3)

	// Merge extracted tag artefacts into the caller's tag set. Python
	// ogham uses the same prefixes (entity:/file:/person:/location:/
	// date:) and the same dedup behaviour.
	allTags := mergeTags(opts.Tags, entities, dates)

	metadata := map[string]any{}
	if len(dates) > 0 {
		metadata["dates"] = dates
	}

	result := &StoreResult{
		Profile:    profile,
		Tags:       allTags,
		Entities:   entities,
		Dates:      dates,
		Importance: importance,
		Surprise:   surprise,
		LinkedTo:   links,
		DryRun:     opts.DryRun,
		Metadata:   metadata,
	}

	if opts.DryRun {
		result.Elapsed = time.Since(start)
		return result, nil
	}

	// Real DB write path.
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native store: %w", err)
	}
	write := storeWrite{
		Content:    content,
		Embedding:  embedding,
		Source:     opts.Source,
		Profile:    profile,
		Tags:       allTags,
		Importance: importance,
		Surprise:   surprise,
		Metadata:   metadata,
	}
	switch backend {
	case "postgres":
		id, err := writeMemoryPostgres(ctx, cfg, write)
		if err != nil {
			return nil, fmt.Errorf("native store: write: %w", err)
		}
		result.ID = id
	case "supabase":
		id, err := writeMemorySupabase(ctx, cfg, write)
		if err != nil {
			return nil, fmt.Errorf("native store: write: %w", err)
		}
		result.ID = id
	default:
		return nil, fmt.Errorf("native store: unknown backend %q", backend)
	}

	result.Elapsed = time.Since(start)
	return result, nil
}

// storeWrite holds everything a backend INSERT needs. Keeps the backend
// call sites tidy and makes the parameter set greppable.
type storeWrite struct {
	Content    string
	Embedding  []float32
	Source     string
	Profile    string
	Tags       []string
	Importance float64
	Surprise   float64
	Metadata   map[string]any
}

// writeMemorySupabase INSERTs the new row via PostgREST (/rest/v1/memories)
// and returns the new row's uuid. Mirrors Python's SupabaseBackend.
// store_memory at src/ogham/backends/supabase.py:50 -- the embedding
// is sent as pgvector's text literal ("[0.1,0.2,...]") so pgvector
// parses it into vector(N) without needing a binary codec.
//
// `Prefer: return=representation` asks PostgREST to echo the inserted
// row (including the server-generated uuid) in the response body so we
// don't have to make a second round-trip to look it up.
func writeMemorySupabase(ctx context.Context, cfg *Config, m storeWrite) (string, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return "", fmt.Errorf("supabase client: %w", err)
	}

	row := map[string]any{
		"content":    m.Content,
		"embedding":  pgvectorLiteral(m.Embedding),
		"profile":    m.Profile,
		"tags":       m.Tags,
		"importance": m.Importance,
		"surprise":   m.Surprise,
	}
	if m.Source != "" {
		row["source"] = m.Source
	}
	if len(m.Metadata) > 0 {
		row["metadata"] = m.Metadata
	} else {
		row["metadata"] = map[string]any{}
	}

	// PostgREST accepts a single object OR an array. Using an array
	// matches the shape supabase-py ultimately emits and makes the
	// response parsing uniform with store_memories_batch if we add it.
	body := []map[string]any{row}
	headers := map[string]string{
		"Prefer": "return=representation",
	}
	raw, err := client.postJSON(ctx, "/memories", body, headers)
	if err != nil {
		return "", err
	}

	// PostgREST returns [{row}]; pull the id back out.
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return "", fmt.Errorf("supabase insert: decode response: %w (body: %s)", err, truncateForError(raw))
	}
	if len(rows) == 0 || rows[0].ID == "" {
		return "", fmt.Errorf("supabase insert: no id returned (body: %s)", truncateForError(raw))
	}
	return rows[0].ID, nil
}

// writeMemoryPostgres INSERTs the new row and returns the generated uuid.
// Only the columns that matter are supplied; the schema defaults cover
// access_count, confidence, created_at, etc.
func writeMemoryPostgres(ctx context.Context, cfg *Config, m storeWrite) (string, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var metadataJSON []byte
	if len(m.Metadata) > 0 {
		metadataJSON, err = json.Marshal(m.Metadata)
		if err != nil {
			return "", fmt.Errorf("marshal metadata: %w", err)
		}
	}

	// Source is nullable; pass a real nil if the caller didn't set one.
	var sourceArg any
	if m.Source != "" {
		sourceArg = m.Source
	}

	const sql = `
INSERT INTO memories (content, embedding, source, profile, tags, importance, surprise, metadata)
VALUES ($1, $2::vector, $3, $4, $5, $6, $7, COALESCE($8::jsonb, '{}'::jsonb))
RETURNING id::text`

	var id string
	err = conn.QueryRow(ctx, sql,
		m.Content,
		pgvectorLiteral(m.Embedding),
		sourceArg,
		m.Profile,
		m.Tags,
		m.Importance,
		m.Surprise,
		metadataJSON,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert: %w", err)
	}
	return id, nil
}

// computeSurprise returns 1.0 - max_sim from the candidate set, clamped
// to [0, 1]. Empty set -> surpriseFallback so the column default doesn't
// bias toward "unusual" when we simply don't know.
func computeSurprise(neighbors []SearchResult) float64 {
	if len(neighbors) == 0 {
		return surpriseFallback
	}
	maxSim := 0.0
	for _, n := range neighbors {
		if n.Similarity > maxSim {
			maxSim = n.Similarity
		}
	}
	s := 1.0 - maxSim
	switch {
	case s < 0:
		return 0
	case s > 1:
		return 1
	default:
		return s
	}
}

// pickAutoLinks returns the top-N neighbors whose similarity exceeds
// threshold, sorted descending by similarity. The actual INSERT into
// memory_links is deferred to a follow-up commit; surfacing candidates
// here lets the --native-store-preview caller see what would link.
func pickAutoLinks(neighbors []SearchResult, threshold float64, n int) []AutoLink {
	var picks []AutoLink
	for _, m := range neighbors {
		if m.Similarity >= threshold {
			picks = append(picks, AutoLink{
				ID:         m.ID,
				Similarity: m.Similarity,
				Content:    m.Content,
			})
		}
	}
	sort.Slice(picks, func(i, j int) bool {
		return picks[i].Similarity > picks[j].Similarity
	})
	if len(picks) > n {
		picks = picks[:n]
	}
	return picks
}

// mergeTags combines caller tags + entity tags + date:YYYY-MM-DD tags
// into a single dedup'd slice. Entity tags already carry their typed
// prefix ("entity:", "file:", "person:", "location:") from the
// extraction package; dates get a "date:" prefix here for parity with
// Python's src/ogham/service.py store path.
func mergeTags(callerTags, entityTags, dates []string) []string {
	seen := make(map[string]struct{}, len(callerTags)+len(entityTags)+len(dates))
	var out []string
	appendIfNew := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, t := range callerTags {
		appendIfNew(t)
	}
	for _, t := range entityTags {
		appendIfNew(t)
	}
	for _, d := range dates {
		appendIfNew("date:" + d)
	}
	sort.Strings(out)
	return out
}
