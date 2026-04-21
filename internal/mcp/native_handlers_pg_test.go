//go:build pgcontainer

// Table-driven handler round-trip sweep. Lifts internal/mcp coverage
// from ~41% (argument-boundary only) into the delegation + native.*
// path by wiring every Build*Handler against a real pgvector container.
//
// Gemini pattern: one test, one table, all 24 handlers. Per-tool entry
// is 3--4 lines. Seeding happens once per-suite via insertFixtures();
// resetting between tests keeps them order-independent.

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/testdb"
)

// newOllamaStub returns an httptest.Server that responds to every
// POST /api/embed with a 512-dim zero vector. Covers the store +
// hybrid_search paths that embed content client-side before hitting
// the DB.
func newOllamaStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vec := make([]float64, 512)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": vec,
		})
	}))
}

// pgTestCfg builds a fresh native.Config pointed at the shared
// pgvector container with the given profile. Every test case calls
// this so Profile mutation (switch_profile) doesn't leak across cases.
func pgTestCfg(t *testing.T, profile string) *native.Config {
	t.Helper()
	dsn := testdb.DSN(t)
	cfg := &native.Config{Profile: profile}
	cfg.Database.Backend = "postgres"
	cfg.Database.URL = dsn
	return cfg
}

// insertSeed writes one row so the search/list/graph/delete handlers
// have something to operate on. Returns the new memory id so tests can
// target it from their args payload.
func insertSeed(t *testing.T, profile, content string, tags []string) string {
	t.Helper()
	return testdb.InsertMemory(t, testdb.DSN(t), profile, content, tags)
}

// handlerCase is one row in the table. payloadFn receives a seeded id
// so delete/update/reinforce/contradict/find_related can target it.
type handlerCase struct {
	name       string
	builder    func(*native.Config) mcp.ToolHandler
	payloadFn  func(seededID string) string // returns JSON arguments
	wantError  bool                          // tool-level error expected?
	wantInBody string                        // substring present in success result content
}

// TestMCPHandlers_PGSweep runs every Build*Handler against a real
// container. Each case resets the DB, seeds one memory, calls the
// handler, and asserts the result shape (ok / error / contains
// marker). Any handler returning an unexpected transport error fails.
func TestMCPHandlers_PGSweep(t *testing.T) {
	// One shared cfg is fine for the happy paths -- the handlers that
	// mutate cfg.Profile (switch_profile) do so in-place, so we rebuild
	// per-case to avoid leaking state.
	dsn := testdb.DSN(t)

	cases := []handlerCase{
		{
			name:      "health_check",
			builder:   BuildNativeHealthHandler,
			payloadFn: func(string) string { return `{}` },
		},
		{
			name:      "list_recent",
			builder:   BuildNativeListHandler,
			payloadFn: func(string) string { return `{"limit":5}` },
			// At least one seeded row; result body contains the content.
			wantInBody: "handler-seed",
		},
		{
			name:      "delete_memory",
			builder:   BuildNativeDeleteHandler,
			payloadFn: func(id string) string { return `{"id":"` + id + `"}` },
			// Delete returns {id, profile} -- echo the profile to prove
			// the handler reached the native path, not the empty-cfg
			// error branch.
			wantInBody: `"profile": "work"`,
		},
		{
			name:       "cleanup_expired",
			builder:    BuildNativeCleanupHandler,
			payloadFn:  func(string) string { return `{}` },
			wantInBody: "deleted",
		},
		{
			name:       "list_profiles",
			builder:    BuildNativeListProfilesHandler,
			payloadFn:  func(string) string { return `{}` },
			wantInBody: "work",
		},
		{
			name:      "set_profile_ttl",
			builder:   BuildNativeSetProfileTTLHandler,
			payloadFn: func(string) string { return `{"profile":"work","ttl_days":7}` },
			wantInBody: `"ttl_days": 7`,
		},
		{
			name:      "reinforce_memory",
			builder:   BuildNativeReinforceHandler,
			payloadFn: func(id string) string { return `{"memory_id":"` + id + `","strength":0.9}` },
			wantInBody: "reinforced",
		},
		{
			name:      "contradict_memory",
			builder:   BuildNativeContradictHandler,
			payloadFn: func(id string) string { return `{"memory_id":"` + id + `","strength":0.1}` },
			wantInBody: "contradicted",
		},
		{
			name:      "update_memory",
			builder:   BuildNativeUpdateHandler,
			payloadFn: func(id string) string {
				return `{"memory_id":"` + id + `","tags":["renamed"]}`
			},
			wantInBody: "updated",
		},
		{
			name:      "current_profile",
			builder:   BuildNativeCurrentProfileHandler,
			payloadFn: func(string) string { return `{}` },
			wantInBody: "work",
		},
		{
			name:      "switch_profile",
			builder:   BuildNativeSwitchProfileHandler,
			payloadFn: func(string) string { return `{"profile":"alt"}` },
			wantInBody: "switched",
		},
		{
			name:       "get_config",
			builder:    BuildNativeGetConfigHandler,
			payloadFn:  func(string) string { return `{}` },
			wantInBody: "postgres", // redacted config still shows the backend
		},
		{
			name:       "get_stats",
			builder:    BuildNativeGetStatsHandler,
			payloadFn:  func(string) string { return `{}` },
			wantInBody: `"profile": "work"`,
		},
		{
			name:       "link_unlinked",
			builder:    BuildNativeLinkUnlinkedHandler,
			payloadFn:  func(string) string { return `{"batch_size":10}` },
			wantInBody: "processed",
		},
		{
			name:      "find_related",
			builder:   BuildNativeFindRelatedHandler,
			payloadFn: func(id string) string { return `{"memory_id":"` + id + `"}` },
			wantInBody: `"count"`, // count=0 is fine; no edges exist
		},
		{
			name:      "suggest_connections",
			builder:   BuildNativeSuggestConnectionsHandler,
			payloadFn: func(id string) string {
				return `{"memory_id":"` + id + `","min_shared_entities":1}`
			},
			wantInBody: `"count"`,
		},
	}

	// Sentinel is per-binary state: switch_profile in one case would
	// leak its "alt" value into current_profile in a later case. Route
	// the sentinel at a per-binary temp HOME so every test starts from
	// "fall through to cfg.Profile".
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OGHAM_PROFILE", "")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testdb.Reset(t, dsn)
			_ = native.ClearActiveProfile()
			seededID := insertSeed(t, "work", "handler-seed content", []string{"alpha"})

			cfg := pgTestCfg(t, "work")
			h := tc.builder(cfg)
			payload := tc.payloadFn(seededID)

			req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
				Arguments: json.RawMessage(payload),
			}}
			res, err := h(context.Background(), req)
			if err != nil {
				t.Fatalf("transport err: %v", err)
			}
			if res == nil {
				t.Fatal("nil result")
			}
			if tc.wantError && !res.IsError {
				t.Errorf("expected IsError=true, got %+v", res)
			}
			if !tc.wantError && res.IsError {
				// Print the error message for debugging.
				var body string
				if len(res.Content) > 0 {
					if tc, ok := res.Content[0].(*mcp.TextContent); ok {
						body = tc.Text
					}
				}
				t.Errorf("unexpected tool error: %s", body)
			}
			if tc.wantInBody != "" {
				var body string
				if len(res.Content) > 0 {
					if tc, ok := res.Content[0].(*mcp.TextContent); ok {
						body = tc.Text
					}
				}
				if !strings.Contains(body, tc.wantInBody) {
					t.Errorf("result body missing %q; got %q", tc.wantInBody, body)
				}
			}
		})
	}
}

// Typed-store handlers require an embedder (content gets embedded
// before the INSERT). These live in their own test so they can set up
// the Ollama httptest stub once rather than threading it through the
// table. Same coverage story -- real handler call into native.Store().
func TestMCPHandlers_TypedStorePGSweep(t *testing.T) {
	dsn := testdb.DSN(t)
	testdb.Reset(t, dsn)

	ollama := newOllamaStub()
	defer ollama.Close()

	cfg := pgTestCfg(t, "work")
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Model = "embeddinggemma"
	cfg.Embedding.Dimension = 512
	t.Setenv("OLLAMA_URL", ollama.URL)

	cases := []struct {
		name       string
		builder    func(*native.Config) mcp.ToolHandler
		payload    string
		wantInBody string
	}{
		{
			name:       "store_memory",
			builder:    BuildNativeStoreHandler,
			payload:    `{"content":"a store memory","tags":["plain"]}`,
			wantInBody: `"profile": "work"`,
		},
		{
			name:       "store_decision",
			builder:    BuildNativeStoreDecisionHandler,
			payload:    `{"decision":"use postgres","rationale":"mature tooling"}`,
			wantInBody: "work",
		},
		{
			name:       "store_fact",
			builder:    BuildNativeStoreFactHandler,
			payload:    `{"fact":"water boils at 100C","confidence":1.0}`,
			wantInBody: "work",
		},
		{
			name:       "store_event",
			builder:    BuildNativeStoreEventHandler,
			payload:    `{"event":"release cut","when":"yesterday"}`,
			wantInBody: "work",
		},
		{
			name:       "store_preference",
			builder:    BuildNativeStorePreferenceHandler,
			payload:    `{"preference":"dark mode","strength":"strong"}`,
			wantInBody: "work",
		},
		{
			name:       "hybrid_search",
			builder:    BuildNativeSearchHandler,
			payload:    `{"query":"postgres","limit":5}`,
			wantInBody: `"count"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.builder(cfg)
			req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
				Arguments: json.RawMessage(tc.payload),
			}}
			res, err := h(context.Background(), req)
			if err != nil {
				t.Fatalf("transport err: %v", err)
			}
			if res.IsError {
				var body string
				if len(res.Content) > 0 {
					if tc, ok := res.Content[0].(*mcp.TextContent); ok {
						body = tc.Text
					}
				}
				t.Fatalf("%s: unexpected tool error: %s", tc.name, body)
			}
			if tc.wantInBody != "" {
				var body string
				if tc2, ok := res.Content[0].(*mcp.TextContent); ok {
					body = tc2.Text
				}
				if !strings.Contains(body, tc.wantInBody) {
					t.Errorf("%s: body missing %q; got %q", tc.name, tc.wantInBody, body)
				}
			}
		})
	}
}
