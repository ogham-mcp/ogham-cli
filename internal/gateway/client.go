package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL   string
	apiKey    string
	userAgent string
	http      *http.Client
}

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

func (c *Client) Health() (map[string]any, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/health", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse health response: %w", err)
	}
	return result, nil
}

func (c *Client) FetchTools() ([]map[string]any, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/mcp/tools", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch tools failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch tools: status %d", resp.StatusCode)
	}

	var tools []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		return nil, fmt.Errorf("parse tools response: %w", err)
	}
	return tools, nil
}

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

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", toolName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("call tool %s: status %d", toolName, resp.StatusCode)
	}

	// Response can be object or array depending on the tool
	var result any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse tool response: %w", err)
	}
	return result, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
}
