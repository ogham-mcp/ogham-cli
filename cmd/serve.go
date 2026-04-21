package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	mcpserver "github.com/ogham-mcp/ogham-cli/internal/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	debugFlag       bool
	serveGatewayOpt bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server over stdio (native tools by default)",
	Long: `Expose Ogham tools to an MCP client (Claude Code, Cursor, Windsurf, Codex,
Claude Desktop) over stdio.

Two modes:

  - default: native Go tools (store_memory, hybrid_search, list_recent,
    health_check). Reads ~/.ogham/config.toml for backend + embedder
    credentials. No gateway API key required. Set up with 'ogham init'.

  - --gateway: forward tool calls to the managed gateway at
    ~/.ogham/config.toml's gateway_url. Requires an API key obtained
    via 'ogham auth login' -- gated until the managed service returns.

Tool names match the Python ogham-mcp sidecar on purpose: an MCP
client already wired for the Python server swaps to the Go binary
without reconfiguring its tool calls. Tools the Go native layer
hasn't absorbed yet (compression, graph, full stats) surface as
"tool not found" -- route through the sidecar for those until v0.6.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if debugFlag {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		server := mcp.NewServer(
			&mcp.Implementation{Name: "ogham", Version: Version},
			nil,
		)

		if serveGatewayOpt {
			return runGatewayServe(ctx, server)
		}
		return runNativeServe(ctx, server)
	},
}

// runNativeServe wires the four v0.5 native tools onto the MCP server
// and runs it on stdio. Reads ~/.ogham/config.toml (written by
// 'ogham init') for backend + embedder credentials -- no gateway API
// key needed.
func runNativeServe(ctx context.Context, server *mcp.Server) error {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return fmt.Errorf("load native config: %w", err)
	}
	// Minimal sanity: the native tools need at least a backend to do
	// anything useful. A fresh install with no config should tell the
	// user rather than start an MCP server that fails every call.
	if cfg.Database.URL == "" && cfg.Database.SupabaseURL == "" {
		return fmt.Errorf(
			"no database configured. Run 'ogham init' to set up Postgres or Supabase, " +
				"or use --gateway to forward to the managed gateway")
	}

	names := mcpserver.RegisterNativeTools(server, cfg)
	slog.Info("registered native MCP tools", "count", len(names), "tools", names)

	slog.Info("MCP server starting on stdio (native mode)",
		"provider", cfg.Embedding.Provider,
		"backend", backendLabel(cfg))
	return server.Run(ctx, &mcp.StdioTransport{})
}

// runGatewayServe preserves the pre-v0.5 gateway forwarding mode. Kept
// for operators running against the managed api.ogham-mcp.dev service.
func runGatewayServe(ctx context.Context, server *mcp.Server) error {
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load gateway config: %w", err)
	}
	if cfg.APIKey == "" {
		return fmt.Errorf(
			"no gateway API key configured. Run 'ogham auth login' " +
				"or drop --gateway to use the native tools instead")
	}

	for _, w := range config.CheckPermissions(path) {
		slog.Warn(w)
	}

	ua := fmt.Sprintf("ogham-cli/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
	client := gateway.New(cfg.GatewayURL, cfg.APIKey, ua)

	slog.Info("fetching tool manifest", "gateway", cfg.GatewayURL)
	hash, err := mcpserver.RegisterTools(ctx, server, client)
	if err != nil {
		return fmt.Errorf("register tools: %w", err)
	}
	slog.Info("tools registered", "manifest_hash", hash[:16])

	slog.Info("MCP server starting on stdio (gateway mode)")
	return server.Run(ctx, &mcp.StdioTransport{})
}

func backendLabel(cfg *native.Config) string {
	if cfg.Database.SupabaseURL != "" {
		return "supabase"
	}
	return "postgres"
}

func init() {
	serveCmd.Flags().BoolVar(&debugFlag, "debug", false, "Enable debug logging (JSON-RPC traffic)")
	serveCmd.Flags().BoolVar(&serveGatewayOpt, "gateway", false,
		"Forward tool calls to the managed gateway instead of serving native tools")
	rootCmd.AddCommand(serveCmd)
}
