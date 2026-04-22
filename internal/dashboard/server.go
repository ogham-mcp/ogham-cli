package dashboard

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// Server wraps the net/http server bound to a loopback listener. The
// listener is created before ListenAndServe so cmd/dashboard.go can
// print the actual bound address (port 0 => random) before the browser
// auto-opens.
type Server struct {
	cfg        *native.Config
	httpServer *http.Server
	listener   net.Listener
}

// New constructs a Server, binds the listener, and wires the router.
// Returns the bound address so the CLI can log / open-browser it.
func New(cfg *native.Config, host string, port int) (*Server, string, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listen %s: %w", addr, err)
	}

	// Resolve the sub-filesystem for static assets so URLs look like
	// /static/styles.css rather than /static/static/styles.css.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		_ = l.Close()
		return nil, "", fmt.Errorf("embed static: %w", err)
	}

	h := &handlers{cfg: cfg}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", h.overview)
	mux.HandleFunc("/filter", h.filter)
	mux.HandleFunc("/search", h.search)
	mux.HandleFunc("/healthz", h.healthz)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Dashboard pages can stream reasonably large HTML (20-row table
		// + search results); 30s write timeout covers the slow-network
		// case without stalling forever on a broken pipe.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &Server{
		cfg:        cfg,
		httpServer: srv,
		listener:   l,
	}, l.Addr().String(), nil
}

// Serve blocks until the listener is closed or the server shuts down.
// Any error other than http.ErrServerClosed is returned to the caller.
func (s *Server) Serve() error {
	return s.httpServer.Serve(s.listener)
}

// Shutdown initiates a graceful shutdown. The caller is responsible for
// bounding the context so a stuck client can't block indefinitely.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the bound address. Useful for tests that need to talk
// to the dashboard after constructing it directly.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}
