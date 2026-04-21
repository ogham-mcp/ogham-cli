package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// bg is a convenience helper for tests that don't care about ctx
// semantics; each call gets a fresh Background so tests stay
// independent of any test-parent ctx conventions.
func bg() context.Context { return context.Background() }

func TestHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("path = %q, want /api/v1/health", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	status, err := c.Health(bg())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if status["status"] != "ok" {
		t.Errorf("status = %v, want ok", status["status"])
	}
}

func TestCallTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/call" {
			t.Errorf("path = %q, want /api/v1/mcp/call", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		if r.Header.Get("User-Agent") != "test/1.0" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tool"] != "store_memory" {
			t.Errorf("tool = %v, want store_memory", body["tool"])
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "stored",
			"id":     "abc-123",
		})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.CallTool(bg(), "store_memory", map[string]any{"content": "test"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result is not map[string]any: %T", result)
	}
	if resultMap["status"] != "stored" {
		t.Errorf("result = %v", result)
	}
}

func TestFetchTools(t *testing.T) {
	tools := []map[string]any{
		{"name": "store_memory", "description": "Store a memory"},
		{"name": "hybrid_search", "description": "Search memories"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/tools" {
			t.Errorf("path = %q, want /api/v1/mcp/tools", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(tools)
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.FetchTools(bg())
	if err != nil {
		t.Fatalf("FetchTools failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d tools, want 2", len(result))
	}
}

func TestAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "my-secret-key" {
			t.Errorf("X-Api-Key = %q, want %q", got, "my-secret-key")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "my-secret-key", "test/1.0")
	_, _ = c.Health(bg())
}

func TestRetryOn500(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"internal"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.Health(bg())
	if err != nil {
		t.Fatalf("Health failed after retries: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("status = %v, want ok", result["status"])
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3", attempts.Load())
	}
}

func TestRetryWithNeonWakingUp(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("X-Neon-Status", "Waking-Up")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"Database warming up"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	result, err := c.Health(bg())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("status = %v, want ok", result["status"])
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2 (1 failure + 1 success)", attempts.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	_, err := c.Health(bg())
	// Health doesn't check status code, so no error -- but only 1 attempt
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts.Load())
	}
}

func TestRetryPreservesBody(t *testing.T) {
	var attempts atomic.Int32
	var lastBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		if n < 2 {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "stored"})
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	_, err := c.CallTool(bg(), "store_memory", map[string]any{"content": "retry test"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	// Body should be preserved across retries
	if lastBody["tool"] != "store_memory" {
		t.Errorf("body not preserved: tool = %v", lastBody["tool"])
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2", attempts.Load())
	}
}

// Cancelling the passed ctx must tear down the gateway call even
// mid-retry: neither the HTTP Do nor the backoff sleep should block
// past cancellation. Guards the v0.5 ctx-plumbing refactor against
// regression.
func TestContextCancel_StopsRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Always return 500 so the retry path kicks in.
		w.WriteHeader(500)
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so the very first Do sees a dead ctx

	start := time.Now()
	_, err := c.Health(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
	// Error should mention context cancellation or be context.Canceled.
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "cancel") {
		t.Logf("error does not mention context/cancel (accepted): %v", err)
	}
	// Must NOT have waited through the 1-second backoff.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (ctx cancel must short-circuit the backoff)", elapsed)
	}
}

// The retry sleep between attempts honours ctx cancellation: a client
// that cancels during the 1s backoff must see the call return quickly,
// not block until the timer fires.
func TestContextCancel_DuringBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	c := New(server.URL, "test-key", "test/1.0")
	// Cancel 150 ms in -- well inside the 1s backoff after the first 500.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Health(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when ctx times out during backoff")
	}
	if elapsed > 600*time.Millisecond {
		t.Errorf("elapsed = %v, want < 600ms (ctx deadline must cut the 1s backoff short)", elapsed)
	}
	// At least one attempt ran; no more than 2 (the second attempt may
	// have started before the deadline fires).
	got := attempts.Load()
	if got < 1 || got > 2 {
		t.Errorf("attempts = %d, want 1 or 2", got)
	}
}
