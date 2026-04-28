package native

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// SupabaseKeyKind classifies a Supabase API key for warning purposes.
// Only "anon" and "publishable" are meaningful failure cases — both lack
// the privileges hybrid_search_memories and the memories table need.
// service_role + secret are the keys Ogham actually wants. unknown
// covers parse failures and is treated as "no warning, give it a try".
type SupabaseKeyKind string

const (
	SupabaseKeyAnon        SupabaseKeyKind = "anon"
	SupabaseKeyPublishable SupabaseKeyKind = "publishable"
	SupabaseKeyServiceRole SupabaseKeyKind = "service_role"
	SupabaseKeySecret      SupabaseKeyKind = "secret"
	SupabaseKeyUnknown     SupabaseKeyKind = "unknown"
)

// ClassifySupabaseKey inspects a Supabase API key and reports its kind.
//
// The new prefixed keys (sb_secret_*, sb_publishable_*) are opaque and
// can be classified by prefix alone. The legacy keys are JWTs whose
// payload carries a "role" claim — we base64-decode the middle segment
// and read it. No signature check is performed; the key is going to be
// sent to Supabase anyway, which is the real authority.
func ClassifySupabaseKey(key string) SupabaseKeyKind {
	key = strings.TrimSpace(key)
	if key == "" {
		return SupabaseKeyUnknown
	}
	switch {
	case strings.HasPrefix(key, "sb_secret_"):
		return SupabaseKeySecret
	case strings.HasPrefix(key, "sb_publishable_"):
		return SupabaseKeyPublishable
	}
	role, ok := jwtRoleClaim(key)
	if !ok {
		return SupabaseKeyUnknown
	}
	switch role {
	case "anon":
		return SupabaseKeyAnon
	case "service_role":
		return SupabaseKeyServiceRole
	}
	return SupabaseKeyUnknown
}

// IsRPCCapable reports whether a key is expected to authorise the RPC
// and SELECT-on-memories paths Ogham relies on. Used both by the loud
// 401 hint and by `ogham config show` to flag misconfigured keys before
// the first request even leaves the box.
func (k SupabaseKeyKind) IsRPCCapable() bool {
	return k == SupabaseKeyServiceRole || k == SupabaseKeySecret || k == SupabaseKeyUnknown
}

// jwtRoleClaim extracts the "role" string claim from a JWT without
// validating the signature. Returns ("", false) on any malformed input
// so callers can fall back to "unknown".
func jwtRoleClaim(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some JWT issuers emit padded segments. Try standard base64url
		// with padding before giving up.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", false
		}
	}
	var claims struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	if claims.Role == "" {
		return "", false
	}
	return claims.Role, true
}
