package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sidecarCaller is the subset of *sidecar.Client the proxy path touches.
// Declaring it here (instead of importing the concrete type) lets tests
// mock the sidecar with a tiny fake. Matches Client.CallTool exactly.
type sidecarCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error)
}

// BuildSidecarProxyHandler wraps a single Python-only tool so MCP calls
// from a client hit the sidecar subprocess. Argument bytes from the
// incoming request get unmarshalled into a map[string]any and passed
// through verbatim -- we trust the sidecar's schema validation to reject
// bad payloads rather than re-implementing it on the Go side.
//
// Error policy:
//   - Unmarshal failure -> CallToolResult{IsError: true}, tool-level error.
//     The MCP protocol distinguishes tool errors (surface to the user as
//     "tool said no") from transport errors (something went wrong moving
//     the bytes); the former is what a bad JSON payload should look like.
//   - Sidecar CallTool returning a Go error -> also CallToolResult{IsError},
//     with the error message in the content. A transport-level error here
//     usually means the Python subprocess died; surfacing it as a tool
//     error lets the client retry the call without a full session tear-
//     down.
//   - Sidecar returning a CallToolResult with IsError=true -> forward it
//     verbatim so Python's structured error content reaches the client
//     unchanged.
//
// Observability: every call logs name + duration at info level.
func BuildSidecarProxyHandler(toolName string, client sidecarCaller) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult(fmt.Sprintf("proxy %s: parse arguments: %v", toolName, err)), nil
			}
		}

		start := time.Now()
		result, err := client.CallTool(ctx, toolName, args)
		dur := time.Since(start)

		if err != nil {
			slog.Warn("proxy_tool_call",
				"name", toolName,
				"duration_ms", dur.Milliseconds(),
				"error", err.Error(),
			)
			return errorResult(fmt.Sprintf("sidecar %s unavailable: %v", toolName, err)), nil
		}

		slog.Info("proxy_tool_call",
			"name", toolName,
			"duration_ms", dur.Milliseconds(),
			"error", result != nil && result.IsError,
		)
		return result, nil
	}
}

// RegisterProxiedTools fetches the sidecar's tool manifest and installs a
// proxy handler for every tool whose name isn't already registered
// natively. nativeNames is the set returned by RegisterNativeTools so
// the router can skip collisions -- native handlers always win.
//
// Returns the names actually proxied (for startup logging) and any error
// encountered while listing the sidecar's tools. A nil error with an
// empty slice means the sidecar reported no tools.
func RegisterProxiedTools(
	ctx context.Context,
	server *mcp.Server,
	lister sidecarLister,
	caller sidecarCaller,
	nativeNames map[string]struct{},
) ([]string, error) {
	tools, err := lister.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("proxy tools: %w", err)
	}

	proxied := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, ok := nativeNames[t.Name]; ok {
			// Native handler already registered; skip to avoid the
			// "two handlers for one tool" confusion and because native
			// is lower-latency + doesn't need the subprocess alive.
			continue
		}
		// Copy minimal fields. The sidecar's schema has already been
		// negotiated with the Python tool; we re-expose it verbatim.
		server.AddTool(&mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}, BuildSidecarProxyHandler(t.Name, caller))
		proxied = append(proxied, t.Name)
	}
	return proxied, nil
}

// sidecarLister is the second subset the sidecar exposes to the proxy
// path. Split from sidecarCaller so handler-only paths don't need to
// mock ListTools.
type sidecarLister interface {
	ListTools(ctx context.Context) ([]*mcp.Tool, error)
}
