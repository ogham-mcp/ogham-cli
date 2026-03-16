# ogham-cli

A lightweight Go binary that connects your AI agents to [Ogham Cloud](https://ogham-mcp.dev) -- persistent shared memory without the self-hosting.

Speaks MCP over stdio to your AI client (Claude Code, Cursor, Windsurf), translates tool calls to HTTPS requests against the Ogham gateway. No Python, no database, no embeddings locally. Just a single binary and an API key.

## Install

```bash
# From source
go install github.com/ogham-mcp/ogham-cli@latest

# Or build locally
git clone https://github.com/ogham-mcp/ogham-cli.git
cd ogham-cli
make build
```

## Quick start

```bash
# Authenticate
ogham auth login --api-key YOUR_API_KEY

# Verify
ogham health

# Add to Claude Code
claude mcp add ogham -- ogham

# Or run the full setup (auth + auto-register)
ogham init --api-key YOUR_API_KEY
```

## Commands

| Command | What it does |
|---------|-------------|
| `ogham` | Start MCP server (alias for `ogham serve`) |
| `ogham init` | Authenticate and configure MCP clients |
| `ogham auth login` | Save API key (`--api-key` flag or interactive) |
| `ogham auth status` | Show login state and gateway connectivity |
| `ogham auth logout` | Remove stored credentials |
| `ogham auth token` | Print API key to stdout (for scripting) |
| `ogham serve` | Start MCP server over stdio |
| `ogham health` | Check gateway connectivity |
| `ogham version` | Print version, OS, and architecture |

## Configuration

Config file: `~/.ogham/config.toml` (created by `ogham init`, permissions `0600`)

```toml
api_key = "ogham_live_..."
gateway_url = "https://api.ogham-mcp.dev"
```

Environment variables override the config file:

- `OGHAM_API_KEY` -- API key
- `OGHAM_GATEWAY_URL` -- gateway URL (defaults to `https://api.ogham-mcp.dev`)

## MCP client config

**Claude Code:**
```bash
claude mcp add ogham -- ogham
```

**Cursor / Windsurf** (`.cursor/mcp.json`):
```json
{"mcpServers": {"ogham": {"command": "ogham"}}}
```

## Development

### Prerequisites

- Go 1.26+
- The `ogham` binary must be on your PATH after building

### Common tasks

```bash
make build          # Build binary (stripped, ~8MB)
make test           # Run all tests
make lint           # go vet + gofmt check
make check          # lint + test
make clean          # Remove build artifacts
make cross-compile  # Build for all platforms (darwin/linux/windows, amd64/arm64)
```

### Build with version

```bash
make build VERSION=0.1.0
./ogham version
# ogham-cli/0.1.0 (darwin; arm64)
```

### Cross-compile

Produces stripped binaries in `dist/`:

```bash
make cross-compile VERSION=0.1.0
ls -lh dist/
# ogham-darwin-arm64      ~8MB
# ogham-darwin-amd64      ~8MB
# ogham-linux-amd64       ~8MB
# ogham-linux-arm64       ~8MB
# ogham-windows-amd64.exe ~8MB
```

### Project structure

```
ogham-cli/
├── main.go                         # Entry point
├── cmd/
│   ├── root.go                     # Cobra root, default = serve
│   ├── version.go                  # ogham version
│   ├── health.go                   # ogham health
│   ├── auth.go                     # ogham auth login/status/logout/token
│   ├── init.go                     # ogham init (auth + client setup)
│   └── serve.go                    # MCP server over stdio
├── internal/
│   ├── config/config.go            # Config file + env var loading
│   ├── gateway/client.go           # HTTP client for gateway REST API
│   └── mcp/server.go              # MCP tool registration + forwarding
├── Makefile
├── .goreleaser.yml                 # GitHub releases (future)
└── .pre-commit-config.yaml
```

### Running tests

```bash
go test ./... -v
```

Tests cover:
- Config loading (file, env overrides, defaults, permissions)
- Gateway HTTP client (health, tools, call -- uses httptest mock server)
- MCP server (tool handler, manifest hashing)

### Pre-commit hooks

Install pre-commit hooks:

```bash
pre-commit install
```

Hooks run: `go-fmt`, `go-vet`, `go-build`, trailing whitespace, large file blocker, private key detection.

### Release process

GoReleaser is configured but not yet automated via GitHub Actions. Manual release:

```bash
# Tag
git tag -a v0.1.0 -m "v0.1.0: initial release"
git push origin v0.1.0

# Build release (requires goreleaser installed)
goreleaser release --clean
```

## Architecture

```
AI Client (Claude Code, Cursor, etc.)
    │ stdio (MCP protocol)
    │
ogham binary (~8MB)
    │ HTTPS + X-Api-Key
    │
api.ogham-mcp.dev (Ogham gateway)
    │
Neon Postgres + pgvector
```

The Go binary is a pass-through MCP server. On startup it fetches the tool manifest from the gateway, registers 10 tools, and forwards every call as a REST request. It has no business logic, no database, no embeddings.

## License

MIT
