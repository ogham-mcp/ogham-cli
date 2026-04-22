package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/dashboard"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	dashboardPort   int
	dashboardHost   string
	dashboardNoOpen bool
)

// allowedDashboardHosts is the short-list of bind addresses the prototype
// accepts. Loopback-only is the security boundary -- the dashboard ships
// no auth, so exposing it beyond localhost is explicitly unsupported
// until v0.9 when TLS + remote-access-auth lands.
var allowedDashboardHosts = map[string]struct{}{
	"127.0.0.1": {},
	"localhost": {},
	"::1":       {},
}

// dashboardCmd launches the native Go dashboard server. Reads config via
// internal/native (same Config path as `ogham serve --native`) and serves
// Templ-rendered Overview + Search views over plain HTTP on a loopback
// address. See docs/plans/2026-04-22-go-dashboard-action-plan.md for the
// design rationale (loopback-only, no auth, shadcn port).
var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Launch the Ogham dashboard (native Go, loopback only)",
	Long: `Starts a local HTTP server on 127.0.0.1 that renders the Ogham
dashboard. No authentication: loopback is the security boundary. Remote
access ships in v0.9 alongside TLS.

Flags:
  --port      TCP port; 0 = random (default)
  --host      Bind address; must resolve to loopback (127.0.0.1, localhost, ::1)
  --no-open   Don't auto-open the browser

Data source: internal/native (direct Postgres / Supabase). Same env vars
as the native CLI path -- DATABASE_URL, EMBEDDING_PROVIDER, OGHAM_PROFILE.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		host := strings.TrimSpace(dashboardHost)
		if _, ok := allowedDashboardHosts[host]; !ok {
			return fmt.Errorf("dashboard: host %q is not loopback; remote access ships in v0.9. Allowed: 127.0.0.1, localhost, ::1", host)
		}

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return fmt.Errorf("dashboard: load config: %w", err)
		}

		server, boundAddr, err := dashboard.New(cfg, host, dashboardPort)
		if err != nil {
			return fmt.Errorf("dashboard: %w", err)
		}

		url := fmt.Sprintf("http://%s/", boundAddr)
		fmt.Fprintf(os.Stderr, "[ogham dashboard] serving on %s (profile=%s)\n", url, cfg.Profile)

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			if err := server.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}()

		if !dashboardNoOpen {
			if err := openBrowser(url); err != nil {
				slog.Warn("dashboard: could not auto-open browser", "error", err)
			}
		}

		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("dashboard: shutdown: %w", err)
			}
			return nil
		case err := <-errCh:
			return err
		}
	},
}

// openBrowser fires a platform-appropriate URL-opener command. Best-effort:
// any failure (e.g. no display on a headless box) is logged as a warning
// but doesn't fail the command -- the user can still paste the URL by hand.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// listenerFor is exported for tests; cmd-layer code uses dashboard.New.
// Kept here to guarantee we catch misconfiguration (non-loopback host) at
// the command boundary before allocating package resources.
func listenerFor(host string, port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
}

func init() {
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 0, "TCP port (0 = random)")
	dashboardCmd.Flags().StringVar(&dashboardHost, "host", "127.0.0.1", "Bind address (loopback only)")
	dashboardCmd.Flags().BoolVar(&dashboardNoOpen, "no-open", false, "Don't auto-open the browser")
	rootCmd.AddCommand(dashboardCmd)
}
