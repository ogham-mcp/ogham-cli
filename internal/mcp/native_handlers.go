package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// Native MCP tool handlers. Expose the v0.5 native Go pipeline
// (extraction + embed + search + store) over stdio so an MCP client
// (Claude Code, Cursor, etc.) can talk to the Go binary directly,
// without a managed gateway and without a Python sidecar.
//
// Tool names match the Python ogham-mcp sidecar on purpose:
// `store_memory`, `hybrid_search`, `list_recent`, `health_check`.
// An MCP client already wired for the Python server swaps to the
// Go binary without reconfiguring its tool calls.
//
// When native has no implementation for a tool the Python sidecar
// exposes (compression, graph, stats subsets), the client sees a
// "tool not found" MCP error -- expected behaviour until those
// absorb in v0.6.

// errorResult renders a CallToolResult carrying an IsError flag.
// MCP spec: tool errors must be IsError=true with Content explaining,
// never a Go error (that maps to a transport-level failure instead).
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("marshal result: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
	}, nil
}

// -----------------------------------------------------------------------
// store_memory
// -----------------------------------------------------------------------

func BuildNativeStoreHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Content string   `json:"content"`
			Tags    []string `json:"tags,omitempty"`
			Source  string   `json:"source,omitempty"`
			Profile string   `json:"profile,omitempty"`
			DryRun  bool     `json:"dry_run,omitempty"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult(fmt.Sprintf("parse arguments: %v", err)), nil
			}
		}
		if args.Content == "" {
			return errorResult("store_memory: content is required"), nil
		}

		result, err := native.Store(ctx, cfg, args.Content, native.StoreOptions{
			Tags:    args.Tags,
			Source:  args.Source,
			Profile: args.Profile,
			DryRun:  args.DryRun,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

// -----------------------------------------------------------------------
// hybrid_search
// -----------------------------------------------------------------------

func BuildNativeSearchHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Query        string   `json:"query"`
			Limit        int      `json:"limit,omitempty"`
			Tags         []string `json:"tags,omitempty"`
			Source       string   `json:"source,omitempty"`
			Profile      string   `json:"profile,omitempty"`
			Profiles     []string `json:"profiles,omitempty"`
			EntityTags   []string `json:"query_entity_tags,omitempty"`
			RecencyDecay float64  `json:"recency_decay,omitempty"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult(fmt.Sprintf("parse arguments: %v", err)), nil
			}
		}
		if args.Query == "" {
			return errorResult("hybrid_search: query is required"), nil
		}

		results, err := native.Search(ctx, cfg, args.Query, native.SearchOptions{
			Limit:        args.Limit,
			Tags:         args.Tags,
			Source:       args.Source,
			Profile:      args.Profile,
			Profiles:     args.Profiles,
			EntityTags:   args.EntityTags,
			RecencyDecay: args.RecencyDecay,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"result": results,
			"count":  len(results),
		})
	}
}

// -----------------------------------------------------------------------
// list_recent
// -----------------------------------------------------------------------

func BuildNativeListHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Limit  int      `json:"limit,omitempty"`
			Source string   `json:"source,omitempty"`
			Tags   []string `json:"tags,omitempty"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult(fmt.Sprintf("parse arguments: %v", err)), nil
			}
		}

		results, err := native.List(ctx, cfg, native.ListOptions{
			Limit:  args.Limit,
			Source: args.Source,
			Tags:   args.Tags,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"result": results,
			"count":  len(results),
		})
	}
}

// -----------------------------------------------------------------------
// health_check
// -----------------------------------------------------------------------

func BuildNativeHealthHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			LiveEmbedder bool `json:"live_embedder,omitempty"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult(fmt.Sprintf("parse arguments: %v", err)), nil
			}
		}

		report, err := native.HealthCheck(ctx, cfg, native.HealthOptions{
			LiveEmbedder: args.LiveEmbedder,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(report)
	}
}

// -----------------------------------------------------------------------
// RegisterNativeTools installs the four v0.5 native tool handlers on the
// MCP server. Returns the list of tool names registered for logging.
// -----------------------------------------------------------------------

func RegisterNativeTools(server *mcp.Server, cfg *native.Config) []string {
	names := []string{}

	server.AddTool(&mcp.Tool{
		Name:        "store_memory",
		Description: "Store a memory in the active profile. Runs native extraction (entities, dates, importance) + parallel embed + surprise scoring + auto-link before writing to the configured backend.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"content":  { "type": "string", "description": "The memory content to store." },
				"tags":     { "type": "array",  "items": {"type": "string"}, "description": "Optional tags (e.g. [\"type:decision\", \"project:ogham\"])." },
				"source":   { "type": "string", "description": "Source label (e.g. claude-code)." },
				"profile":  { "type": "string", "description": "Profile override; defaults to the config profile." },
				"dry_run":  { "type": "boolean", "description": "Run extraction + embed + search without writing to the backend." }
			},
			"required": ["content"]
		}`),
	}, BuildNativeStoreHandler(cfg))
	names = append(names, "store_memory")

	server.AddTool(&mcp.Tool{
		Name:        "hybrid_search",
		Description: "Hybrid search (vector + keyword RRF) across memories in the active profile. Returns ranked results with similarity + relevance scores.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query":             { "type": "string", "description": "Natural-language query." },
				"limit":             { "type": "integer", "description": "Max results to return (default 10)." },
				"tags":              { "type": "array", "items": {"type": "string"} },
				"source":            { "type": "string" },
				"profile":           { "type": "string" },
				"profiles":          { "type": "array", "items": {"type": "string"}, "description": "Cross-profile search." },
				"query_entity_tags": { "type": "array", "items": {"type": "string"} },
				"recency_decay":     { "type": "number", "description": "0 disables, >0 gates decay by query type." }
			},
			"required": ["query"]
		}`),
	}, BuildNativeSearchHandler(cfg))
	names = append(names, "hybrid_search")

	server.AddTool(&mcp.Tool{
		Name:        "list_recent",
		Description: "List the most recent memories in the active profile, optionally filtered by source or tags.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit":  { "type": "integer", "description": "Max rows (default 20)." },
				"source": { "type": "string" },
				"tags":   { "type": "array", "items": {"type": "string"} }
			}
		}`),
	}, BuildNativeListHandler(cfg))
	names = append(names, "list_recent")

	server.AddTool(&mcp.Tool{
		Name:        "health_check",
		Description: "Probe the configured backend + embedder. Set live_embedder=true to burn a real provider token.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"live_embedder": { "type": "boolean", "description": "Make a real provider API call (costs a token)." }
			}
		}`),
	}, BuildNativeHealthHandler(cfg))
	names = append(names, "health_check")

	return names
}
