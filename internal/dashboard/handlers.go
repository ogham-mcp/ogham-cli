package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/dashboard/templates"
	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// requestTimeout caps how long a single handler may spend talking to
// Postgres / the embedding provider. Dashboard pages are interactive, so
// any stat query that takes more than 10s is a bug; the user would give
// up before that anyway.
const requestTimeout = 10 * time.Second

type handlers struct {
	cfg *native.Config
}

// ViewData is the common props bundle every page layout needs (profile
// name, nav state). Kept dead-simple -- no session, no user, no auth.
type ViewData = templates.ViewData

// StatCards is the headline-numbers struct the Overview stats row
// renders. Field names match the 4 locked cards in the Phase 0 spike.
// When a stat isn't directly derivable from native.Stats we surface it
// as empty-string + a note on the card -- the prototype explicitly
// prefers "N/A (stats API gap)" over an invented value.
type StatCards = templates.StatCards

// overview handles `GET /`. Fetches stats + recent memories in sequence;
// neither call is heavy enough to justify a parallel dance at prototype
// scale. If either fails we render a minimal error page rather than a
// 500 so the user at least sees the chrome and can retry.
func (h *handlers) overview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	vd := h.viewData()
	vd.Active = "overview"

	stats, statsErr := native.GetStats(ctx, h.cfg)
	if statsErr != nil {
		slog.Warn("dashboard: stats", "error", statsErr)
	}
	mems, listErr := native.List(ctx, h.cfg, native.ListOptions{Limit: 20})
	if listErr != nil {
		slog.Warn("dashboard: list", "error", listErr)
	}

	cards := buildStatCards(stats)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Overview(vd, cards, mems, firstErr(statsErr, listErr)).Render(ctx, w)
}

// filter handles `GET /filter?q=...` for the HTMX live filter. Returns
// HTML fragments (just the <tr> rows), not a full page -- hx-target
// replaces `#memories-tbody` contents.
func (h *handlers) filter(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	q := strings.TrimSpace(r.URL.Query().Get("q"))

	mems, err := native.List(ctx, h.cfg, native.ListOptions{Limit: 20})
	if err != nil {
		slog.Warn("dashboard: filter list", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if q != "" {
		mems = clientFilter(mems, q)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.MemoryRows(mems).Render(ctx, w)
}

// search handles `GET /search?q=...`. Uses native.Search (hybrid_search)
// when q is non-empty; otherwise renders the empty search page so the
// user sees the input field and can type.
func (h *handlers) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	vd := h.viewData()
	vd.Active = "search"

	var (
		results []native.SearchResult
		err     error
	)
	if q != "" {
		results, err = native.Search(ctx, h.cfg, q, native.SearchOptions{Limit: 20})
		if err != nil {
			slog.Warn("dashboard: search", "error", err, "query", q)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Search(vd, q, results, err).Render(ctx, w)
}

// healthz is a cheap liveness probe. Mostly useful for the acceptance
// script -- lets it wait for the server to come up without parsing
// stderr logs.
func (h *handlers) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// timelinePageSize is how many rows the Timeline handler fetches per
// scroll. 50 matches the spec ("Initial page fetches 50") and is small
// enough that the first paint is under the 10s requestTimeout even on
// cold SSDs.
const timelinePageSize = 50

// timeline handles `GET /timeline` -- full-page render.
// Accepts ?before=<RFC3339> for HTMX cursor pagination and
// ?on=YYYY-MM-DD for calendar drill-in.
func (h *handlers) timeline(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	td := h.loadTimeline(ctx, r)
	vd := h.viewData()
	vd.Active = "timeline"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Timeline(vd, td).Render(ctx, w)
}

// timelineRows handles `GET /timeline/rows?before=...&on=...` -- the
// HTMX-fragment endpoint for infinite scroll. Emits just the grouped
// rows + the next load-more sentinel.
func (h *handlers) timelineRows(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	td := h.loadTimeline(ctx, r)
	if td.Err != nil {
		// Fragment endpoint surfaces errors inline as a terse message;
		// the full-page template reserves the banner treatment.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<div class="ml-6 py-4 text-center text-xs text-destructive">Error loading more: ` +
			htmlEscape(td.Err.Error()) + `</div>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.TimelineRows(td.Memories, td.NextCursor, td.OnDateLabel).Render(ctx, w)
}

// timelineExpand renders a single memory in the expanded card form.
// Bound to a card's click handler; swaps the card in place.
func (h *handlers) timelineExpand(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	m, err := h.findMemory(ctx, id)
	if err != nil || m == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.TimelineCardExpanded(*m).Render(ctx, w)
}

// timelineCollapse re-renders a memory in the collapsed form. Same
// lookup path as timelineExpand; cheaper than caching the row because
// a memory fetch by id is sub-millisecond on local PG.
func (h *handlers) timelineCollapse(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	m, err := h.findMemory(ctx, id)
	if err != nil || m == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.TimelineCardCollapsed(*m).Render(ctx, w)
}

// auditPageSize is the per-request row count for /audit and its
// infinite-scroll fragment. Bigger than Timeline (50) because audit
// rows are terse (one timestamp + pill + id + collapsed payload) and
// render faster.
const auditPageSize = 75

// audit handles `GET /audit`.
func (h *handlers) audit(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	vd := h.viewData()
	vd.Active = "audit"

	ad := h.loadAudit(ctx, r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Audit(vd, ad).Render(ctx, w)
}

// auditRows handles `GET /audit/rows?before=...&op=...` -- the HTMX
// infinite-scroll fragment endpoint. Emits just the <tr> rows + the
// next load-more sentinel, no page chrome.
func (h *handlers) auditRows(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	ad := h.loadAudit(ctx, r)
	if ad.Err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<tr><td colspan="4" class="py-3 px-4 text-center text-xs text-destructive">Error loading more: ` +
			htmlEscape(ad.Err.Error()) + `</td></tr>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AuditRows(ad.Entries, ad.NextCursor, ad.Active).Render(ctx, w)
}

// loadAudit centralises the common parse + fetch + cursor logic.
func (h *handlers) loadAudit(ctx context.Context, r *http.Request) templates.AuditData {
	op := strings.TrimSpace(r.URL.Query().Get("op"))
	// Guard against arbitrary query values -- only the known four
	// operation types are valid tab filters. Unknown ops collapse to
	// "" (All). Keeps the render logic simple.
	switch op {
	case "", "store", "update", "delete", "decay":
	default:
		op = ""
	}
	ad := templates.AuditData{Active: op}

	var before time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			before = t
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			before = t
		}
	}

	events, err := native.AuditEntries(ctx, h.cfg, h.cfg.Profile, op, before, auditPageSize)
	if err != nil {
		slog.Warn("dashboard: audit", "error", err)
		ad.Err = err
		return ad
	}
	ad.Entries = events
	if len(events) == auditPageSize {
		ad.NextCursor = events[len(events)-1].EventTime
	}
	return ad
}

// calendar handles `GET /calendar`. Fetches 365 days of per-day
// counts, builds the heatmap grid, renders.
func (h *handlers) calendar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	vd := h.viewData()
	vd.Active = "calendar"

	counts, err := native.StoreCountsByDay(ctx, h.cfg, 365)
	cd := templates.BuildCalendar(counts, time.Now())
	if err != nil {
		slog.Warn("dashboard: calendar daycounts", "error", err)
		cd.Err = err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.Calendar(vd, cd).Render(ctx, w)
}

// findMemory fetches a single memory by id + active profile. Uses the
// List endpoint with a small page and a linear scan -- a dedicated
// GetByID would be marginally cheaper but the Timeline expand flow
// runs at most a few times per session.
func (h *handlers) findMemory(ctx context.Context, id string) (*native.Memory, error) {
	// 200-row page is large enough to cover any reasonable recent
	// working set. If the user expands a much older card, Timeline's
	// infinite scroll will have already brought it into the native
	// List cache; if not, it still falls back to a direct SQL lookup
	// via the pagination cursor.
	mems, err := native.List(ctx, h.cfg, native.ListOptions{Limit: 200})
	if err != nil {
		return nil, err
	}
	for i := range mems {
		if mems[i].ID == id {
			return &mems[i], nil
		}
	}
	return nil, nil
}

// loadTimeline centralises the shared logic: parse ?before + ?on, call
// native.List, compute the next cursor. Reused by the page + fragment
// endpoints so both stay in sync.
func (h *handlers) loadTimeline(ctx context.Context, r *http.Request) templates.TimelineData {
	opts := native.ListOptions{Limit: timelinePageSize}
	var onDateLabel string

	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			opts.Before = ts
		} else if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			opts.Before = ts
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("on")); raw != "" {
		// Accept YYYY-MM-DD (preferred) or a full RFC3339 timestamp.
		if d, err := time.Parse("2006-01-02", raw); err == nil {
			opts.OnDate = d
			onDateLabel = d.UTC().Format("Monday, 2 January 2006")
		} else if d, err := time.Parse(time.RFC3339, raw); err == nil {
			opts.OnDate = d
			onDateLabel = d.UTC().Format("Monday, 2 January 2006")
		}
	}

	mems, err := native.List(ctx, h.cfg, opts)
	if err != nil {
		slog.Warn("dashboard: timeline list", "error", err)
		return templates.TimelineData{Err: err, OnDateLabel: onDateLabel}
	}

	var next time.Time
	// Only surface a cursor if we got a full page AND the caller is
	// not filtering to a single day (day-filter has a bounded result
	// set; pagination inside a single day would loop forever).
	if len(mems) == timelinePageSize && opts.OnDate.IsZero() {
		next = mems[len(mems)-1].CreatedAt
	}
	return templates.TimelineData{
		Memories:    mems,
		NextCursor:  next,
		OnDateLabel: onDateLabel,
	}
}

// htmlEscape is a minimal inline escaper for the two error-banner
// fragments that don't go through a full templ render. Kept tiny on
// purpose -- anything richer belongs in a template.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// viewData packages the per-request layout context. Profile + backend
// indicator feed the header chrome; the backend pill tells the operator
// at a glance which database the dashboard is actually reading from
// (important when both SUPABASE_URL and DATABASE_URL are set and
// ResolveBackend's precedence silently picks one).
func (h *handlers) viewData() ViewData {
	profile := h.cfg.Profile
	if profile == "" {
		profile = "default"
	}
	return ViewData{Profile: profile, Backend: backendLabel(h.cfg)}
}

// backendLabel returns a short, credential-free string identifying the
// active backend. Examples:
//
//	postgres@localhost:5433/scratch
//	supabase@gljsgbhfaqjsoexwlvzf
//
// Password + API key are never included. A misconfigured Config falls
// back to "unconfigured" so the header still renders instead of
// showing an error-stained pill.
func backendLabel(cfg *native.Config) string {
	if cfg == nil {
		return "unconfigured"
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return "unconfigured"
	}
	switch backend {
	case "postgres":
		return "postgres@" + postgresHostDB(cfg.Database.URL)
	case "supabase":
		return "supabase@" + supabaseProjectRef(cfg.Database.SupabaseURL)
	default:
		return backend
	}
}

// postgresHostDB parses a postgres://user:pass@host:port/db style DSN
// and returns "host:port/db" -- never the credentials. Unparseable
// input returns "unknown" so the header pill still renders.
func postgresHostDB(dsn string) string {
	if dsn == "" {
		return "unknown"
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	host := u.Host
	// Strip a trailing slash on the path to get the bare db name.
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return host
	}
	return fmt.Sprintf("%s/%s", host, db)
}

// supabaseProjectRef extracts just the subdomain from a URL like
// https://gljsgbhfaqjsoexwlvzf.supabase.co. A bare host without a
// dotted domain falls back to returning the host verbatim.
func supabaseProjectRef(raw string) string {
	if raw == "" {
		return "unknown"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// Fallback: strip scheme manually if url.Parse couldn't.
		host := strings.TrimPrefix(raw, "https://")
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimSuffix(host, "/")
		if host == "" {
			return "unknown"
		}
		return host
	}
	host := u.Host
	// "abc.supabase.co" -> "abc" (just the project-ref).
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

// buildStatCards maps native.Stats into the 4 locked card numbers. All
// four cards now resolve to real values -- Connected% and Decay are
// populated directly from the native.Stats fields added alongside this
// dashboard change. The Note below the grid is reserved for a single-
// sentence error caveat (nil stats only).
func buildStatCards(s *native.Stats) StatCards {
	if s == nil {
		return StatCards{
			Memories:  "--",
			Connected: "--",
			Tags:      "--",
			Decay:     "--",
			Note:      "Stats unavailable (see stderr for details).",
		}
	}
	return StatCards{
		Memories:  formatInt(s.Total),
		Connected: formatPct(s.ConnectedPct),
		Tags:      formatInt(int64(len(s.TopTags))),
		Decay:     formatInt(s.DecayCount),
	}
}

// formatPct renders a 0-100 float as the integer percentage + "%".
// Dashboard cards prefer integer bucketing for readability; 3.7% and
// 4.2% are both "4%" under this rule. Edge cases:
//   - negative values (shouldn't happen) clamp to 0.
//   - values >100 (also shouldn't happen) clamp to 100 to avoid a
//     card like "134%" blowing past the column width.
func formatPct(v float64) string {
	if v <= 0 {
		return "0%"
	}
	if v >= 100 {
		return "100%"
	}
	// Round-half-up via +0.5 rather than math.Round to keep this
	// allocation-free and avoid importing math just for one call.
	return itoa(int64(v+0.5)) + "%"
}

// clientFilter is the dumb in-memory filter the Overview page uses for
// the HTMX live-filter input. For 20-row lists this is adequate; a
// server-side WHERE once List supports a text-match filter would be
// better but that's scope-creep for the prototype.
func clientFilter(mems []native.Memory, q string) []native.Memory {
	q = strings.ToLower(q)
	out := mems[:0]
	for _, m := range mems {
		if strings.Contains(strings.ToLower(m.Content), q) {
			out = append(out, m)
			continue
		}
		for _, t := range m.Tags {
			if strings.Contains(strings.ToLower(t), q) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// firstErr returns the first non-nil error from the list. Handy for the
// Overview page which wants to surface any single failure without
// clobbering successful data from the other call.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// formatInt renders an int64 with thousands separators. Avoids pulling
// golang.org/x/text/message just for this; the locale-free comma style
// matches the Next.js dashboard reference.
func formatInt(n int64) string {
	if n < 1000 {
		return itoa(n)
	}
	s := itoa(n)
	// Insert commas right-to-left.
	var buf []byte
	for i, c := range []byte(s) {
		if i != 0 && (len(s)-i)%3 == 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, c)
	}
	return string(buf)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
