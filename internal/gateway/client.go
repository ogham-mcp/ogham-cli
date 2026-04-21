package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client is an HTTP client for the Ogham gateway REST API.
type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
}

// New creates a gateway client with 60s timeout.
func New(baseURL, apiKey, userAgent string) *Client {
	return &Client{
		baseURL:   baseURL,
		apiKey:    apiKey,
		userAgent: userAgent,
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// doWithRetry executes an HTTP request with exponential backoff.
// Retries on network errors, 500+, and 503 with X-Neon-Status: Waking-Up.
// Does NOT retry on 4xx (client errors). Honours ctx cancellation for
// both the per-request Do and the between-attempt sleep -- Ctrl+C on
// the CLI tears down the in-flight gateway call cleanly.
func (c *Client) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	maxRetries := 3
	backoff := 1 * time.Second

	// Save body for retries (if present).
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		_ = req.Body.Close()
	}

	var lastErr error
	var lastStatus int
	for i := 0; i < maxRetries; i++ {
		// Each attempt gets a fresh body + a ctx-carrying request.
		attempt := req.Clone(ctx)
		if bodyBytes != nil {
			attempt.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.http.Do(attempt)
		if err != nil {
			// Network error -- retry, unless ctx is already done.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			slog.Warn("request failed", "attempt", i+1, "error", err)
		} else if resp.StatusCode < 500 {
			// Success or client error -- don't retry.
			return resp, nil
		} else {
			// Server error -- check for Neon cold start.
			neonStatus := resp.Header.Get("X-Neon-Status")
			if neonStatus == "Waking-Up" {
				slog.Info("neon waking up, will retry",
					"attempt", i+1,
					"retry_after", resp.Header.Get("Retry-After"),
				)
			} else {
				slog.Warn("server error",
					"status", resp.StatusCode,
					"attempt", i+1,
				)
			}
			lastStatus = resp.StatusCode
			_ = resp.Body.Close() // must close before retry to avoid fd leak
		}

		if i < maxRetries-1 {
			// Exponential backoff: 1s, 2s, 4s -- cancellable.
			wait := backoff * time.Duration(1<<i)
			slog.Info("retrying", "wait", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
	}
	return nil, fmt.Errorf("failed after %d attempts (last status %d)", maxRetries, lastStatus)
}

// Health checks gateway connectivity.
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/health", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse health response: %w", err)
	}
	return result, nil
}

// FetchTools retrieves the MCP tool manifest from the gateway.
func (c *Client) FetchTools(ctx context.Context) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/mcp/tools", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch tools failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch tools: status %d", resp.StatusCode)
	}

	var tools []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		return nil, fmt.Errorf("parse tools response: %w", err)
	}
	return tools, nil
}

// CallTool executes a tool call via the gateway.
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]any) (any, error) {
	body := map[string]any{
		"tool":      toolName,
		"arguments": arguments,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/mcp/call", bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", toolName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("call tool %s: status %d", toolName, resp.StatusCode)
	}

	var result any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse tool response: %w", err)
	}
	return result, nil
}

// BulkImport sends a batch of memories to the gateway bulk import endpoint.
func (c *Client) BulkImport(ctx context.Context, memories []any) (map[string]any, error) {
	body := map[string]any{
		"memories": memories,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/memories/bulk", bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("bulk import: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bulk import: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse bulk import response: %w", err)
	}
	return result, nil
}

// HookSessionStart calls the gateway hooks/session-start endpoint.
func (c *Client) HookSessionStart(ctx context.Context, cwd, profile string) (string, error) {
	return c.hookContextCall(ctx, "/api/v1/hooks/session-start", map[string]string{
		"cwd": cwd, "profile": profile,
	})
}

// HookPostTool calls the gateway hooks/post-tool endpoint.
func (c *Client) HookPostTool(ctx context.Context, toolName string, toolInput map[string]any, cwd, sessionID, profile string) error {
	body := map[string]any{
		"tool_name":  toolName,
		"tool_input": toolInput,
		"cwd":        cwd,
		"session_id": sessionID,
		"profile":    profile,
	}
	_, err := c.hookStatusCall(ctx, "/api/v1/hooks/post-tool", body)
	return err
}

// HookInscribe calls the gateway hooks/inscribe endpoint.
func (c *Client) HookInscribe(ctx context.Context, sessionID, cwd, profile string) error {
	body := map[string]string{
		"session_id": sessionID,
		"cwd":        cwd,
		"profile":    profile,
	}
	_, err := c.hookStatusCall(ctx, "/api/v1/hooks/inscribe", body)
	return err
}

// HookRecall calls the gateway hooks/recall endpoint.
func (c *Client) HookRecall(ctx context.Context, cwd, profile string) (string, error) {
	return c.hookContextCall(ctx, "/api/v1/hooks/recall", map[string]string{
		"cwd": cwd, "profile": profile,
	})
}

// hookContextCall makes a POST and returns the "context" field from the response.
func (c *Client) hookContextCall(ctx context.Context, path string, body any) (string, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return "", fmt.Errorf("hook %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("hook %s: status %d: %s", path, resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse hook response: %w", err)
	}
	if ctxStr, ok := result["context"].(string); ok {
		return ctxStr, nil
	}
	return "", nil
}

// hookStatusCall makes a POST and checks for success status.
func (c *Client) hookStatusCall(ctx context.Context, path string, body any) (string, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return "", fmt.Errorf("hook %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("hook %s: status %d: %s", path, resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse hook response: %w", err)
	}
	if status, ok := result["status"].(string); ok {
		return status, nil
	}
	return "ok", nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
}
