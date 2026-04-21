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
	"os"
	"os/exec"
	"strings"
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
// subprocess. One Client = one subprocess lifecycle.
type Client struct {
	impl    *mcp.Implementation
	cmd     *exec.Cmd
	session *mcp.ClientSession
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

	cmdArgs := resolveCommand(opts.Command, opts.Args)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...) //nolint:gosec // user-controlled sidecar command is intentional
	cmd.Env = append(os.Environ(), opts.Env...)
	// stderr is inherited -- the Python sidecar's logs surface to the terminal,
	// which is helpful when diagnosing why a tool call failed.
	cmd.Stderr = os.Stderr

	return &Client{impl: impl, cmd: cmd}
}

// Connect spawns the subprocess and runs the MCP initialize handshake.
func (c *Client) Connect(ctx context.Context) error {
	if c.session != nil {
		return errors.New("sidecar: already connected")
	}
	transport := &mcp.CommandTransport{Command: c.cmd}
	client := mcp.NewClient(c.impl, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("sidecar connect: %w", err)
	}
	c.session = session
	return nil
}

// CallTool invokes an MCP tool on the sidecar. Returns the raw CallToolResult;
// callers unpack Content / StructuredContent per tool contract.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if c.session == nil {
		return nil, errors.New("sidecar: not connected (call Connect first)")
	}
	return c.session.CallTool(ctx, &mcp.CallToolParams{
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
	if c.session == nil {
		return nil, errors.New("sidecar: not connected (call Connect first)")
	}
	result, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sidecar list tools: %w", err)
	}
	return result.Tools, nil
}

// Close tears down the MCP session and waits for the subprocess to exit.
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
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
