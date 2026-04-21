package native

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Supabase happy-path coverage via httptest. Each test stands up a
// minimal PostgREST-lookalike, points a Config at it via SupabaseURL,
// and asserts the shape of the outgoing request + decodes a canned
// response. No real Supabase, no real pgx. Covers the *Supabase
// functions that would otherwise sit at 0% coverage outside live tests.
//
// Pattern lifted from TestWriteMemorySupabase_RoundTrip in store_test.go.

func newSupabaseTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Config) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := &Config{
		Database: Database{
			SupabaseURL: server.URL,
			SupabaseKey: "sb_secret_test",
		},
	}
	return server, cfg
}

// --- deleteSupabase -------------------------------------------------------

func TestDeleteSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		// PostgREST filter shape: id=eq.<uuid>&profile=eq.<name>
		if got := r.URL.Query().Get("id"); got != "eq.abc-123" {
			t.Errorf("id filter = %q", got)
		}
		if got := r.URL.Query().Get("profile"); got != "eq.work" {
			t.Errorf("profile filter = %q", got)
		}
		if r.Header.Get("Prefer") != "return=representation" {
			t.Errorf("Prefer header missing; got %q", r.Header.Get("Prefer"))
		}
		// Respond with the deleted row so the function returns cleanly.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "abc-123", "profile": "work"},
		})
	})

	result, err := Delete(context.Background(), cfg, "abc-123", "work")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if result.ID != "abc-123" || result.Profile != "work" {
		t.Errorf("result = %+v", result)
	}
}

func TestDeleteSupabase_NoRowIsUserError(t *testing.T) {
	// Empty response body ([]) = no row matched. Delete must surface
	// a clean "no memory with id X" error rather than silently succeeding.
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	_, err := Delete(context.Background(), cfg, "nope", "work")
	if err == nil || !strings.Contains(err.Error(), "no memory") {
		t.Errorf("want 'no memory' error; got %v", err)
	}
}

// --- updateConfidenceSupabase --------------------------------------------

func TestUpdateConfidenceSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// RPC endpoint convention.
		if !strings.HasSuffix(r.URL.Path, "/rpc/update_confidence") {
			t.Errorf("path = %q, want .../rpc/update_confidence", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var args map[string]any
		if err := json.Unmarshal(body, &args); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		// Supabase RPC uses "memory_profile" to dodge PostgREST's
		// reserved `profile` query param. Pinning this guards parity
		// with the Python backend.
		if args["memory_profile"] != "work" {
			t.Errorf("memory_profile = %v, want 'work'", args["memory_profile"])
		}
		if args["memory_id"] != "abc-123" {
			t.Errorf("memory_id = %v", args["memory_id"])
		}
		if args["signal"] != 0.85 {
			t.Errorf("signal = %v, want 0.85", args["signal"])
		}
		// RPC returns a scalar float.
		_, _ = w.Write([]byte("0.9"))
	})

	result, err := UpdateConfidence(context.Background(), cfg, "abc-123", 0.85, "work")
	if err != nil {
		t.Fatalf("UpdateConfidence: %v", err)
	}
	if result.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", result.Confidence)
	}
}

// --- updateSupabase (update_memory PATCH) --------------------------------

func TestUpdateSupabase_TagsMetadataPatchRoundTrip(t *testing.T) {
	// Content-change would require NewEmbedder, which we can't wire
	// up in a unit test without spinning a second httptest server for
	// the embedder (not worth the rig for a coverage test). This
	// exercises the tags + metadata branch, which is the common case
	// for reinforcement UIs and tag-rename flows.
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		// Tags + metadata must appear; content + embedding must NOT
		// (no re-embed on this code path).
		if _, ok := row["tags"]; !ok {
			t.Error("tags missing from PATCH body")
		}
		if _, ok := row["metadata"]; !ok {
			t.Error("metadata missing from PATCH body")
		}
		if _, ok := row["content"]; ok {
			t.Error("content must not appear when Content is nil in UpdateOptions")
		}
		if _, ok := row["embedding"]; ok {
			t.Error("embedding must not appear when Content is nil (no re-embed)")
		}
		// updated_at is always bumped for parity across installs.
		if _, ok := row["updated_at"]; !ok {
			t.Error("updated_at missing from PATCH body")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "abc-123", "updated_at": "2026-04-21T12:00:00Z"},
		})
	})

	_, err := Update(context.Background(), cfg, "abc-123", UpdateOptions{
		Tags:     []string{"type:decision"},
		Metadata: map[string]any{"note": "review"},
		Profile:  "work",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// --- listProfilesSupabase ------------------------------------------------

func TestListProfilesSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/get_profile_counts") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"profile": "work", "count": 42},
			{"profile": "personal", "count": 7},
		})
	})

	profiles, err := ListProfiles(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(profiles))
	}
	if profiles[0].Profile != "work" || profiles[0].Count != 42 {
		t.Errorf("profiles[0] = %+v", profiles[0])
	}
}

// --- getProfileTTLSupabase -----------------------------------------------

func TestGetProfileTTLSupabase_RoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.URL.Query().Get("profile"); got != "eq.work" {
			t.Errorf("profile filter = %q", got)
		}
		ttl := 30
		_ = json.NewEncoder(w).Encode([]ProfileTTL{{Profile: "work", TTLDays: &ttl}})
	})

	ttl, err := GetProfileTTL(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("GetProfileTTL: %v", err)
	}
	if ttl.TTLDays == nil || *ttl.TTLDays != 30 {
		t.Errorf("TTLDays = %+v, want pointer to 30", ttl.TTLDays)
	}
}

func TestGetProfileTTLSupabase_NoRowReturnsEmpty(t *testing.T) {
	// Empty PostgREST response = no row for that profile. Must surface
	// as a nil TTLDays, not an error -- callers interpret nil as "no
	// TTL configured, memories never expire".
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	ttl, err := GetProfileTTL(context.Background(), cfg, "work")
	if err != nil {
		t.Fatalf("GetProfileTTL: %v", err)
	}
	if ttl.TTLDays != nil {
		t.Errorf("want nil TTLDays for absent row; got %v", *ttl.TTLDays)
	}
}

// --- setProfileTTLSupabase -----------------------------------------------

func TestSetProfileTTLSupabase_UpsertRoundTrip(t *testing.T) {
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		// PostgREST upsert marker.
		if prefer := r.Header.Get("Prefer"); !strings.Contains(prefer, "resolution=merge-duplicates") {
			t.Errorf("Prefer header = %q, want merge-duplicates", prefer)
		}
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if row["profile"] != "work" {
			t.Errorf("profile = %v", row["profile"])
		}
		if row["ttl_days"] != float64(14) { // JSON numbers decode as float64
			t.Errorf("ttl_days = %v", row["ttl_days"])
		}
		ttl := 14
		_ = json.NewEncoder(w).Encode([]ProfileTTL{{Profile: "work", TTLDays: &ttl}})
	})

	result, err := SetProfileTTL(context.Background(), cfg, "work", 14)
	if err != nil {
		t.Fatalf("SetProfileTTL: %v", err)
	}
	if result.TTLDays == nil || *result.TTLDays != 14 {
		t.Errorf("TTLDays = %+v", result.TTLDays)
	}
}

func TestSetProfileTTLSupabase_ClearRoundTrip(t *testing.T) {
	// ttlDays = -1 semantic means "clear the TTL". The body must send
	// ttl_days: null (not -1), otherwise Postgres stores -1 and the
	// schema's TTL logic misinterprets it.
	_, cfg := newSupabaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if row["ttl_days"] != nil {
			t.Errorf("ttl_days = %v, want null for clear", row["ttl_days"])
		}
		_ = json.NewEncoder(w).Encode([]ProfileTTL{{Profile: "work"}})
	})

	result, err := SetProfileTTL(context.Background(), cfg, "work", -1)
	if err != nil {
		t.Fatalf("SetProfileTTL: %v", err)
	}
	if result.TTLDays != nil {
		t.Errorf("cleared TTL must be nil; got %v", *result.TTLDays)
	}
}
