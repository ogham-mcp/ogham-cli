package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/native/cache"
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

type deleteMemoryArgs struct {
	ID      string `json:"id"                jsonschema:"required,description=UUID of the memory to delete."`
	Profile string `json:"profile,omitempty" jsonschema:"description=Profile override; defaults to the config profile."`
}

type cleanupExpiredArgs struct {
	Profile string `json:"profile,omitempty" jsonschema:"description=Profile to sweep; defaults to the config profile."`
}

type listProfilesArgs struct{}

type setProfileTTLArgs struct {
	Profile string `json:"profile"            jsonschema:"required,description=Profile whose TTL to update."`
	TTLDays int    `json:"ttl_days,omitempty" jsonschema:"description=Days until a memory in this profile expires. Pass -1 to clear the TTL and keep memories forever."`
	Clear   bool   `json:"clear,omitempty"    jsonschema:"description=If true, clear the TTL (equivalent to ttl_days=-1)."`
}

type reinforceMemoryArgs struct {
	ID       string  `json:"memory_id"           jsonschema:"required,description=UUID of the memory to reinforce."`
	Strength float64 `json:"strength,omitempty"  jsonschema:"description=Reinforcement strength in (0.0, 1.0]. Default 0.85. Higher = stronger boost to confidence."`
}

type contradictMemoryArgs struct {
	ID       string  `json:"memory_id"           jsonschema:"required,description=UUID of the memory to contradict."`
	Strength float64 `json:"strength,omitempty"  jsonschema:"description=Contradiction strength in [0.0, 1.0). Default 0.15. Lower = stronger push toward 0 confidence."`
}

type switchProfileArgs struct {
	Profile string `json:"profile" jsonschema:"required,description=Profile name to activate. Becomes the active profile for subsequent tool calls in this session and persists to ~/.ogham/active_profile so later CLI invocations see it too."`
}

type currentProfileArgs struct{}

// --- Batch B typed-store argument structs ---------------------------------
//
// These mirror the Python ogham-mcp tools (store_decision, store_fact,
// store_event, store_preference) exactly so an MCP client already wired
// against Python swaps to the Go binary without rewriting tool calls.
// Each wraps native.Store() with a type-specific tag + structured
// metadata payload.

type storeDecisionArgs struct {
	Decision        string   `json:"decision"                    jsonschema:"required,description=What was decided."`
	Rationale       string   `json:"rationale"                   jsonschema:"required,description=Why -- the reasoning behind the decision."`
	Alternatives    []string `json:"alternatives,omitempty"      jsonschema:"description=What was considered and rejected."`
	ReasoningTrace  string   `json:"reasoning_trace,omitempty"   jsonschema:"description=Optional longer-form reasoning narrative."`
	Tags            []string `json:"tags,omitempty"              jsonschema:"description=Additional tags; type:decision is always added."`
	RelatedMemories []string `json:"related_memories,omitempty"  jsonschema:"description=IDs to link to after store. Not yet native -- pending Batch E graph walk."`
	Source          string   `json:"source,omitempty"            jsonschema:"description=Where this decision was recorded (the MCP client / tool)."`
	Profile         string   `json:"profile,omitempty"           jsonschema:"description=Profile override; defaults to the active profile."`
}

type storeFactArgs struct {
	Fact           string   `json:"fact"                       jsonschema:"required,description=The factual statement."`
	Subject        string   `json:"subject,omitempty"          jsonschema:"description=Optional subject/entity the fact is about."`
	Confidence     float64  `json:"confidence,omitempty"       jsonschema:"description=Confidence in [0.0, 1.0]. Default 1.0."`
	SourceCitation string   `json:"source_citation,omitempty"  jsonschema:"description=Optional citation (paper, URL, who-said-it)."`
	Tags           []string `json:"tags,omitempty"             jsonschema:"description=Additional tags; type:fact is always added."`
	Source         string   `json:"source,omitempty"           jsonschema:"description=Where this fact was recorded."`
	Profile        string   `json:"profile,omitempty"          jsonschema:"description=Profile override; defaults to the active profile."`
}

type storeEventArgs struct {
	Event        string   `json:"event"                 jsonschema:"required,description=What happened."`
	When         string   `json:"when,omitempty"        jsonschema:"description=Time expression (verbatim; extraction layer parses it)."`
	Participants []string `json:"participants,omitempty" jsonschema:"description=People or entities involved."`
	Location     string   `json:"location,omitempty"    jsonschema:"description=Place name."`
	Tags         []string `json:"tags,omitempty"        jsonschema:"description=Additional tags; type:event is always added."`
	Source       string   `json:"source,omitempty"      jsonschema:"description=Where this event was recorded."`
	Profile      string   `json:"profile,omitempty"     jsonschema:"description=Profile override; defaults to the active profile."`
}

// --- Batch E graph walk args ---------------------------------------------

type linkUnlinkedArgs struct {
	BatchSize int     `json:"batch_size,omitempty" jsonschema:"description=Memories to process per call. Default 100."`
	Threshold float64 `json:"threshold,omitempty"  jsonschema:"description=Minimum similarity to create a link. Default 0.85."`
	MaxLinks  int     `json:"max_links,omitempty"  jsonschema:"description=Maximum links per memory. Default 5."`
}

type exploreKnowledgeArgs struct {
	Query       string   `json:"query"                   jsonschema:"required,description=Natural language query."`
	Depth       int      `json:"depth,omitempty"         jsonschema:"description=Relationship hops to traverse. 0 = seed only. Default 1."`
	MinStrength float64  `json:"min_strength,omitempty"  jsonschema:"description=Minimum edge strength to traverse. Default 0.5."`
	Limit       int      `json:"limit,omitempty"         jsonschema:"description=Seed match count. Default 5."`
	Tags        []string `json:"tags,omitempty"          jsonschema:"description=Filter seed results to memories with any of these tags."`
	Source      string   `json:"source,omitempty"        jsonschema:"description=Filter seed results to this source."`
}

type findRelatedArgs struct {
	MemoryID          string   `json:"memory_id"                   jsonschema:"required,description=UUID of the starting memory."`
	RelationshipTypes []string `json:"relationship_types,omitempty" jsonschema:"description=Filter edge types (e.g. supports, contradicts, supersedes)."`
	Depth             int      `json:"depth,omitempty"             jsonschema:"description=Hops to traverse. Default 1."`
	MinStrength       float64  `json:"min_strength,omitempty"      jsonschema:"description=Minimum edge strength. Default 0.5."`
	Limit             int      `json:"limit,omitempty"             jsonschema:"description=Maximum results. Default 20."`
}

type suggestConnectionsArgs struct {
	MemoryID          string `json:"memory_id"                     jsonschema:"required,description=UUID of the starting memory."`
	MinSharedEntities int    `json:"min_shared_entities,omitempty" jsonschema:"description=Minimum entities in common. Default 2."`
	Limit             int    `json:"limit,omitempty"               jsonschema:"description=Maximum suggestions. Default 10."`
}

// --- Batch C stats / config args (all take no parameters) ---------------

type getConfigArgs struct{}
type getStatsArgs struct{}
type getCacheStatsArgs struct{}

type storePreferenceArgs struct {
	Preference   string   `json:"preference"             jsonschema:"required,description=What is preferred (e.g. 'dark mode', 'PostgreSQL over MySQL')."`
	Subject      string   `json:"subject,omitempty"      jsonschema:"description=Subject/entity the preference applies to."`
	Alternatives []string `json:"alternatives,omitempty" jsonschema:"description=Rejected alternatives."`
	Strength     string   `json:"strength,omitempty"     jsonschema:"description=One of: strong, normal, weak. Default 'normal'."`
	Tags         []string `json:"tags,omitempty"         jsonschema:"description=Additional tags; type:preference is always added."`
	Source       string   `json:"source,omitempty"       jsonschema:"description=Where this preference was recorded."`
	Profile      string   `json:"profile,omitempty"      jsonschema:"description=Profile override; defaults to the active profile."`
}

// updateMemoryArgs uses pointer + nil-slice semantics so we can distinguish
// an omitted field (leave untouched) from an explicit clear ([] / {}).
// Content is *string: nil = untouched, "" = set to empty string, "..." = replace.
// Tags/Metadata use nil-slice / nil-map semantics instead of pointers, which
// encoding/json handles cleanly (absent + explicit null both decode to nil).
type updateMemoryArgs struct {
	ID       string         `json:"memory_id"          jsonschema:"required,description=UUID of the memory to update."`
	Content  *string        `json:"content,omitempty"  jsonschema:"description=New content. Triggers re-embedding. Omit to leave unchanged."`
	Tags     []string       `json:"tags,omitempty"     jsonschema:"description=Replace tags entirely. Pass an empty array to clear. Omit to leave unchanged."`
	Metadata map[string]any `json:"metadata,omitempty" jsonschema:"description=Replace metadata entirely. Pass an empty object to clear. Omit to leave unchanged."`
	Profile  string         `json:"profile,omitempty"  jsonschema:"description=Profile override; defaults to the config profile."`
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

func BuildNativeDeleteHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args deleteMemoryArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.ID == "" {
			return errorResult("delete_memory: id is required"), nil
		}
		result, err := native.Delete(ctx, cfg, args.ID, args.Profile)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeCleanupHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cleanupExpiredArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.Cleanup(ctx, cfg, args.Profile)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeListProfilesHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// list_profiles takes no arguments but MCP clients can still send
		// {} or an empty payload. unmarshalArgs handles both shapes.
		var args listProfilesArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		profiles, err := native.ListProfiles(ctx, cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"result": profiles,
			"count":  len(profiles),
		})
	}
}

// reinforceDefault / contradictDefault mirror the Python tool defaults.
// Kept as const so test + handler agree.
const (
	reinforceDefaultStrength = 0.85
	contradictDefaultStrength = 0.15
)

func BuildNativeReinforceHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args reinforceMemoryArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.ID == "" {
			return errorResult("reinforce_memory: memory_id is required"), nil
		}
		strength := args.Strength
		if strength == 0 {
			strength = reinforceDefaultStrength
		}
		// Python: 0 < strength <= 1.0
		if strength <= 0 || strength > 1.0 {
			return errorResult(fmt.Sprintf("reinforce_memory: strength must be in (0.0, 1.0]; got %v", strength)), nil
		}
		result, err := native.UpdateConfidence(ctx, cfg, args.ID, strength, "")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"status":     "reinforced",
			"id":         result.ID,
			"profile":    result.Profile,
			"confidence": result.Confidence,
		})
	}
}

// --- Batch E graph-walk handlers -----------------------------------------

func BuildNativeLinkUnlinkedHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args linkUnlinkedArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.LinkUnlinked(ctx, cfg, native.LinkUnlinkedOptions{
			BatchSize: args.BatchSize,
			Threshold: args.Threshold,
			MaxLinks:  args.MaxLinks,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeExploreKnowledgeHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args exploreKnowledgeArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		results, err := native.ExploreKnowledge(ctx, cfg, args.Query, native.ExploreOptions{
			Depth:       args.Depth,
			MinStrength: args.MinStrength,
			Limit:       args.Limit,
			Tags:        args.Tags,
			Source:      args.Source,
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

func BuildNativeFindRelatedHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args findRelatedArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.MemoryID == "" {
			return errorResult("find_related: memory_id is required"), nil
		}
		results, err := native.FindRelated(ctx, cfg, args.MemoryID, native.FindRelatedOptions{
			Depth:             args.Depth,
			MinStrength:       args.MinStrength,
			RelationshipTypes: args.RelationshipTypes,
			Limit:             args.Limit,
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

func BuildNativeSuggestConnectionsHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args suggestConnectionsArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.MemoryID == "" {
			return errorResult("suggest_connections: memory_id is required"), nil
		}
		results, err := native.SuggestConnections(ctx, cfg, args.MemoryID, args.MinSharedEntities, args.Limit)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"result": results,
			"count":  len(results),
		})
	}
}

// --- Batch C stats / config handlers --------------------------------------

func BuildNativeGetConfigHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getConfigArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		// native.Mask redacts URL passwords + API keys so the result is
		// safe for an MCP client or log stream. Mirrors what `ogham
		// config show` emits via the CLI.
		return jsonResult(native.Mask(cfg))
	}
}

func BuildNativeGetStatsHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getStatsArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		stats, err := native.GetStats(ctx, cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(stats)
	}
}

func BuildNativeGetCacheStatsHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args getCacheStatsArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		// cache.Default() lazily opens ~/.cache/ogham/embeddings.db on
		// first call. We don't Close() after -- the cache is a shared
		// singleton consumed by the embedders, so closing here would
		// break the next store_memory. Tests exercise Open/Close
		// explicitly.
		c, err := cache.Default()
		if err != nil {
			return errorResult(fmt.Sprintf("cache open: %v", err)), nil
		}
		stats, err := c.Stats()
		if err != nil {
			return errorResult(fmt.Sprintf("cache stats: %v", err)), nil
		}
		return jsonResult(stats)
	}
}

// --- Batch B typed-store handlers -----------------------------------------

func BuildNativeStoreDecisionHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args storeDecisionArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.StoreDecision(ctx, cfg, native.StoreDecisionOptions{
			Decision:        args.Decision,
			Rationale:       args.Rationale,
			Alternatives:    args.Alternatives,
			ReasoningTrace:  args.ReasoningTrace,
			Tags:            args.Tags,
			RelatedMemories: args.RelatedMemories,
			Source:          args.Source,
			Profile:         args.Profile,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeStoreFactHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args storeFactArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.StoreFact(ctx, cfg, native.StoreFactOptions{
			Fact:           args.Fact,
			Subject:        args.Subject,
			Confidence:     args.Confidence,
			SourceCitation: args.SourceCitation,
			Tags:           args.Tags,
			Source:         args.Source,
			Profile:        args.Profile,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeStoreEventHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args storeEventArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.StoreEvent(ctx, cfg, native.StoreEventOptions{
			Event:        args.Event,
			When:         args.When,
			Participants: args.Participants,
			Location:     args.Location,
			Tags:         args.Tags,
			Source:       args.Source,
			Profile:      args.Profile,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeStorePreferenceHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args storePreferenceArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		result, err := native.StorePreference(ctx, cfg, native.StorePreferenceOptions{
			Preference:   args.Preference,
			Subject:      args.Subject,
			Alternatives: args.Alternatives,
			Strength:     args.Strength,
			Tags:         args.Tags,
			Source:       args.Source,
			Profile:      args.Profile,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
	}
}

func BuildNativeSwitchProfileHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args switchProfileArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.Profile == "" {
			return errorResult("switch_profile: profile is required"), nil
		}
		old := native.ActiveProfile(cfg)
		if err := native.SwitchProfile(args.Profile); err != nil {
			return errorResult(err.Error()), nil
		}
		// Mutate cfg.Profile in-process so the very next tool call in
		// this MCP session sees the new profile without re-reading the
		// sentinel. External processes (CLI invocations) will read the
		// sentinel on their next Load(), which keeps the two surfaces
		// in lockstep.
		cfg.Profile = args.Profile
		return jsonResult(map[string]any{
			"status":  "switched",
			"old":     old,
			"new":     args.Profile,
			"profile": args.Profile,
		})
	}
}

func BuildNativeCurrentProfileHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args currentProfileArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		return jsonResult(map[string]any{
			"profile": native.ActiveProfile(cfg),
		})
	}
}

func BuildNativeUpdateHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args updateMemoryArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.ID == "" {
			return errorResult("update_memory: memory_id is required"), nil
		}
		if args.Content == nil && args.Tags == nil && args.Metadata == nil {
			// Python raises ValueError("No updates provided") -- map to
			// IsError so MCP clients see it as a tool-level rejection, not
			// transport failure.
			return errorResult("update_memory: no fields to update (pass content, tags, or metadata)"), nil
		}
		result, err := native.Update(ctx, cfg, args.ID, native.UpdateOptions{
			Content:  args.Content,
			Tags:     args.Tags,
			Metadata: args.Metadata,
			Profile:  args.Profile,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"status":          "updated",
			"id":              result.ID,
			"profile":         result.Profile,
			"updated_at":      result.UpdatedAt,
			"fields_updated":  result.FieldsUpdated,
			"re_embedded":     result.ReEmbedded,
		})
	}
}

func BuildNativeContradictHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args contradictMemoryArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.ID == "" {
			return errorResult("contradict_memory: memory_id is required"), nil
		}
		strength := args.Strength
		if strength == 0 {
			strength = contradictDefaultStrength
		}
		// Python: 0 <= strength < 1.0. We additionally refuse negative
		// values since Python does too (the range check fails).
		if strength < 0 || strength >= 1.0 {
			return errorResult(fmt.Sprintf("contradict_memory: strength must be in [0.0, 1.0); got %v", strength)), nil
		}
		result, err := native.UpdateConfidence(ctx, cfg, args.ID, strength, "")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"status":     "contradicted",
			"id":         result.ID,
			"profile":    result.Profile,
			"confidence": result.Confidence,
		})
	}
}

func BuildNativeSetProfileTTLHandler(cfg *native.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args setProfileTTLArgs
		if fail := unmarshalArgs(req, &args); fail != nil {
			return fail, nil
		}
		if args.Profile == "" {
			return errorResult("set_profile_ttl: profile is required"), nil
		}
		ttl := args.TTLDays
		// clear=true wins: callers that toggle the flag expect it to
		// override any stale ttl_days value in the same request.
		if args.Clear {
			ttl = -1
		}
		result, err := native.SetProfileTTL(ctx, cfg, args.Profile, ttl)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return jsonResult(result)
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
		{
			"delete_memory",
			"Delete a single memory by id from the active profile. Refuses to delete across profiles -- pass the profile override explicitly if you need to.",
			deleteMemoryArgs{},
			BuildNativeDeleteHandler(cfg),
		},
		{
			"cleanup_expired",
			"Sweep the active profile and hard-delete every memory whose TTL has elapsed. Returns the count seen + the count actually deleted (they differ if something else cleaned up concurrently).",
			cleanupExpiredArgs{},
			BuildNativeCleanupHandler(cfg),
		},
		{
			"list_profiles",
			"List every profile that currently holds at least one non-expired memory, with counts.",
			listProfilesArgs{},
			BuildNativeListProfilesHandler(cfg),
		},
		{
			"set_profile_ttl",
			"Set or clear the TTL (in days) that applies to new memories stored in a profile. Pass clear=true (or ttl_days=-1) to remove the TTL entirely.",
			setProfileTTLArgs{},
			BuildNativeSetProfileTTLHandler(cfg),
		},
		{
			"reinforce_memory",
			"Reinforce a memory's confidence -- mark it as verified or confirmed. Increases the memory's confidence score, making it rank higher in future searches. strength must be in (0.0, 1.0]; default 0.85.",
			reinforceMemoryArgs{},
			BuildNativeReinforceHandler(cfg),
		},
		{
			"contradict_memory",
			"Contradict a memory's confidence -- mark it as disputed or outdated. Decreases the memory's confidence score, making it rank lower in future searches. The memory isn't deleted, just deprioritised. strength must be in [0.0, 1.0); default 0.15.",
			contradictMemoryArgs{},
			BuildNativeContradictHandler(cfg),
		},
		{
			"update_memory",
			"Update an existing memory. Re-embeds when content changes; omitting a field leaves it untouched, passing an empty array/object clears it. Returns id, updated_at, and the list of fields that were actually written.",
			updateMemoryArgs{},
			BuildNativeUpdateHandler(cfg),
		},
		{
			"switch_profile",
			"Switch the active profile for subsequent tool calls. Persists to ~/.ogham/active_profile so later CLI invocations and MCP sessions see the switch. config.toml's default_profile is the baseline and is never modified.",
			switchProfileArgs{},
			BuildNativeSwitchProfileHandler(cfg),
		},
		{
			"current_profile",
			"Return the currently active profile, resolving OGHAM_PROFILE env > ~/.ogham/active_profile > config.toml default_profile > \"default\".",
			currentProfileArgs{},
			BuildNativeCurrentProfileHandler(cfg),
		},
		{
			"store_decision",
			"Store an architectural decision with rationale. Creates a memory with type:decision tag and structured metadata (alternatives, reasoning_trace, decided_at). related_memories linking is pending Batch E.",
			storeDecisionArgs{},
			BuildNativeStoreDecisionHandler(cfg),
		},
		{
			"store_fact",
			"Store a factual statement with confidence and optional citation. Creates a memory with type:fact tag; metadata carries subject, confidence, source_citation, recorded_at.",
			storeFactArgs{},
			BuildNativeStoreFactHandler(cfg),
		},
		{
			"store_event",
			"Store an event with temporal + participant metadata. Creates a memory with type:event tag so temporal queries match by when, social queries by participants, location queries by location.",
			storeEventArgs{},
			BuildNativeStoreEventHandler(cfg),
		},
		{
			"store_preference",
			"Store a user preference with structured metadata. type:preference tag. strength distinguishes 'always' (strong) from 'usually' (normal) from 'sometimes' (weak).",
			storePreferenceArgs{},
			BuildNativeStorePreferenceHandler(cfg),
		},
		{
			"get_config",
			"Return the resolved Ogham configuration with URL passwords and API keys redacted. Useful for verifying which backend + embedder the server is actually using.",
			getConfigArgs{},
			BuildNativeGetConfigHandler(cfg),
		},
		{
			"get_stats",
			"Aggregate counts for the active profile: total memories, top sources, top tags, untagged count, with-TTL count, expiring-within-7-days count.",
			getStatsArgs{},
			BuildNativeGetStatsHandler(cfg),
		},
		{
			"get_cache_stats",
			"Snapshot of the shared embedding cache at $HOME/.cache/ogham/embeddings.db: row count, size bytes, hit/miss counters since process start.",
			getCacheStatsArgs{},
			BuildNativeGetCacheStatsHandler(cfg),
		},
		{
			"link_unlinked",
			"Backfill auto-links for memories that don't have any yet. Call repeatedly after a bulk import until processed returns 0.",
			linkUnlinkedArgs{},
			BuildNativeLinkUnlinkedHandler(cfg),
		},
		{
			"explore_knowledge",
			"Hybrid search for the query, then walk the relationship graph N hops deep. Returns seed matches (depth=0) plus connected memories (depth=1+) with edge type + strength.",
			exploreKnowledgeArgs{},
			BuildNativeExploreKnowledgeHandler(cfg),
		},
		{
			"find_related",
			"Traverse the relationship graph from a known memory. Returns reachable memories with depth + edge strength. Impact-analysis tool.",
			findRelatedArgs{},
			BuildNativeFindRelatedHandler(cfg),
		},
		{
			"suggest_connections",
			"Suggest memories sharing entities with the target but no explicit relationship. Surfaces hidden connections through the entity graph.",
			suggestConnectionsArgs{},
			BuildNativeSuggestConnectionsHandler(cfg),
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
