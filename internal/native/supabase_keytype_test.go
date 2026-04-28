package native

import (
	"encoding/base64"
	"strings"
	"testing"
)

// makeJWT builds a token of the form "header.payload.sig" where the
// payload contains the given claims JSON. The signature segment is a
// throwaway string -- ClassifySupabaseKey doesn't verify signatures.
func makeJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".signature_not_checked"
}

func TestClassifySupabaseKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want SupabaseKeyKind
	}{
		{"empty", "", SupabaseKeyUnknown},
		{"sb_secret prefix", "sb_secret_abcdef0123", SupabaseKeySecret},
		{"sb_publishable prefix", "sb_publishable_abcdef0123", SupabaseKeyPublishable},
		{"legacy anon JWT", makeJWT(`{"role":"anon","iss":"supabase"}`), SupabaseKeyAnon},
		{"legacy service_role JWT", makeJWT(`{"role":"service_role","iss":"supabase"}`), SupabaseKeyServiceRole},
		{"JWT without role", makeJWT(`{"iss":"supabase"}`), SupabaseKeyUnknown},
		{"unrecognised role", makeJWT(`{"role":"authenticated"}`), SupabaseKeyUnknown},
		{"malformed JWT segments", "not.a.valid", SupabaseKeyUnknown},
		{"single-segment garbage", "sk_live_xyz", SupabaseKeyUnknown},
		{"whitespace trimmed", "  sb_secret_abc  ", SupabaseKeySecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySupabaseKey(tc.key); got != tc.want {
				t.Fatalf("ClassifySupabaseKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestSupabaseKeyKind_IsRPCCapable(t *testing.T) {
	// Secret + service_role authorise the RPC and memories paths Ogham
	// uses; anon and publishable are gated by RLS and will 401. Unknown
	// is treated as "give it a try" rather than erroring early — saves
	// users on edge-case key formats from being blocked by our heuristic.
	capable := []SupabaseKeyKind{SupabaseKeySecret, SupabaseKeyServiceRole, SupabaseKeyUnknown}
	denied := []SupabaseKeyKind{SupabaseKeyAnon, SupabaseKeyPublishable}
	for _, k := range capable {
		if !k.IsRPCCapable() {
			t.Fatalf("expected %q to be RPC-capable", k)
		}
	}
	for _, k := range denied {
		if k.IsRPCCapable() {
			t.Fatalf("expected %q to NOT be RPC-capable", k)
		}
	}
}

func TestMaskFlagsAnonKey(t *testing.T) {
	cfg := &Config{
		Database: Database{
			Backend:     "supabase",
			SupabaseURL: "https://demo.supabase.co",
			SupabaseKey: makeJWT(`{"role":"anon"}`),
		},
		Embedding: Embedding{Provider: "ollama", Dimension: 512},
		Profile:   "default",
	}
	masked := Mask(cfg)
	if masked.Database.SupabaseKeyKind != string(SupabaseKeyAnon) {
		t.Fatalf("expected supabase_key_kind=anon, got %q", masked.Database.SupabaseKeyKind)
	}
	if len(masked.Warnings) == 0 {
		t.Fatal("expected at least one warning for anon key, got none")
	}
	if !strings.Contains(masked.Warnings[0], "sb_secret_") {
		t.Fatalf("warning should point at sb_secret_; got %q", masked.Warnings[0])
	}
}

func TestHTTPStatusErr_401AnonGetsHint(t *testing.T) {
	c := &supabaseClient{keyKind: SupabaseKeyAnon}
	err := c.httpStatusErr("rpc hybrid_search_memories", 401, []byte(`{"message":"Invalid API key"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sb_secret_") {
		t.Fatalf("401 with anon key should mention sb_secret_; got: %v", err)
	}
	if !strings.Contains(err.Error(), "anon") {
		t.Fatalf("401 hint should name the key kind; got: %v", err)
	}
}

func TestHTTPStatusErr_401SecretNoHint(t *testing.T) {
	// A 401 with a secret-prefixed key is more likely a key-rotated /
	// project-mismatched config than a wrong-role config. Don't shout
	// about anon — the user already has the right key kind.
	c := &supabaseClient{keyKind: SupabaseKeySecret}
	err := c.httpStatusErr("rpc hybrid_search_memories", 401, []byte(`{"message":"Invalid API key"}`))
	if strings.Contains(err.Error(), "sb_secret_") {
		t.Fatalf("secret-key 401 should not nag about sb_secret_; got: %v", err)
	}
}

func TestHTTPStatusErr_500NoHintEvenForAnon(t *testing.T) {
	// The hint specifically targets 401. Non-auth failures must surface
	// the raw PostgREST body so operators can debug them without our
	// auth-flavoured detour.
	c := &supabaseClient{keyKind: SupabaseKeyAnon}
	err := c.httpStatusErr("rpc hybrid_search_memories", 500, []byte(`{"message":"internal error"}`))
	if strings.Contains(err.Error(), "sb_secret_") {
		t.Fatalf("non-401 errors should not get the auth hint; got: %v", err)
	}
}

func TestMaskNoWarningForSecretKey(t *testing.T) {
	cfg := &Config{
		Database: Database{
			Backend:     "supabase",
			SupabaseURL: "https://demo.supabase.co",
			SupabaseKey: "sb_secret_abc123def456",
		},
		Embedding: Embedding{Provider: "ollama", Dimension: 512},
	}
	masked := Mask(cfg)
	if masked.Database.SupabaseKeyKind != string(SupabaseKeySecret) {
		t.Fatalf("expected supabase_key_kind=secret, got %q", masked.Database.SupabaseKeyKind)
	}
	if len(masked.Warnings) != 0 {
		t.Fatalf("expected no warnings for secret key, got %v", masked.Warnings)
	}
}
