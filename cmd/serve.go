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
	"github.com/spf13/cobra"
)

var debugFlag bool

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server over stdio",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Root's PersistentPreRunE has already installed a shared slog
		// handler driven by -v / --quiet. --debug on this command is a
		// legacy opt-in that forces debug level regardless.
		if debugFlag {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
		}

		path := config.DefaultPath()
		cfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.APIKey == "" {
			return fmt.Errorf("no API key configured. Run: ogham init")
		}

		for _, w := range config.CheckPermissions(path) {
			slog.Warn(w)
		}

		ua := fmt.Sprintf("ogham-cli/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
		client := gateway.New(cfg.GatewayURL, cfg.APIKey, ua)

		server := mcp.NewServer(
			&mcp.Implementation{Name: "ogham", Version: Version},
			nil,
		)

		slog.Info("fetching tool manifest", "gateway", cfg.GatewayURL)
		hash, err := mcpserver.RegisterTools(server, client)
		if err != nil {
			return fmt.Errorf("register tools: %w", err)
		}
		slog.Info("tools registered", "manifest_hash", hash[:16])

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		slog.Info("MCP server starting on stdio")
		return server.Run(ctx, &mcp.StdioTransport{})
	},
}

func init() {
	serveCmd.Flags().BoolVar(&debugFlag, "debug", false, "Enable debug logging (JSON-RPC traffic)")
	rootCmd.AddCommand(serveCmd)
}
