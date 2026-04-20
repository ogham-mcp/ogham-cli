package cmd

import (
	"strings"
	"testing"
)

func TestValidateSupabaseURL(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"   ", true},
		{"x.supabase.co", true},
		{"https://x.supabase.co", false},
		{"http://localhost:54321", false},
	}
	for _, tc := range cases {
		err := validateSupabaseURL(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateSupabaseURL(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestValidatePostgresURL(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"host:5432/db", true},
		{"postgres://u:p@h:5432/d", false},
		{"postgresql://u@h/d", false},
	}
	for _, tc := range cases {
		err := validatePostgresURL(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("validatePostgresURL(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestValidators_ErrorMessagesAreActionable(t *testing.T) {
	// Error text should contain actionable hints rather than generic
	// "invalid" messages. Matters because users see these in the huh TUI.
	if err := validateSupabaseURL("example.com"); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Errorf("Supabase validator should mention https://, got %v", err)
	}
	if err := validatePostgresURL("example"); err == nil || !strings.Contains(err.Error(), "postgres://") {
		t.Errorf("Postgres validator should mention postgres://, got %v", err)
	}
}
