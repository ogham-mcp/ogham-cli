package gateway

import (
	"bytes"
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
// Does NOT retry on 4xx (client errors).
func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	maxRetries := 3
	backoff := 1 * time.Second

	// Save body for retries (if present)
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		_ = req.Body.Close()
	}

	for i := 0; i < maxRetries; i++ {
		// Reset body for each attempt
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.http.Do(req)

		if err != nil {
			// Network error -- retry
			slog.Warn("request failed", "attempt", i+1, "error", err)
		} else if resp.StatusCode < 500 {
			// Success or client error -- don't retry
			return resp, nil
		} else {
			// Server error -- check for Neon cold start
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
			_ = resp.Body.Close() // must close before retry to avoid fd leak
		}

		if i < maxRetries-1 {
			// Exponential backoff: 1s, 2s, 4s
			wait := backoff * time.Duration(1<<i)
			slog.Info("retrying", "wait", wait)
			time.Sleep(wait)
		}
	}

	return nil, fmt.Errorf("failed after %d attempts", maxRetries)
}

// Health checks gateway connectivity.
func (c *Client) Health() (map[string]any, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/health", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
func (c *Client) FetchTools() ([]map[string]any, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/mcp/tools", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
func (c *Client) CallTool(toolName string, arguments map[string]any) (any, error) {
	body := map[string]any{
		"tool":      toolName,
		"arguments": arguments,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/mcp/call", bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
func (c *Client) BulkImport(memories []any) (map[string]any, error) {
	body := map[string]any{
		"memories": memories,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/memories/bulk", bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
func (c *Client) HookSessionStart(cwd, profile string) (string, error) {
	return c.hookContextCall("/api/v1/hooks/session-start", map[string]string{
		"cwd": cwd, "profile": profile,
	})
}

// HookPostTool calls the gateway hooks/post-tool endpoint.
func (c *Client) HookPostTool(toolName string, toolInput map[string]any, cwd, sessionID, profile string) error {
	body := map[string]any{
		"tool_name":  toolName,
		"tool_input": toolInput,
		"cwd":        cwd,
		"session_id": sessionID,
		"profile":    profile,
	}
	_, err := c.hookStatusCall("/api/v1/hooks/post-tool", body)
	return err
}

// HookInscribe calls the gateway hooks/inscribe endpoint.
func (c *Client) HookInscribe(sessionID, cwd, profile string) error {
	body := map[string]string{
		"session_id": sessionID,
		"cwd":        cwd,
		"profile":    profile,
	}
	_, err := c.hookStatusCall("/api/v1/hooks/inscribe", body)
	return err
}

// HookRecall calls the gateway hooks/recall endpoint.
func (c *Client) HookRecall(cwd, profile string) (string, error) {
	return c.hookContextCall("/api/v1/hooks/recall", map[string]string{
		"cwd": cwd, "profile": profile,
	})
}

// hookContextCall makes a POST and returns the "context" field from the response.
func (c *Client) hookContextCall(path string, body any) (string, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
	if ctx, ok := result["context"].(string); ok {
		return ctx, nil
	}
	return "", nil
}

// hookStatusCall makes a POST and checks for success status.
func (c *Client) hookStatusCall(path string, body any) (string, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
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
