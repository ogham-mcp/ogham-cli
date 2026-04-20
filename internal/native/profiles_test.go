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

func TestListProfiles_Supabase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/rpc/get_profile_counts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := []ProfileCount{
			{Profile: "work", Count: 1003},
			{Profile: "default", Count: 47},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	got, err := ListProfiles(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Profile != "work" || got[0].Count != 1003 {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestGetProfileTTL_Supabase_NoRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/v1/profile_settings") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	ttl, err := GetProfileTTL(context.Background(), cfg, "work")
	if err != nil {
		t.Fatal(err)
	}
	if ttl.Profile != "work" || ttl.TTLDays != nil {
		t.Errorf("expected no-TTL result, got %+v (TTLDays=%v)", ttl, ttl.TTLDays)
	}
}

func TestSetProfileTTL_Supabase_Upsert(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/profile_settings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if prefer := r.Header.Get("Prefer"); !strings.Contains(prefer, "merge-duplicates") {
			t.Errorf("expected Prefer: merge-duplicates header, got %q", prefer)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["profile"] != "work" {
			t.Errorf("profile = %v", parsed["profile"])
		}
		days, _ := parsed["ttl_days"].(float64)
		if days != 90 {
			t.Errorf("ttl_days = %v, want 90", parsed["ttl_days"])
		}

		back := 90
		resp := []ProfileTTL{{Profile: "work", TTLDays: &back}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	ttl, err := SetProfileTTL(context.Background(), cfg, "work", 90)
	if err != nil {
		t.Fatal(err)
	}
	if ttl.TTLDays == nil || *ttl.TTLDays != 90 {
		t.Errorf("unexpected ttl: %+v", ttl)
	}
}

func TestSetProfileTTL_Supabase_Clear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["ttl_days"] != nil {
			t.Errorf("ttl_days should be null for clear, got %v", parsed["ttl_days"])
		}
		resp := []ProfileTTL{{Profile: "work", TTLDays: nil}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &Config{Database: Database{SupabaseURL: server.URL, SupabaseKey: "sb_test"}}
	ttl, err := SetProfileTTL(context.Background(), cfg, "work", -1)
	if err != nil {
		t.Fatal(err)
	}
	if ttl.TTLDays != nil {
		t.Errorf("TTL should be cleared, got %v", *ttl.TTLDays)
	}
}

func TestSetProfileTTL_RequiresProfile(t *testing.T) {
	cfg := &Config{Database: Database{URL: "postgres://x"}}
	if _, err := SetProfileTTL(context.Background(), cfg, "", 30); err == nil {
		t.Error("expected error when profile is empty")
	}
}
