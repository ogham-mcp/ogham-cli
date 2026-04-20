package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// ProfileCount is one row from get_profile_counts(): a profile name
// plus the number of non-expired memories in it.
type ProfileCount struct {
	Profile string `json:"profile"`
	Count   int64  `json:"count"`
}

// ProfileTTL describes a row from profile_settings. TTLDays is nil when
// the profile has no explicit TTL configured (memories then never expire
// unless the caller sets expires_at directly on store).
type ProfileTTL struct {
	Profile string `json:"profile"`
	TTLDays *int   `json:"ttl_days"`
}

// ListProfiles returns every profile that currently holds at least one
// non-expired memory. Matches Python's list_profiles tool which calls
// the get_profile_counts() function.
func ListProfiles(ctx context.Context, cfg *Config) ([]ProfileCount, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native profiles: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, fmt.Errorf("native profiles: %w", err)
	}
	switch backend {
	case "supabase":
		return listProfilesSupabase(ctx, cfg)
	case "postgres":
		return listProfilesPostgres(ctx, cfg)
	default:
		return nil, fmt.Errorf("native profiles: unknown backend %q", backend)
	}
}

func listProfilesPostgres(ctx context.Context, cfg *Config) ([]ProfileCount, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native profiles: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, "SELECT profile, count FROM get_profile_counts()")
	if err != nil {
		return nil, fmt.Errorf("native profiles: query: %w", err)
	}
	defer rows.Close()

	var out []ProfileCount
	for rows.Next() {
		var p ProfileCount
		if err := rows.Scan(&p.Profile, &p.Count); err != nil {
			return nil, fmt.Errorf("native profiles: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func listProfilesSupabase(ctx context.Context, cfg *Config) ([]ProfileCount, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	raw, err := client.callRPC(ctx, "get_profile_counts", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out []ProfileCount
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("native profiles: parse: %w (body: %s)", err, truncateForError(raw))
	}
	return out, nil
}

// GetProfileTTL reads the TTL (days) currently configured for a profile.
// A nil return (no row + no error) means no TTL is configured.
func GetProfileTTL(ctx context.Context, cfg *Config, profile string) (*ProfileTTL, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native profile ttl: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	if profile == "" {
		profile = cfg.Profile
	}
	if profile == "" {
		profile = "default"
	}
	switch backend {
	case "supabase":
		return getProfileTTLSupabase(ctx, cfg, profile)
	case "postgres":
		return getProfileTTLPostgres(ctx, cfg, profile)
	default:
		return nil, fmt.Errorf("native profile ttl: unknown backend %q", backend)
	}
}

func getProfileTTLPostgres(ctx context.Context, cfg *Config, profile string) (*ProfileTTL, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native profile ttl: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var ttl *int
	err = conn.QueryRow(ctx,
		"SELECT ttl_days FROM profile_settings WHERE profile = $1",
		profile).Scan(&ttl)
	if err != nil {
		if err == pgx.ErrNoRows {
			return &ProfileTTL{Profile: profile}, nil
		}
		return nil, fmt.Errorf("native profile ttl: query: %w", err)
	}
	return &ProfileTTL{Profile: profile, TTLDays: ttl}, nil
}

func getProfileTTLSupabase(ctx context.Context, cfg *Config, profile string) (*ProfileTTL, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("select", "profile,ttl_days")
	q.Set("profile", "eq."+profile)
	q.Set("limit", "1")

	endpoint := client.baseURL + "/profile_settings?" + q.Encode()
	raw, err := client.getJSON(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var rows []ProfileTTL
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native profile ttl: parse: %w (body: %s)", err, truncateForError(raw))
	}
	if len(rows) == 0 {
		return &ProfileTTL{Profile: profile}, nil
	}
	return &rows[0], nil
}

// SetProfileTTL upserts a TTL for a profile. Pass a negative ttlDays to
// clear the setting entirely (match Python's None semantics).
func SetProfileTTL(ctx context.Context, cfg *Config, profile string, ttlDays int) (*ProfileTTL, error) {
	if cfg == nil {
		return nil, fmt.Errorf("native profile ttl: nil config")
	}
	backend, err := cfg.ResolveBackend()
	if err != nil {
		return nil, err
	}
	if profile == "" {
		return nil, fmt.Errorf("native profile ttl: profile is required")
	}
	switch backend {
	case "supabase":
		return setProfileTTLSupabase(ctx, cfg, profile, ttlDays)
	case "postgres":
		return setProfileTTLPostgres(ctx, cfg, profile, ttlDays)
	default:
		return nil, fmt.Errorf("native profile ttl: unknown backend %q", backend)
	}
}

func setProfileTTLPostgres(ctx context.Context, cfg *Config, profile string, ttlDays int) (*ProfileTTL, error) {
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("native profile ttl: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var ttlArg any
	if ttlDays >= 0 {
		ttlArg = ttlDays
	}
	var ttl *int
	err = conn.QueryRow(ctx, `
		INSERT INTO profile_settings (profile, ttl_days)
		VALUES ($1, $2)
		ON CONFLICT (profile) DO UPDATE SET ttl_days = EXCLUDED.ttl_days, updated_at = now()
		RETURNING ttl_days`,
		profile, ttlArg).Scan(&ttl)
	if err != nil {
		return nil, fmt.Errorf("native profile ttl: upsert: %w", err)
	}
	return &ProfileTTL{Profile: profile, TTLDays: ttl}, nil
}

func setProfileTTLSupabase(ctx context.Context, cfg *Config, profile string, ttlDays int) (*ProfileTTL, error) {
	client, err := newSupabaseClient(cfg)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"profile": profile}
	if ttlDays >= 0 {
		body["ttl_days"] = ttlDays
	} else {
		body["ttl_days"] = nil
	}

	raw, err := client.postJSON(ctx, "/profile_settings", body, map[string]string{
		// PostgREST upsert semantics: ask the server to return the
		// representation, and tell it to resolve conflicts on the
		// primary-key column.
		"Prefer": "return=representation,resolution=merge-duplicates",
	})
	if err != nil {
		return nil, err
	}
	var rows []ProfileTTL
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("native profile ttl: parse: %w (body: %s)", err, truncateForError(raw))
	}
	if len(rows) == 0 {
		return &ProfileTTL{Profile: profile}, nil
	}
	return &rows[0], nil
}
