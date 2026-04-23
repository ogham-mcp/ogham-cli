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
)

// supabaseClient is a thin PostgREST client for the memories table and
// the Ogham-defined RPC functions. Parallel to the pgx path -- same
// result shapes, different transport.
type supabaseClient struct {
	baseURL    string
	apiKey     string
	http       *http.Client
	authScheme string // "Bearer" by default -- only Basic is ever needed for edge cases
}

func newSupabaseClient(cfg *Config) (*supabaseClient, error) {
	u := strings.TrimRight(cfg.Database.SupabaseURL, "/")
	if u == "" {
		return nil, fmt.Errorf("supabase: SUPABASE_URL not set")
	}
	if cfg.Database.SupabaseKey == "" {
		return nil, fmt.Errorf("supabase: SUPABASE_KEY / SUPABASE_SECRET_KEY not set")
	}
	if _, err := url.Parse(u); err != nil {
		return nil, fmt.Errorf("supabase: invalid SUPABASE_URL %q: %w", u, err)
	}
	return &supabaseClient{
		baseURL: u + "/rest/v1",
		apiKey:  cfg.Database.SupabaseKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// setAuth applies the standard Supabase header pair. Some routes expect
// both -- apikey for rate limiting / project identity, Authorization for
// the row-level-security role.
func (c *supabaseClient) setAuth(req *http.Request) {
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}

// callRPC POSTs to /rpc/{name} with the given named-argument body.
// PostgREST returns JSON: an array for table-returning RPCs, a bare
// value for scalar RPCs. We only deal with table-returning here.
func (c *supabaseClient) callRPC(ctx context.Context, name string, args map[string]any) ([]byte, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("supabase rpc %s: marshal: %w", name, err)
	}
	endpoint := c.baseURL + "/rpc/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase rpc %s: http: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("supabase rpc %s: read: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("supabase rpc %s: http %d: %s", name, resp.StatusCode, truncateForError(respBody))
	}
	return respBody, nil
}

// selectTable fetches a projection of the memories table via PostgREST
// query-string filters. Used by native list so Supabase users can run
// --native even without a direct Postgres connection.
func (c *supabaseClient) selectTable(ctx context.Context, query url.Values) ([]byte, error) {
	endpoint := c.baseURL + "/memories?" + query.Encode()
	return c.getJSON(ctx, endpoint)
}

// getJSON is a generic authenticated GET against PostgREST. Used by the
// table-level helpers (select, profile settings fetch, ...).
func (c *supabaseClient) getJSON(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase GET %s: http: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("supabase GET %s: read: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("supabase GET %s: http %d: %s", endpoint, resp.StatusCode, truncateForError(respBody))
	}
	return respBody, nil
}

// getJSONRange is a paginated GET. Sets Range: {start}-{end} so PostgREST
// returns that byte-slice of the result set; accepts 200 (full) or 206
// (partial). Callers use this to page past the managed-Supabase 1000-row
// per-request cap that prevents a single wide GET from covering the full
// active set on larger profiles.
func (c *supabaseClient) getJSONRange(ctx context.Context, endpoint string, start, end int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Range-Unit", "items")
	req.Header.Set("Range", fmt.Sprintf("%d-%d", start, end))
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase GET %s: http: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("supabase GET %s: read: %w", endpoint, err)
	}
	// 200 OK when the requested range covers the full set; 206 Partial
	// Content when the server truncates; 416 Range Not Satisfiable when
	// the start offset is past the end of the set (empty tail page).
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return respBody, nil
	case http.StatusRequestedRangeNotSatisfiable:
		return []byte("[]"), nil
	default:
		return nil, fmt.Errorf("supabase GET %s: http %d: %s", endpoint, resp.StatusCode, truncateForError(respBody))
	}
}

// postJSON is a generic authenticated POST. Takes an optional extraHeaders
// map so callers can pass Prefer headers (return=representation,
// resolution=merge-duplicates) for upsert semantics.
func (c *supabaseClient) postJSON(ctx context.Context, path string, body any, extraHeaders map[string]string) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("supabase POST %s: marshal: %w", path, err)
	}
	endpoint := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase POST %s: http: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("supabase POST %s: read: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("supabase POST %s: http %d: %s", path, resp.StatusCode, truncateForError(respBody))
	}
	return respBody, nil
}

// searchSupabase runs hybrid_search_memories via PostgREST RPC. Body
// shape matches Python's SupabaseBackend.hybrid_search_memories.
func searchSupabase(ctx context.Context, cfg *Config, query string, opts SearchOptions) ([]SearchResult, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}

	embedder, err := NewEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	embedding, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("native search: embed: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	profile := opts.Profile
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}

	args := map[string]any{
		"query_text":      query,
		"query_embedding": pgvectorLiteral(embedding),
		"match_count":     limit,
		"filter_profile":  profile,
	}
	if len(opts.Tags) > 0 {
		args["filter_tags"] = opts.Tags
	}
	if opts.Source != "" {
		args["filter_source"] = opts.Source
	}
	if len(opts.Profiles) > 0 {
		args["filter_profiles"] = opts.Profiles
	}

	raw, err := client.callRPC(ctx, "hybrid_search_memories", args)
	if err != nil {
		return nil, err
	}

	// PostgREST returns the table-returning RPC's rows as a plain JSON array.
	var rows []SearchResult
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native search: parse RPC response: %w (body: %s)", err, truncateForError(raw))
	}
	return rows, nil
}

// listSupabase fetches recent memories via PostgREST. Mirrors Python's
// list_recent_memories filters: profile + not-expired + optional source
// + optional tags + ORDER BY created_at.
func listSupabase(ctx context.Context, cfg *Config, opts ListOptions) ([]Memory, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}

	profile := cfg.Profile
	if profile == "" {
		profile = "default"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	q := url.Values{}
	q.Set("select", "id,content,tags,source,created_at")
	q.Set("profile", "eq."+profile)
	q.Set("or", "(expires_at.is.null,expires_at.gt.now())")
	q.Set("order", "created_at.desc")
	q.Set("limit", fmt.Sprintf("%d", limit))
	if opts.Source != "" {
		q.Set("source", "eq."+opts.Source)
	}
	if len(opts.Tags) > 0 {
		// PostgREST array-overlap: tags=ov.{a,b,c}. Mirrors the Python
		// backend's tags && filter_tags predicate.
		q.Set("tags", "ov.{"+strings.Join(opts.Tags, ",")+"}")
	}
	if !opts.Before.IsZero() {
		// PostgREST lt. filter takes an ISO-8601 timestamp. UTC here so
		// the server's local tz doesn't silently shift the cutoff.
		q.Set("created_at", "lt."+opts.Before.UTC().Format(time.RFC3339Nano))
	}
	if !opts.OnDate.IsZero() {
		day := opts.OnDate.UTC().Truncate(24 * time.Hour)
		// PostgREST allows multiple filters on the same column via
		// repeated keys -- Values.Add preserves both. gte+lt bucket.
		q.Add("created_at", "gte."+day.Format(time.RFC3339Nano))
		q.Add("created_at", "lt."+day.Add(24*time.Hour).Format(time.RFC3339Nano))
	}

	raw, err := client.selectTable(ctx, q)
	if err != nil {
		return nil, err
	}

	var rows []Memory
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native list: parse response: %w (body: %s)", err, truncateForError(raw))
	}
	return rows, nil
}
