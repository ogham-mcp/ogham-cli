package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
)

// BuildToolHandler creates an MCP ToolHandler that forwards calls to the gateway.
func BuildToolHandler(client *gateway.Client, toolName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := make(map[string]any)
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{
						&mcp.TextContent{Text: fmt.Sprintf("Failed to parse arguments: %v", err)},
					},
				}, nil
			}
		}

		slog.Info("tool_call", "name", toolName)

		result, err := client.CallTool(toolName, args)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Gateway error: %v", err)},
				},
			}, nil
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(out)},
			},
		}, nil
	}
}

// RegisterTools fetches the tool manifest from the gateway and registers
// each tool with the MCP server.
func RegisterTools(server *mcp.Server, client *gateway.Client) (string, error) {
	tools, err := client.FetchTools()
	if err != nil {
		return "", fmt.Errorf("fetch tools: %w", err)
	}

	for _, toolDef := range tools {
		name, _ := toolDef["name"].(string)
		desc, _ := toolDef["description"].(string)
		if name == "" {
			continue
		}

		tool := &mcp.Tool{
			Name:        name,
			Description: desc,
		}

		if schema, ok := toolDef["inputSchema"]; ok {
			raw, _ := json.Marshal(schema)
			tool.InputSchema = json.RawMessage(raw)
		}

		server.AddTool(tool, BuildToolHandler(client, name))
		slog.Info("registered_tool", "name", name)
	}

	return ManifestHash(tools), nil
}

// ManifestHash computes a SHA-256 hash of the tool manifest for change detection.
func ManifestHash(tools []map[string]any) string {
	data, _ := json.Marshal(tools)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
