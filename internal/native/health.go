package native

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// CheckResult captures the outcome of a single parallel probe. Fields are
// named so JSON output is self-describing.
type CheckResult struct {
	Name     string        `json:"name"`
	OK       bool          `json:"ok"`
	Duration time.Duration `json:"duration_ns"`
	Detail   string        `json:"detail,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// Report is the aggregate return from HealthCheck. OK is true only if
// every probe succeeded.
type Report struct {
	OK       bool          `json:"ok"`
	Duration time.Duration `json:"duration_ns"`
	Backend  string        `json:"backend"`
	Provider string        `json:"provider,omitempty"`
	Checks   []CheckResult `json:"checks"`
}

// HealthCheck runs the configured probes in parallel. The whole report
// completes in roughly the time of the slowest probe.
//
// Probes currently included:
//   - backend: Supabase PostgREST HEAD or Postgres pgx ping
//   - embedder: config validation only by default (avoids a paid Gemini
//     call on every `ogham health`); opt in to the live API call via
//     opts.LiveEmbedder = true
func HealthCheck(ctx context.Context, cfg *Config, opts HealthOptions) (*Report, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native health: nil config")
	}

	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}

	report := &Report{Backend: backend, Provider: cfg.Embedding.Provider}

	start := time.Now()
	var mu sync.Mutex
	addResult := func(r CheckResult) {
		mu.Lock()
		defer mu.Unlock()
		report.Checks = append(report.Checks, r)
	}

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		addResult(checkBackend(ctx, cfg, backend))
		return nil
	})
	eg.Go(func() error {
		addResult(checkEmbedder(ctx, cfg, opts.LiveEmbedder))
		return nil
	})

	_ = eg.Wait()
	report.Duration = time.Since(start)
	report.OK = true
	for _, c := range report.Checks {
		if !c.OK {
			report.OK = false
			break
		}
	}
	// Stable order for output regardless of goroutine scheduling.
	sortChecksByName(report.Checks)
	return report, nil
}

// HealthOptions lets callers opt into live external calls.
type HealthOptions struct {
	// LiveEmbedder performs a real embedding API call if true. Costs
	// one provider token per run; defaults off so `ogham health` is free.
	LiveEmbedder bool
}

func checkBackend(ctx context.Context, cfg *Config, backend string) CheckResult {
	start := time.Now()
	switch backend {
	case "supabase":
		detail, err := pingSupabase(ctx, cfg)
		return finish("backend:supabase", start, detail, err)
	case "postgres":
		detail, err := pingPostgres(ctx, cfg)
		return finish("backend:postgres", start, detail, err)
	default:
		return finish("backend:"+backend, start, "", fmt.Errorf("unknown backend %q", backend))
	}
}

func pingSupabase(ctx context.Context, cfg *Config) (string, error) {
	if cfg.Database.SupabaseURL == "" || cfg.Database.SupabaseKey == "" {
		return "", fmt.Errorf("SUPABASE_URL/SUPABASE_KEY missing")
	}
	base := strings.TrimRight(cfg.Database.SupabaseURL, "/")
	// Cheapest authenticated read: request 0 rows.
	q := url.Values{}
	q.Set("select", "id")
	q.Set("limit", "0")
	endpoint := base + "/rest/v1/memories?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", cfg.Database.SupabaseKey)
	req.Header.Set("Authorization", "Bearer "+cfg.Database.SupabaseKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return base, nil
}

func pingPostgres(ctx context.Context, cfg *Config) (string, error) {
	if cfg.Database.URL == "" {
		return "", fmt.Errorf("DATABASE_URL missing")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(pingCtx, cfg.Database.URL)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(pingCtx) }()
	if err := conn.Ping(pingCtx); err != nil {
		return "", fmt.Errorf("ping: %w", err)
	}
	return redactURL(cfg.Database.URL), nil
}

func checkEmbedder(ctx context.Context, cfg *Config, live bool) CheckResult {
	start := time.Now()
	name := "embedder:" + cfg.Embedding.Provider

	embedder, err := NewEmbedder(cfg)
	if err != nil {
		return finish(name, start, "", err)
	}
	detail := embedder.Name()

	if !live {
		return finish(name, start, detail+" (config validated; pass --live-embedder for a real round-trip)", nil)
	}

	vec, err := embedder.Embed(ctx, "ogham health probe")
	if err != nil {
		return finish(name, start, detail, err)
	}
	return finish(name, start, fmt.Sprintf("%s dim=%d", detail, len(vec)), nil)
}

func finish(name string, start time.Time, detail string, err error) CheckResult {
	r := CheckResult{Name: name, Duration: time.Since(start), Detail: detail}
	if err != nil {
		r.Error = err.Error()
	} else {
		r.OK = true
	}
	return r
}

func sortChecksByName(checks []CheckResult) {
	// Small list -- insertion sort keeps the implementation dependency-free.
	for i := 1; i < len(checks); i++ {
		for j := i; j > 0 && checks[j-1].Name > checks[j].Name; j-- {
			checks[j-1], checks[j] = checks[j], checks[j-1]
		}
	}
}
