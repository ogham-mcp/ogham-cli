package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// CallbackResult holds the API key received from the browser auth flow.
type CallbackResult struct {
	APIKey string
	Error  error
}

// StartCallbackServer starts a local HTTP server that waits for the
// dashboard to redirect back with an API key.
func StartCallbackServer(ctx context.Context) (port int, result <-chan CallbackResult, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, fmt.Errorf("start callback server: %w", err)
	}
	port = listener.Addr().(*net.TCPAddr).Port

	ch := make(chan CallbackResult, 1)
	done := make(chan struct{}) // signal for shutdown, separate from result
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		errMsg := r.URL.Query().Get("error")

		if errMsg != "" {
			ch <- CallbackResult{Error: fmt.Errorf("auth failed: %s", errMsg)}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, authFailHTML)
			close(done)
			return
		}

		if key == "" {
			ch <- CallbackResult{Error: fmt.Errorf("no API key received")}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, authFailHTML)
			close(done)
			return
		}

		ch <- CallbackResult{APIKey: key}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, authSuccessHTML)
		close(done)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("callback server listening", "port", port)
		if err := server.Serve(listener); err != http.ErrServerClosed {
			slog.Error("callback server error", "error", err)
		}
	}()

	// Auto-shutdown after result received or timeout
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			// Give browser time to render the success page
			time.Sleep(1 * time.Second)
		}
		if err := server.Shutdown(context.Background()); err != nil {
			slog.Error("callback server shutdown error", "error", err)
		}
	}()

	return port, ch, nil
}

const authSuccessHTML = `<!DOCTYPE html>
<html>
<head><title>Ogham CLI</title></head>
<body style="background:#000;color:#fff;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center">
<h2>Logged in successfully</h2>
<p style="color:#888">You can close this tab and return to the terminal.</p>
</div>
</body>
</html>`

const authFailHTML = `<!DOCTYPE html>
<html>
<head><title>Ogham CLI</title></head>
<body style="background:#000;color:#fff;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center">
<h2>Authentication failed</h2>
<p style="color:#888">Please try again from the terminal.</p>
</div>
</body>
</html>`
