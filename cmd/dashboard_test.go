package cmd

import (
	"testing"
)

// TestAllowedDashboardHosts guards the loopback-only security boundary.
// Adding an entry here without a corresponding design doc update (v0.9
// remote access plan) is a bug.
func TestAllowedDashboardHosts(t *testing.T) {
	want := map[string]bool{
		"127.0.0.1": true,
		"localhost": true,
		"::1":       true,
	}
	for host, ok := range want {
		if _, got := allowedDashboardHosts[host]; got != ok {
			t.Errorf("allowedDashboardHosts[%q]: got %v, want %v", host, got, ok)
		}
	}
	// Non-loopback must be rejected.
	for _, bad := range []string{"0.0.0.0", "192.168.1.1", "example.com"} {
		if _, got := allowedDashboardHosts[bad]; got {
			t.Errorf("allowedDashboardHosts[%q]: unexpectedly allowed", bad)
		}
	}
}

// TestListenerFor binds a random port on localhost and confirms the
// helper returns a valid listener. Guards against a regression where a
// JoinHostPort format error would surface only at runtime.
func TestListenerFor(t *testing.T) {
	l, err := listenerFor("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listenerFor: %v", err)
	}
	defer func() { _ = l.Close() }()
	if l.Addr().String() == "" {
		t.Errorf("listener returned empty addr")
	}
}
