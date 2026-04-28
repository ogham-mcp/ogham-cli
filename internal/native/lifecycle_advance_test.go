package native

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAdvanceLifecycle_NilCfg(t *testing.T) {
	if _, err := AdvanceLifecycle(context.Background(), nil, "p", AdvanceLifecycleOptions{}); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestAdvanceLifecycle_UnknownBackend(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "rocks"}}
	if _, err := AdvanceLifecycle(context.Background(), cfg, "p", AdvanceLifecycleOptions{}); err == nil {
		t.Error("expected error on unknown backend")
	}
}

// Pre-026 cluster: memory_lifecycle table missing on the Supabase side
// surfaces as 404 from the HEAD probe -> StageReport with
// LifecycleAbsent=true and zero counts.
func TestAdvanceLifecycle_SupabaseLifecycleAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Probe HEAD against /memory_lifecycle -> 404 (table missing).
		if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/memory_lifecycle") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}

	rep, err := AdvanceLifecycle(context.Background(), cfg, "default", AdvanceLifecycleOptions{})
	if err != nil {
		t.Fatalf("AdvanceLifecycle: %v", err)
	}
	if !rep.LifecycleAbsent {
		t.Error("LifecycleAbsent should be true on pre-026 cluster")
	}
	if rep.FreshToStable != 0 || rep.EditingClosed != 0 {
		t.Errorf("counts should be zero when table absent; got %+v", rep)
	}
}

// Empty-set: HEAD finds the table, GET candidates returns no rows,
// PATCH editing-close also returns nothing -> {0, 0}.
func TestAdvanceLifecycle_SupabaseEmptyProfile(t *testing.T) {
	var headHits, getHits, patchHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt64(&headHits, 1)
			w.Header().Set("Content-Range", "0-0/0")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			atomic.AddInt64(&getHits, 1)
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPatch:
			atomic.AddInt64(&patchHits, 1)
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}
	rep, err := AdvanceLifecycle(context.Background(), cfg, "default", AdvanceLifecycleOptions{})
	if err != nil {
		t.Fatalf("AdvanceLifecycle: %v", err)
	}
	if rep.FreshToStable != 0 || rep.EditingClosed != 0 {
		t.Errorf("expected zero counts on empty profile; got %+v", rep)
	}
	if headHits == 0 {
		t.Error("expected at least one HEAD probe")
	}
}

// Happy path: 2 fresh candidates clear the gates, both flip; 1 editing
// row past the cutoff closes. Confirms PATCH bodies / count math.
func TestAdvanceLifecycle_SupabaseHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodHead && strings.HasSuffix(path, "/memory_lifecycle"):
			w.Header().Set("Content-Range", "0-0/3")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/memory_lifecycle"):
			// Fresh candidates from lifecycle side.
			_, _ = w.Write([]byte(`[
				{"memory_id":"m1"},
				{"memory_id":"m2"},
				{"memory_id":"m3"}
			]`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/memories"):
			// Two of the three clear the gate.
			_, _ = w.Write([]byte(`[{"id":"m1"},{"id":"m3"}]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/memory_lifecycle"):
			// Need to distinguish fresh->stable from editing->stable by
			// the query string. Both queries set stage=eq.<from>; check
			// it on r.URL.RawQuery.
			q := r.URL.Query()
			if q.Get("stage") == "eq.fresh" {
				_, _ = w.Write([]byte(`[
					{"memory_id":"m1","stage":"stable"},
					{"memory_id":"m3","stage":"stable"}
				]`))
				return
			}
			if q.Get("stage") == "eq.editing" {
				_, _ = w.Write([]byte(`[{"memory_id":"e1","stage":"stable"}]`))
				return
			}
			t.Errorf("unexpected PATCH stage filter %q", q.Get("stage"))
			w.WriteHeader(500)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()

	cfg := &Config{
		Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
		Embedding: Embedding{Provider: "gemini", APIKey: "k"},
		Profile:   "default",
	}
	rep, err := AdvanceLifecycle(context.Background(), cfg, "default", AdvanceLifecycleOptions{})
	if err != nil {
		t.Fatalf("AdvanceLifecycle: %v", err)
	}
	if rep.FreshToStable != 2 {
		t.Errorf("FreshToStable = %d, want 2", rep.FreshToStable)
	}
	if rep.EditingClosed != 1 {
		t.Errorf("EditingClosed = %d, want 1", rep.EditingClosed)
	}
}

// Defaults applied: missing options pull Python-matching values.
func TestAdvanceLifecycle_DefaultsApplied(t *testing.T) {
	o := AdvanceLifecycleOptions{}
	if o.DwellHours != 0 || o.SurpriseGate != 0 || o.ImportanceGate != 0 || o.EditingWindowMinutes != 0 {
		t.Fatalf("zero value drifted: %+v", o)
	}
	// We can't run advanceLifeCyclePostgres without a DB; instead
	// confirm AdvanceDefault* constants match Python (sanity check that
	// matters for parity with src/ogham/lifecycle.py).
	if AdvanceDefaultDwellHours != 1.0 {
		t.Errorf("DwellHours default = %f, want 1.0 (mirror Python)", AdvanceDefaultDwellHours)
	}
	if AdvanceDefaultSurpriseGate != 0.3 {
		t.Errorf("SurpriseGate default = %f, want 0.3", AdvanceDefaultSurpriseGate)
	}
	if AdvanceDefaultImportanceGate != 0.5 {
		t.Errorf("ImportanceGate default = %f, want 0.5", AdvanceDefaultImportanceGate)
	}
	if AdvanceDefaultEditingWindowMinutes != 30 {
		t.Errorf("EditingWindowMinutes default = %d, want 30", AdvanceDefaultEditingWindowMinutes)
	}
}

func TestDecodeJSON_EmptyBody(t *testing.T) {
	var v []map[string]any
	if err := decodeJSON(nil, &v); err != nil {
		t.Errorf("decodeJSON(nil) returned error: %v", err)
	}
	if err := decodeJSON([]byte{}, &v); err != nil {
		t.Errorf("decodeJSON(empty) returned error: %v", err)
	}
}
