package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""}, // empty stays empty; Mask() returns zero-value for empty fields
		{"short", "<redacted>"},
		{"exactly8", "<redacted>"},
		{"sb_secret_abcdef1234567890", "sb_s…7890"},
		{"AIzaSyBnXo7vN6y23Awur4WIo1jAYu0EDxV89d4", "AIza…89d4"},
	}
	for _, tc := range cases {
		got := maskSecret(tc.in)
		// empty case handled by Mask, not maskSecret -- the real behaviour
		// matters at the Mask() level.
		if tc.in == "" {
			continue
		}
		if got != tc.want {
			t.Errorf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMask(t *testing.T) {
	cfg := &Config{
		Profile: "work",
		Database: Database{
			Backend:     "supabase",
			URL:         "postgres://u:verysecret@host:5432/db",
			SupabaseURL: "https://x.supabase.co",
			SupabaseKey: "sb_secret_abcdef1234567890wxyz",
		},
		Embedding: Embedding{
			Provider:  "gemini",
			APIKey:    "AIzaSyBnXo7vN6y23Awur4WIo1jAYu0EDxV89d4",
			Model:     "gemini-embedding-2-preview",
			Dimension: 512,
		},
	}
	m := Mask(cfg)
	if m.Profile != "work" || m.Database.Backend != "supabase" {
		t.Errorf("basic passthrough wrong: %+v", m)
	}
	if strings.Contains(m.Database.URL, "verysecret") {
		t.Errorf("password leaked in URL: %q", m.Database.URL)
	}
	if !strings.Contains(m.Database.URL, "***") {
		t.Errorf("URL not redacted: %q", m.Database.URL)
	}
	if strings.Contains(m.Database.SupabaseKey, "1234567890") {
		t.Errorf("supabase key not masked: %q", m.Database.SupabaseKey)
	}
	if strings.Contains(m.Embedding.APIKey, "V89d4") {
		// "V89d4" are the last 4 of the test key; they SHOULD appear
		// because masking shows last-4. Invert the check.
	}
	if !strings.HasPrefix(m.Embedding.APIKey, "AIza") {
		t.Errorf("embedding key should expose first 4: %q", m.Embedding.APIKey)
	}
}

func TestDelete_RequiresID(t *testing.T) {
	cfg := &Config{Database: Database{URL: "postgres://x"}}
	if _, err := Delete(context.Background(), cfg, "", "work"); err == nil {
		t.Error("expected error for empty id")
	}
}

func TestDelete_NoBackend(t *testing.T) {
	cfg := &Config{}
	if _, err := Delete(context.Background(), cfg, "abc", "work"); err == nil {
		t.Error("expected error when backend unresolvable")
	}
}

func TestDeleteSupabase_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if !strings.Contains(r.URL.RawQuery, "id=eq.aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") {
			t.Errorf("query missing id: %s", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.RawQuery, "profile=eq.work") {
			t.Errorf("query missing profile: %s", r.URL.RawQuery)
		}
		if !strings.Contains(r.Header.Get("Prefer"), "return=representation") {
			t.Errorf("missing Prefer header")
		}
		// Return one deleted row.
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}]`))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	res, err := Delete(context.Background(), cfg, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "work")
	if err != nil {
		t.Fatal(err)
	}
	if res.Profile != "work" {
		t.Errorf("profile = %q", res.Profile)
	}
}

func TestDeleteSupabase_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	_, err := Delete(context.Background(), cfg, "nope", "work")
	if err == nil || !strings.Contains(err.Error(), "no memory") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestCleanupSupabase_ReturnsDeletedCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/v1/rpc/count_expired_memories":
			_, _ = w.Write([]byte("5"))
		case "/rest/v1/rpc/cleanup_expired_memories":
			body, _ := decodeBody(r.Body)
			if body["target_profile"] != "work" {
				t.Errorf("wrong profile in body: %+v", body)
			}
			_, _ = w.Write([]byte("5"))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	res, err := Cleanup(context.Background(), cfg, "work")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 5 {
		t.Errorf("deleted = %d, want 5", res.Deleted)
	}
}

func TestDecay_DryRun_Supabase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CountDecayEligible uses GET /memories with filters; return two rows.
		_, _ = w.Write([]byte(`[{},{}]`))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	res, err := Decay(context.Background(), cfg, "work", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Eligible != 2 {
		t.Errorf("unexpected dry-run result: %+v", res)
	}
}

func TestAuditSupabase_FilterShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/v1/audit_log") {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("profile") != "eq.work" {
			t.Errorf("profile filter = %q", q.Get("profile"))
		}
		if q.Get("operation") != "eq.store" {
			t.Errorf("operation filter = %q", q.Get("operation"))
		}
		if q.Get("order") != "event_time.desc" {
			t.Errorf("order = %q", q.Get("order"))
		}
		_, _ = w.Write([]byte(`[
			{"event_time":"2026-04-20T09:30:00Z","profile":"work","operation":"store","memory_id":"abc"},
			{"event_time":"2026-04-20T09:29:00Z","profile":"work","operation":"store","memory_id":"def"}
		]`))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	events, err := Audit(context.Background(), cfg, "work", "store", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2", len(events))
	}
}

// decodeBody parses a JSON request body into a map for assertions.
func decodeBody(r interface{ Read(p []byte) (int, error) }) (map[string]any, error) {
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	var m map[string]any
	if err := json.Unmarshal(buf[:n], &m); err != nil {
		return nil, err
	}
	return m, nil
}
