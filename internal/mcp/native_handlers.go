package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
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
// Schemas are generated from the argument structs via
// github.com/invopop/jsonschema so signatures + schemas can't drift:
// adding a field to a StoreMemoryArgs automatically shows up in
// tools/list. No hand-written JSON to keep in sync.

// ---------------------------------------------------------------------
// Argument types. jsonschema + json tags drive both unmarshaling AND
// the schema advertised over MCP tools/list.
// ---------------------------------------------------------------------

type storeMemoryArgs struct {
	Content string   `json:"content"                    jsonschema:"required,description=The memory content to store."`
	Tags    []string `json:"tags,omitempty"             jsonschema:"description=Optional tags (e.g. type:decision, project:ogham)."`
	Source  string   `json:"source,omitempty"           jsonschema:"description=Source label (e.g. claude-code)."`
	Profile string   `json:"profile,omitempty"          jsonschema:"description=Profile override; defaults to the config profile."`
	DryRun  bool     `json:"dry_run,omitempty"          jsonschema:"description=Run extraction + embed + search without writing to the backend."`
}

type hybridSearchArgs struct {
	Query           string   `json:"query"                      jsonschema:"required,description=Natural-language query."`
	Limit           int      `json:"limit,omitempty"            jsonschema:"description=Max results to return (default 10)."`
	Tags            []string `json:"tags,omitempty"             jsonschema:"description=Filter results to memories with any of these tags."`
	Source          string   `json:"source,omitempty"           jsonschema:"description=Filter results to memories from this source."`
	Profile         string   `json:"profile,omitempty"          jsonschema:"description=Single-profile override; empty = config profile."`
	Profiles        []string `json:"profiles,omitempty"         jsonschema:"description=Search across multiple profiles (overrides profile)."`
	QueryEntityTags []string `json:"query_entity_tags,omitempty" jsonschema:"description=Entity-tag-conditioned re-ranking."`
	RecencyDecay    float64  `json:"recency_decay,omitempty"    jsonschema:"description=0 disables; >0 biases toward newer memories."`
}

type listRecentArgs struct {
	Limit  int      `json:"limit,omitempty"  jsonschema:"description=Max rows (default 20)."`
	Source string   `json:"source,omitempty" jsonschema:"description=Filter to memories from this source."`
	Tags   []string `json:"tags,omitempty"   jsonschema:"description=Filter to memories with any of these tags."`
}

type healthCheckArgs struct {
	LiveEmbedder bool `json:"live_embedder,omitempty" jsonschema:"description=Make a real provider API call (costs a token)."`
}

// ---------------------------------------------------------------------
// schemaFor reflects a Go struct's jsonschema tags into the inline JSON
// Schema shape MCP expects. DoNotReference collapses nested types so
// the output is one flat object, not a bag of $refs.
// ---------------------------------------------------------------------

func schemaFor(v any) json.RawMessage {
	r := &jsonschema.Reflector{
		DoNotReference:             true,
		AllowAdditionalProperties:  true,
		RequiredFromJSONSchemaTags: true,
	}
	schema := r.ReflectFromType(reflect.TypeOf(v))
	// Drop $schema / $id / definitions wrappers -- MCP tools/list wants
	// the object schema directly.
	schema.ID = ""
	schema.Version = ""
	raw, err := json.Marshal(schema)
	if err != nil {
		// Should never happen for a static Go struct; fall back to a
		// permissive schema so the tool still registers.
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

// ---------------------------------------------------------------------
// Handler factories. One per tool. Each: unmarshal args, call native,
// shape result as CallToolResult.
// ---------------------------------------------------------------------

// errorResult renders a CallToolResult carrying an IsError flag. MCP
// spec: tool errors must be IsError=true with Content explaining,
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

func unmarshalArgs(req *mcp.CallToolRequest, dst any) *mcp.CallToolResult {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params.Arguments, dst); err != nil {
		return errorResult(fmt.Sprintf("parse arguments: %v", err))
	}
	return nil
}

func BuildNativeStoreHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args storeMemoryArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
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

func BuildNativeSearchHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args hybridSearchArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
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
			EntityTags:   args.QueryEntityTags,
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

func BuildNativeListHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listRecentArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
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

func BuildNativeHealthHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args healthCheckArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
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

// ---------------------------------------------------------------------
// RegisterNativeTools installs the v0.5 native tool handlers on the MCP
// server. Returns the set of tool names registered so the proxy path
// can skip them (native wins on name collision).
// ---------------------------------------------------------------------

func RegisterNativeTools(server *mcp.Server, cfg *native.Config) map[string]struct{} {
	tools := []struct {
		name        string
		description string
		schemaProto any
		handler     mcp.ToolHandler
	}{
		{
			"store_memory",
			"Store a memory in the active profile. Runs native extraction (entities, dates, importance) + parallel embed + surprise scoring + auto-link before writing to the configured backend.",
			storeMemoryArgs{},
			BuildNativeStoreHandler(cfg),
		},
		{
			"hybrid_search",
			"Hybrid search (vector + keyword RRF) across memories in the active profile. Returns ranked results with similarity + relevance scores.",
			hybridSearchArgs{},
			BuildNativeSearchHandler(cfg),
		},
		{
			"list_recent",
			"List the most recent memories in the active profile, optionally filtered by source or tags.",
			listRecentArgs{},
			BuildNativeListHandler(cfg),
		},
		{
			"health_check",
			"Probe the configured backend + embedder. Set live_embedder=true to burn a real provider token.",
			healthCheckArgs{},
			BuildNativeHealthHandler(cfg),
		},
	}

	names := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		server.AddTool(&mcp.Tool{
			Name:        t.name,
			Description: t.description,
			InputSchema: schemaFor(t.schemaProto),
		}, t.handler)
		names[t.name] = struct{}{}
	}
	return names
}
