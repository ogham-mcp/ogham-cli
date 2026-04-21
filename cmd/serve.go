package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	mcpserver "github.com/ogham-mcp/ogham-cli/internal/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/sidecar"
	"github.com/spf13/cobra"
)

var (
	debugFlag         bool
	serveGatewayOpt   bool
	serveNoSidecarOpt bool
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

// runNativeServe wires the v0.5 native tools onto the MCP server, then
// (unless --no-sidecar is set) spawns the Python ogham-mcp sidecar and
// proxies every tool the sidecar exposes that isn't already native.
// Native handlers always win on name collision.
//
// Eager spawn at startup (not lazy) because MCP clients cache the
// tool manifest after the initialize handshake; tools that don't exist
// at manifest-time never surface to the client.
//
// Graceful degradation: if the sidecar fails to spawn (no uv, no Python,
// wheel fetch timeout), we log a warning and keep serving the native
// subset. The Go binary must work standalone for users who only need
// the four native tools.
func runNativeServe(ctx context.Context, server *mcp.Server) error {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return fmt.Errorf("load native config: %w", err)
	}
	if cfg.Database.URL == "" && cfg.Database.SupabaseURL == "" {
		return fmt.Errorf(
			"no database configured. Run 'ogham init' to set up Postgres or Supabase, " +
				"or use --gateway to forward to the managed gateway")
	}

	nativeNames := mcpserver.RegisterNativeTools(server, cfg)
	slog.Info("registered native MCP tools",
		"count", len(nativeNames),
		"tools", toolNamesFor(nativeNames))

	var sidecarClient *sidecar.Client
	if !serveNoSidecarOpt {
		sidecarClient = tryConnectSidecar(ctx, cfg)
		if sidecarClient != nil {
			defer func() { _ = sidecarClient.Close() }()
			proxied, perr := mcpserver.RegisterProxiedTools(ctx, server, sidecarClient, sidecarClient, nativeNames)
			if perr != nil {
				// Manifest fetch failed even though Connect succeeded.
				// Keep serving native; surface the warning.
				slog.Warn("proxied tool registration failed; serving native-only",
					"err", perr)
			} else {
				slog.Info("registered proxied MCP tools",
					"count", len(proxied), "tools", proxied)
			}
		}
	}

	slog.Info("MCP server starting on stdio (native mode)",
		"provider", cfg.Embedding.Provider,
		"backend", backendLabel(cfg),
		"native_tools", len(nativeNames),
		"sidecar_connected", sidecarClient != nil)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// tryConnectSidecar attempts to spawn the Python sidecar + run the MCP
// initialize handshake with a bounded timeout. Returns nil on any
// failure; callers must tolerate sidecar absence (native-only mode).
//
// The 15s timeout accommodates first-run `uv tool run --from ogham-mcp`
// which may fetch the wheel + deps from PyPI. Subsequent runs with a
// warm uv cache complete in ~1-2s.
func tryConnectSidecar(ctx context.Context, cfg *native.Config) *sidecar.Client {
	spawnCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Forward the native config into the sidecar's env so both stacks
	// see the same provider / dim / backend. Mirrors what the sidecar
	// mode of the CLI does via connectSidecar.
	client := sidecar.New(sidecar.Options{Env: cfg.SidecarEnv()})
	if err := client.Connect(spawnCtx); err != nil {
		slog.Warn("sidecar not available; serving native-only tools",
			"err", err,
			"hint", "install `uv` + `ogham-mcp`, or set OGHAM_SIDECAR_CMD, or pass --no-sidecar")
		return nil
	}
	return client
}

// toolNamesFor turns the set of native tool names into a sorted slice
// for logging. Keeps the "tools" log attribute deterministic across
// restarts so grepping logs stays sane.
func toolNamesFor(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// Cheap sort -- names are short + small N.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
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
	serveCmd.Flags().BoolVar(&serveNoSidecarOpt, "no-sidecar", false,
		"Skip sidecar spawn -- serve only the native Go tools (useful in CI / air-gapped / to debug)")
	rootCmd.AddCommand(serveCmd)
}
