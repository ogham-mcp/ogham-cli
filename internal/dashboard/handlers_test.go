package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// newTestContext returns a 2-second deadline context scoped to the test.
// Keeps shutdown tests responsive; any shutdown that takes longer is a
// regression in the http.Server config or a leaked handler.
func newTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Handler tests here are smoke-level -- they don't touch a real Postgres
// or Supabase, so they rely on native.GetStats / native.List / native.Search
// failing cleanly when no backend is configured. The Overview page is
// designed to render error-banner-plus-chrome in that case, which is the
// behaviour we assert.

func newTestHandlers() *handlers {
	// Config with no backend: ResolveBackend will error, stats/list will
	// surface the error, overview.templ renders the banner + 4 empty cards.
	return &handlers{cfg: &native.Config{Profile: "test"}}
}

func TestOverview_ErrorBannerRenders(t *testing.T) {
	h := newTestHandlers()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	h.overview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	// Chrome elements always present.
	if !strings.Contains(body, "Ogham") || !strings.Contains(body, "Overview") {
		t.Errorf("missing layout chrome in response")
	}
	if !strings.Contains(body, `data-slot="card"`) {
		t.Errorf("missing card element -- template structure regressed")
	}
	// Error banner because the config has no backend.
	if !strings.Contains(body, "Data error") {
		t.Errorf("expected Data error banner; body head: %q", body[:min(300, len(body))])
	}
}

func TestOverview_RejectsNonRootPath(t *testing.T) {
	h := newTestHandlers()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)

	h.overview(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d want %d", rr.Code, http.StatusNotFound)
	}
}

func TestSearch_EmptyQueryRendersForm(t *testing.T) {
	h := newTestHandlers()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/search", nil)

	h.search(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="q"`) {
		t.Errorf("search form input missing")
	}
	// No error banner when the query is empty -- we haven't called Search.
	if strings.Contains(body, "Search error") {
		t.Errorf("unexpected error banner on empty-query page")
	}
}

func TestSearch_NonEmptyQueryShowsBanner(t *testing.T) {
	// Non-empty query triggers native.Search, which fails (no backend);
	// the handler must surface the error banner and still render chrome.
	h := newTestHandlers()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/search?q=docker", nil)

	h.search(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Search error") {
		t.Errorf("expected Search error banner when backend unconfigured")
	}
	if !strings.Contains(body, `value="docker"`) {
		t.Errorf("query value not echoed back into the input")
	}
}

func TestHealthz(t *testing.T) {
	h := newTestHandlers()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	h.healthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	if strings.TrimSpace(rr.Body.String()) != "ok" {
		t.Errorf("healthz body: got %q want %q", rr.Body.String(), "ok")
	}
}

func TestBuildStatCards_Nil(t *testing.T) {
	got := buildStatCards(nil)
	if got.Memories != "--" || got.Connected != "--" || got.Tags != "--" || got.Decay != "--" {
		t.Errorf("nil stats should zero all cards to --; got %+v", got)
	}
	if got.Note == "" {
		t.Errorf("nil stats should carry an explanatory note")
	}
}

func TestBuildStatCards_Populated(t *testing.T) {
	s := &native.Stats{
		Total: 1234,
		TopTags: []native.Breakdown{
			{Name: "type:decision", Count: 10},
			{Name: "project:ogham", Count: 5},
		},
	}
	got := buildStatCards(s)
	if got.Memories != "1,234" {
		t.Errorf("Memories: got %q want %q", got.Memories, "1,234")
	}
	if got.Tags != "2" {
		t.Errorf("Tags: got %q want %q", got.Tags, "2")
	}
}

func TestClientFilter(t *testing.T) {
	mems := []native.Memory{
		{ID: "1", Content: "Docker setup notes", Tags: []string{"type:gotcha"}},
		{ID: "2", Content: "Search threshold discussion", Tags: []string{"project:ogham"}},
		{ID: "3", Content: "Random other memory", Tags: []string{"docker-related"}},
	}
	got := clientFilter(mems, "docker")
	if len(got) != 2 {
		t.Fatalf("filter: got %d matches want 2", len(got))
	}
}

func TestFormatInt(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		42:         "42",
		999:        "999",
		1000:       "1,000",
		1234:       "1,234",
		1234567:    "1,234,567",
		1000000000: "1,000,000,000",
	}
	for in, want := range cases {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d): got %q want %q", in, got, want)
		}
	}
}

// TestServerBindsAndServes verifies New() + Serve() + Shutdown() together.
// Uses port 0 so the OS picks a free port; guards against the regression
// where an embedded filesystem path typo would surface only at runtime.
func TestServerBindsAndServes(t *testing.T) {
	srv, addr, err := New(&native.Config{Profile: "test"}, "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	// Don't hammer the listener before the goroutine wires up; give it
	// a frame to schedule. 100ms is plenty on a local machine.
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status: %d", resp.StatusCode)
	}

	// Shutdown must not error on clean paths.
	if err := srv.Shutdown(newTestContext(t)); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil && err != http.ErrServerClosed {
		t.Errorf("Serve returned unexpected error: %v", err)
	}
}

// TestStaticAssetsEmbedded asserts the embedded styles.css and htmx.min.js
// are served at /static/ with a reasonable body. Catches a regression
// where the go:embed pattern picks up the wrong directory.
func TestStaticAssetsEmbedded(t *testing.T) {
	srv, addr, err := New(&native.Config{Profile: "test"}, "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Shutdown(newTestContext(t)) })
	time.Sleep(100 * time.Millisecond)

	for path, minBytes := range map[string]int{
		"/static/styles.css":  100, // compiled Tailwind, >100 bytes trivially
		"/static/htmx.min.js": 100,
	} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", path, resp.StatusCode)
		}
		buf := make([]byte, minBytes+1)
		n, _ := resp.Body.Read(buf)
		_ = resp.Body.Close()
		if n <= minBytes {
			t.Errorf("%s: body too short (%d bytes)", path, n)
		}
	}
}
