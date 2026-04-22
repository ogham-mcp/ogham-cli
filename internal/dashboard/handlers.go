package dashboard

import (
	"context"
	"log/slog"
	"net/http"
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

// viewData packages the per-request layout context. Profile is the only
// moving piece today; future iterations will add nav-state highlighting.
func (h *handlers) viewData() ViewData {
	profile := h.cfg.Profile
	if profile == "" {
		profile = "default"
	}
	return ViewData{Profile: profile}
}

// buildStatCards maps native.Stats into the 4 locked card numbers. The
// prototype exposes only Memories + Tags from the stats payload today --
// Connected% and Decay count require predicates native.Stats doesn't
// currently compute. They ship as "--" with an "API gap" footnote so
// we don't invent numbers; a v0.2 task will backfill.
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
		Connected: "--",
		Tags:      formatInt(int64(len(s.TopTags))),
		Decay:     "--",
		Note:      "Connected% and Decay are not yet exposed by native.Stats; tracked for v0.2.",
	}
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
