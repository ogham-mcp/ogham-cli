// Package sidecar spawns the Python Ogham MCP server as a child process and
// speaks MCP (JSON-RPC over stdio) to it via modelcontextprotocol/go-sdk.
//
// This is the default mode for the Go CLI: the Go binary is an MCP client,
// the Python MCP server is the local gateway. On enterprise machines where
// Claude Code's MCP registration is blocked, the sidecar subprocess is
// invisible to the policy -- only the Go binary is seen, via Bash.
package sidecar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultCmdBase is the `uv tool run` invocation with Python pinned to 3.13
// (ogham-mcp requires >= 3.13; uv's resolver otherwise picks whatever system
// Python is first on PATH, which is often 3.10 or 3.11). The `--from` spec
// is built at resolve-time from OGHAM_SIDECAR_EXTRAS so users can pick
// embedding backends without writing the whole command.
//
// Users who install ogham-mcp as a permanent uv tool can skip all of this
// and just set OGHAM_SIDECAR_CMD=ogham serve.
var defaultCmdBase = []string{"uv", "tool", "run", "--python", "3.13", "--from"}

// buildDefaultCmd composes the default sidecar command from the optional
// extras list. extras of "postgres,gemini" produces:
//
//	uv tool run --python 3.13 --from ogham-mcp[postgres,gemini] ogham serve
//
// Empty extras yield a bare `ogham-mcp` which covers ollama-default users.
func buildDefaultCmd(extras string) []string {
	fromSpec := "ogham-mcp"
	if extras = strings.TrimSpace(extras); extras != "" {
		fromSpec = "ogham-mcp[" + extras + "]"
	}
	out := make([]string, 0, len(defaultCmdBase)+3)
	out = append(out, defaultCmdBase...)
	out = append(out, fromSpec, "ogham", "serve")
	return out
}

// Client wraps an MCP client session whose transport is the Python sidecar
// subprocess. One Client's lifecycle can span multiple subprocesses if the
// reconnect supervisor resurrects the sidecar after a crash.
type Client struct {
	impl *mcp.Implementation
	opts Options // retained so reconnect can rebuild cmd with the same args + env

	mu      sync.Mutex
	cmd     *exec.Cmd
	session *mcp.ClientSession
	// closed signals the supervisor to stop; set on explicit Close().
	closed bool
}

// Options configures how the sidecar is launched.
type Options struct {
	// Command and Args override the subprocess invocation. If Command is
	// empty, the value of OGHAM_SIDECAR_CMD (space-separated) is used; if
	// that is empty, defaultCmd is used.
	Command string
	Args    []string

	// Env adds or overrides environment variables passed to the subprocess.
	// The parent process environment is always inherited first; Env entries
	// are appended (later wins under Go's exec semantics).
	Env []string

	// Impl identifies this client in the MCP initialize handshake. If nil,
	// a default "ogham-cli" Implementation is used.
	Impl *mcp.Implementation

	// TerminateDuration is how long Close waits for the subprocess to exit
	// after closing stdin before escalating to SIGTERM. Zero means use the
	// transport default (5s).
	TerminateDuration time.Duration
}

// New builds an unconnected Client. Call Connect to spawn the subprocess and
// complete the MCP initialize handshake.
func New(opts Options) *Client {
	impl := opts.Impl
	if impl == nil {
		impl = &mcp.Implementation{Name: "ogham-cli", Version: "dev"}
	}
	return &Client{
		impl: impl,
		opts: opts,
		cmd:  buildCmd(opts),
	}
}

// buildCmd assembles a fresh *exec.Cmd from Options. Extracted so reconnect
// can rebuild after the previous subprocess has Wait()-ed -- exec.Cmd can't
// be reused after Start/Wait.
func buildCmd(opts Options) *exec.Cmd {
	cmdArgs := resolveCommand(opts.Command, opts.Args)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...) //nolint:gosec // user-controlled sidecar command is intentional
	cmd.Env = append(os.Environ(), opts.Env...)
	// stderr is inherited -- the Python sidecar's logs surface to the terminal,
	// which is helpful when diagnosing why a tool call failed.
	cmd.Stderr = os.Stderr
	return cmd
}

// Connect spawns the subprocess and runs the MCP initialize handshake. On
// success, starts a supervisor goroutine that watches for subprocess death
// and kicks off one reconnect attempt with a 1s backoff.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.session != nil {
		c.mu.Unlock()
		return errors.New("sidecar: already connected")
	}
	c.mu.Unlock()

	session, err := c.dial(ctx, c.cmd)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()

	go c.supervise(session)
	return nil
}

// dial runs the MCP initialize handshake against the given cmd. Extracted
// so Connect and reconnect share the transport wiring.
func (c *Client) dial(ctx context.Context, cmd *exec.Cmd) (*mcp.ClientSession, error) {
	transport := &mcp.CommandTransport{Command: cmd}
	client := mcp.NewClient(c.impl, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("sidecar connect: %w", err)
	}
	return session, nil
}

// supervise blocks on session.Wait(); when the Python subprocess exits, it
// clears the session and attempts one reconnect with a 1s backoff. On
// reconnect success, recurses to watch the replacement session. On failure,
// leaves the Client in a disconnected state (subsequent tool calls surface
// "sidecar unavailable" until the user restarts ogham serve).
//
// Exits silently if Close() has been called, which nils the session and
// sets c.closed to true.
func (c *Client) supervise(session *mcp.ClientSession) {
	_ = session.Wait()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	// Drop the dead session so in-flight CallTool sees "unavailable".
	c.session = nil
	c.mu.Unlock()

	slog.Warn("sidecar subprocess exited; attempting one reconnect", "backoff_ms", 1000)
	time.Sleep(1 * time.Second)

	reconnectCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	newCmd := buildCmd(c.opts)
	newSession, err := c.dial(reconnectCtx, newCmd)
	if err != nil {
		slog.Error("sidecar reconnect failed; proxy tools will return errors until restart",
			"err", err)
		return
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = newSession.Close()
		return
	}
	c.cmd = newCmd
	c.session = newSession
	c.mu.Unlock()

	slog.Info("sidecar reconnected")
	go c.supervise(newSession)
}

// CallTool invokes an MCP tool on the sidecar. Returns the raw CallToolResult;
// callers unpack Content / StructuredContent per tool contract.
//
// Thread-safe: concurrent callers race on the same session pointer, which
// is safe because ClientSession is documented as safe for concurrent use.
// The mutex only guards session swaps by the supervisor.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, errors.New("sidecar: not connected (subprocess may have died; reconnect pending)")
	}
	return session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}

// ListTools asks the sidecar for its tool manifest. The returned []*mcp.Tool
// preserves Name, Description, and InputSchema so callers can re-register
// the same tools on a different MCP server (the proxy use case).
//
// Used by `ogham serve` to build the hybrid native + proxied tool set:
// native Go handlers win on name collision, everything else gets
// forwarded to the sidecar via CallTool.
func (c *Client) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, errors.New("sidecar: not connected (call Connect first)")
	}
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sidecar list tools: %w", err)
	}
	return result.Tools, nil
}

// Close tears down the MCP session and waits for the subprocess to exit.
// Also tells the supervisor to stop -- if the session dies after Close()
// is called, no reconnect is attempted.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	session := c.session
	c.session = nil
	c.mu.Unlock()

	if session == nil {
		return nil
	}
	return session.Close()
}

// resolveCommand picks the argv for the subprocess using this precedence:
//  1. explicit opts.Command (+ opts.Args) -- caller knows best
//  2. OGHAM_SIDECAR_CMD env var, whitespace-split -- full override
//  3. buildDefaultCmd(OGHAM_SIDECAR_EXTRAS) -- ephemeral uv tool run with
//     the user's chosen extras (e.g. "postgres,gemini")
func resolveCommand(cmd string, args []string) []string {
	if cmd != "" {
		return append([]string{cmd}, args...)
	}
	if env := strings.TrimSpace(os.Getenv("OGHAM_SIDECAR_CMD")); env != "" {
		return strings.Fields(env)
	}
	return buildDefaultCmd(os.Getenv("OGHAM_SIDECAR_EXTRAS"))
}
