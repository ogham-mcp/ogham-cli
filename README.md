# ogham-cli

A single Go binary that gives AI agents persistent, searchable memory -- even on locked-down enterprise laptops where third-party MCP servers are blocked.

> **Pre-release.** v0.1-v0.3 are internal dogfood. **First public release is v0.4.0.** Expect breaking changes. Install paths below assume building from source.

## Who this is for

### 1. Self-hosters

You want persistent memory across AI clients (Claude Code, Cursor, Windsurf, Codex, Antigravity) and you want to run the whole stack yourself. No cloud, no SaaS vendor. The Go binary is your command-line entry point; behind it sits the Python Ogham MCP server (`ogham-mcp`) doing embeddings, hybrid search, entity extraction, the dashboard, and the knowledge graph.

### 2. Locked-down enterprise environments

Your employer's Claude Code deployment blocks third-party MCP servers -- only IT-approved ones show up. Installing `ogham-mcp` as an MCP registration silently fails. This pattern has become common across regulated industries (enterprise managed Claude Code, VPN-scoped policies, compliance-driven allowlists).

The Go binary bypasses the lockdown because it is *not* an MCP registration. It is a plain executable that Claude Code invokes via Bash. Enterprise policy does not block arbitrary CLI binaries. Inside, the Go binary spawns Python as a child process -- Claude Code never sees the MCP server, so the lockdown has nothing to block.

## Architecture

```
  ┌──────────────────────────────────────────────┐
  │  Claude Code / Cursor / Windsurf / Codex     │
  └───────────────┬──────────────────────────────┘
                  │  Bash call -- JSON by default
                  │  (see CLAUDE.md template below)
                  ▼
  ┌──────────────────────────────────────────────┐
  │  ogham (Go binary, ~8 MB, zero runtime deps) │
  │    cobra subcommands                         │
  │    MCP client (modelcontextprotocol/go-sdk)  │
  │    dotenv auto-loader (project .env etc.)    │
  └───────────────┬──────────────────────────────┘
                  │  stdio (MCP JSON-RPC)
                  ▼
  ┌──────────────────────────────────────────────┐
  │  ogham serve (Python, spawned as subprocess) │
  │    FastMCP 3.x, hybrid search, entity        │
  │    extraction (18 langs), compression,       │
  │    Prefab dashboard                          │
  └───────────────┬──────────────────────────────┘
                  │
                  ▼
      PostgreSQL + pgvector (Supabase / Neon / self-hosted)
```

Three build modes, one codebase:

| Mode | How invoked | Default? | Use case |
|---|---|---|---|
| **Sidecar** | `go build .` | yes | Talks MCP to the Python sidecar. All Python features available. |
| **Native** | default for most subcommands | Go talks to Postgres/Supabase/Gemini directly. ~10× faster than sidecar for read paths. |
| **Sidecar** | `--legacy` (or `--python`) | Routes through the Python MCP server. Use for tool-layer enrichment Python still owns (query reformulation, entity-overlap boost, Hebbian reinforcement on retrieval), or when a command has no native path yet (currently just `store`). |
| **Gateway** | `go build -tags gateway .` | no | HTTPS against managed `api.ogham-mcp.dev`. Hidden in default build. |

Tools will be absorbed into native Go over time (see Roadmap). The Python sidecar stays for the dashboard and features that would otherwise need a Node frontend we don't want to build.

## Install (pre-release -- build from source)

```bash
git clone https://github.com/ogham-mcp/ogham-cli.git
cd ogham-cli
go build -o /usr/local/bin/ogham .
```

Requires Go 1.26+. The binary is ~8 MB after `-s -w`.

For v0.4 public release the install will be:

```bash
brew install ogham-mcp/tap/ogham                       # macOS
curl -L https://github.com/ogham-mcp/ogham-cli/releases/latest/download/ogham-linux-amd64 -o ogham  # Linux
```

## Quick start

Prerequisites on the host:
- `uv` (Astral uv -- `curl -LsSf https://astral.sh/uv/install.sh | sh`)
- Python 3.13 available to `uv` (install with `uv python install 3.13` if missing)
- A Postgres database reachable from the host (Supabase, Neon, or self-hosted)

One-time config -- drop a `.env` in your working directory or `~/.ogham/config.env`:

```bash
# Database -- pick one backend
DATABASE_BACKEND=supabase
SUPABASE_URL=https://your-project.supabase.co
SUPABASE_KEY=sb_secret_...

# Or for vanilla Postgres / Neon
# DATABASE_BACKEND=postgres
# DATABASE_URL=postgresql://user:pass@host:5432/ogham

# Embedding provider
EMBEDDING_PROVIDER=gemini
GEMINI_API_KEY=...
EMBEDDING_DIM=512

# Tell the Go binary which Python extras to install into the sidecar
OGHAM_SIDECAR_EXTRAS=postgres,gemini

# Default memory profile
DEFAULT_PROFILE=work
```

Then:

```bash
ogham health              # JSON status report
ogham list --limit 5      # recent memories (JSON by default)
ogham search "query"      # hybrid vector + keyword search
ogham store "content" --tags type:decision,project:foo
```

**Output + backend are chosen for you.** JSON is the default (scripts and LLMs parse it cleanly). Native Go is the default backend (direct Postgres / Supabase / Gemini, ~10× faster than spinning up a Python process per call).

Add `--text` for human-readable output, `--legacy` (or `--python`) to route through the Python sidecar:

```bash
ogham list --text --limit 5              # numbered, readable
ogham search "query" --legacy --text     # Python tool-layer enrichment, human output
```

## Claude Code integration (the enterprise-lockdown unblock)

On machines where Claude Code blocks MCP registration, add this to your project's `CLAUDE.md`:

```markdown
## Ogham shared memory

This project uses Ogham for persistent shared memory across sessions.
Use Bash to invoke the `ogham` CLI directly -- do not attempt MCP registration.

Before starting work, retrieve context:
    ogham search "what you're about to work on"

Save decisions and learnings:
    ogham store "what you learned" --tags type:decision,project:$(basename $(pwd))

List recent work:
    ogham list --limit 20

All commands return JSON by default -- ideal for parsing in Bash pipelines.
Add --text if you ever need to read output with human eyes.
```

Claude Code will now call `ogham` via its Bash tool. Enterprise MCP filtering is bypassed entirely because nothing ever registers as an MCP server from Claude Code's perspective.

## Configuration

### Where configuration lives

1. **Project-local `.env`** (highest priority) -- override for a single repo
2. **`~/.ogham/config.env`** (global fallback) -- works from any cwd
3. **`~/.ogham/config.toml`** -- Go-native config; overrides both env files

The Go binary auto-loads all three and passes the resolved environment to the Python sidecar. Python does not need to know about TOML; the Go side translates.

### Common env vars

| Variable | Purpose |
|---|---|
| `DATABASE_BACKEND` | `supabase` or `postgres` |
| `SUPABASE_URL`, `SUPABASE_KEY` | Supabase backend credentials |
| `DATABASE_URL` | Postgres backend connection string |
| `EMBEDDING_PROVIDER` | `ollama` / `openai` / `voyage` / `gemini` / `mistral` |
| `GEMINI_API_KEY` / `OPENAI_API_KEY` / `VOYAGE_API_KEY` / `MISTRAL_API_KEY` | Provider-specific keys |
| `EMBEDDING_DIM` | Embedding dimension (default 512) |
| `DEFAULT_PROFILE` | Memory profile used when no `--profile` flag given |
| `OGHAM_SIDECAR_EXTRAS` | Comma-separated Python extras (e.g. `postgres,gemini`) |
| `OGHAM_SIDECAR_CMD` | Full override for how the Python sidecar is launched |

### Subprocess command resolution

Precedence, highest to lowest:

1. `OGHAM_SIDECAR_CMD` -- full command override (whitespace-split)
2. `OGHAM_SIDECAR_EXTRAS` -- appended to the ephemeral `uv tool run --from ogham-mcp[...]`
3. Default: `uv tool run --python 3.13 --from ogham-mcp ogham serve`

If you have ogham-mcp installed as a permanent uv tool with the right extras:

```bash
uv tool install --refresh "ogham-mcp[postgres,gemini]"
export OGHAM_SIDECAR_CMD="ogham serve"
```

Then every `ogham` command starts in milliseconds instead of waiting for the ephemeral install.

## Commands

Every command outputs JSON by default and runs natively where possible. Pass `--text` for human output, `--legacy` (or `--python`) to route through the Python sidecar.

| Command | Default path | Purpose |
|---|---|---|
| `ogham health` | native | Parallel errgroup probes (DB + embedder). Adds `--live-embedder` to burn a real provider token. |
| `ogham list [--limit N] [--profile P] [--source S] [--tags a,b]` | native | Recent memories |
| `ogham search <query> [--limit N] [--tags a,b] [--profile P]` | native | Hybrid search (vector + keyword + RRF). Native uses Gemini via REST + `hybrid_search_memories` RPC. Add `--legacy` for the Python tool-layer enrichment (query reformulation, entity-overlap boost, record_access). |
| `ogham store [content] [--tags a,b] [--source s] [--profile P]` | sidecar | Store a memory. Content can be a positional arg or piped on stdin: `git diff \| ogham store --source git-diff`. Native path blocked on entity extractor port -- a stderr notice tells you on first run. |
| `ogham export [--profile P] [--format json\|markdown] [-o file]` | sidecar | Export a profile's memories. Stdout by default; write to file with `-o`. |
| `ogham import <file.json> [--profile P] [--dedup 0.8]` | sidecar | Bulk-import from an `ogham export` JSON file (or `-` for stdin). |
| `ogham profile current / switch / list / ttl` | native | Profile ops. `switch` persists to TOML + env. |
| `ogham stats` | native | Headline counts, top sources, top tags |
| `ogham delete <id>` | native | Delete a memory |
| `ogham cleanup [--dry-run] [--yes]` | native | Remove expired memories (`cleanup_expired_memories` RPC) |
| `ogham decay [--dry-run] [--batch-size N]` | native | Apply Hebbian decay (`apply_hebbian_decay` RPC) |
| `ogham audit [--operation X] [--limit N]` | native | Read the audit trail |
| `ogham config show` | native | Dump resolved config with secrets masked |
| `ogham init` | interactive | huh TUI wizard; writes TOML + env |
| `ogham dashboard [--port N]` | Python subprocess | Starts the Prefab dashboard (Python stays Python for the frontend) |
| `ogham serve` | MCP server | Run as an MCP stdio server |
| `ogham hooks install / run <event>` | sidecar | Wire into Claude Code hooks |
| `ogham plugin openclaw` / `agent-zero` | offline | Emit host plugin manifest |
| `ogham auth login --api-key KEY` | gateway only | Gateway API-key management (build-tag gated) |
| `ogham version` | offline | Print version + commit + build date + Go version + platform |
| `ogham completion bash\|zsh\|fish\|powershell` | offline | Emit shell completion script (cobra built-in) |

### Global flags (persistent on every subcommand)

| Flag | Effect |
|---|---|
| `--text` | Human-readable output instead of JSON |
| `--legacy`, `--python` | Route through the Python MCP sidecar instead of native Go |
| `-q`, `--quiet` | Suppress stderr informational notices (e.g. the sidecar fallback message on `store`) |

Deprecated silent no-ops (kept so pre-rc4 scripts don't break): `--json`, `--native`. Both are now the default; the flags do nothing.

### Shell completion

Cobra exposes completion for bash / zsh / fish / powershell. One-time setup:

```bash
# bash (add to ~/.bashrc)
source <(ogham completion bash)

# zsh (add to ~/.zshrc)
source <(ogham completion zsh)

# fish
ogham completion fish | source

# powershell (add to $PROFILE)
ogham completion powershell | Out-String | Invoke-Expression
```

Then `ogham <TAB>` completes subcommands, `ogham --<TAB>` completes flags, etc.

`ogham` alone (no subcommand) starts `ogham serve`. Useful if you prefer configuring a compatible client with just `"command": "ogham"`.

## Python CLI ↔ Go CLI parity

The Go CLI aims at parity with the Python `ogham` CLI for day-to-day use. Dev-only tools stay on the Python side.

| Python | Go | Notes |
|---|---|---|
| `serve`, `init`, `health`, `dashboard`, `store`, `search` | same | core parity |
| `list-memories` | **`list`** | renamed for brevity; Go adds `--source` filter |
| `stats` | `stats` | native aggregation |
| `profiles` | `profile list` | Go splits into subcommand group (`profile current/switch/list/ttl`) |
| `use` | `profile switch` | Go persists to TOML+env |
| `delete`, `cleanup`, `decay`, `audit`, `config` | `delete`, `cleanup`, `decay`, `audit`, `config show` | native-only; mirror the Python SQL RPCs |
| `hooks install/recall/inscribe` | `hooks install` / `hooks run <event>` | same underlying Python handlers |
| `export`, `import` | — | still Python-only -- pair with native `store` when entity extractor is ported |
| `openapi` | — | dev-only; stays Python |

Go-only: `auth`, `plugin openclaw/agent-zero`, `import-agent-zero`, `profile ttl`, `version`.

See `docs/plans/2026-04-16-go-cli-enterprise.md` in the R&D repo for the live feature-port tracker with per-tool status (Python MCP side and CLI side).

## Tips for enterprise / locked-down machines

### First-run playbook on a locked-down Claude Code

The whole reason this binary exists. Follow in order:

1. **Install the binary.** `chmod +x` and drop into `/usr/local/bin` (or any PATH dir). No Python, no runtime, no registration.
2. **Run `ogham init`.** The wizard collects your Supabase / Postgres + embedding provider, writes `~/.ogham/config.toml` and `~/.ogham/config.env` (permissions `0600`). It will attempt to auto-register with Claude Code and **fail on locked-down machines** -- that failure is expected, not a problem. See the next section.
3. **Pre-flight check:**
   ```bash
   ogham health                    # parallel probes, DB + embedder config (native is default)
   ogham health --live-embedder    # burns one provider token; hits Gemini/Voyage/etc. for real
   ogham health --legacy --text    # route through Python sidecar, human-readable
   ```
4. **Drop this into your project's `CLAUDE.md`:**
   ```markdown
   ## Ogham shared memory
   Invoke via Bash:
       ogham search "what you're about to work on"
       ogham store "what you learned" --tags type:decision
       ogham list --limit 20
   ```
5. **Start Claude Code.** It will shell out to `ogham` via its Bash tool. Enterprise MCP policy doesn't apply -- nothing is registered.

### Expected "failures" that aren't failures

**`Cannot add an MCP server. Enterprise MCP configuration is active and has exclusive control over MCP servers.`**
This is the policy blocking `claude mcp add ogham`. It's the exact situation the Go CLI was built to route around. The init wizard prints the manual command as a suggestion; don't re-run it, use the `CLAUDE.md` Bash workflow above instead.

**First sidecar-backed command is slow (~15-30 s).**
`uv tool run --from "ogham-mcp[...]"` downloads the Python distribution + provider SDK the first time. The download is cached per user, so the second run is fast. To skip the ephemeral install entirely: `uv tool install --refresh "ogham-mcp[postgres,gemini]"` once, then `export OGHAM_SIDECAR_CMD="ogham serve"` in your `.env`.

**macOS `"ogham" cannot be opened because Apple cannot check it for malicious software`.**
Pre-release binaries are unsigned. See the "Removing the quarantine flag on first run" section below for the one-line fix. Signed + notarized builds are planned for v0.4.

**Other MCP clients on the same locked machine.** The enterprise policy applies to *Claude Code* specifically. Cursor / Windsurf / Codex / Claude Desktop have separate config systems. `ogham init` prints snippets for each -- try those too.

## Troubleshooting

**`error: Failed to spawn: ogham`**
The ephemeral `uv tool run` couldn't find a Python project. Either set `OGHAM_SIDECAR_CMD="uv tool run --python 3.13 --from ogham-mcp ogham serve"` or install `ogham-mcp` as a permanent uv tool.

**`No solution found when resolving tool dependencies: Python>=3.13`**
Your shell's default Python is older than 3.13. The default command pins `--python 3.13`; if you overrode via `OGHAM_SIDECAR_CMD`, add `--python 3.13` there too.

**`google-genai package not installed` / `voyageai not installed`**
Your `~/.ogham/config.env` is missing the `OGHAM_SIDECAR_EXTRAS` line. This can happen if init was run with an older binary (pre-v0.3.0-rc2). Fix:
```bash
ogham init --yes --no-register    # re-runs the writer with extras derivation
# or manually
echo 'OGHAM_SIDECAR_EXTRAS=postgres,gemini' >> ~/.ogham/config.env
```
v0.3.0-rc2+ derives the extras automatically from your provider + backend choices.

**`SUPABASE_URL is required for SupabaseBackend`**
Python can't see your config. The Go binary reads `~/.ogham/config.env` and `$PWD/.env` on startup and forwards their values to the sidecar -- make sure one of those files has your credentials. Remember shell env > project `.env` > `~/.ogham/config.env`.

**Sidecar starts cleanly but `list` returns no rows.**
Check the profile: `ogham profile current`. If it's not what you expected, `ogham profile switch work` persists the change to config. Memories with `expires_at` in the past are hidden; `ogham profile ttl <name>` inspects the current TTL.

**Dashboard shows "default" profile even though `ogham profile current` says "work".**
Bug in v0.3.0-rc1 -- Python's `ogham dashboard` typer CLI hardcoded `--profile default="default"`. Fixed in v0.3.0-rc2 (Go passes `--profile <cfg.Profile>` explicitly) and in Python `ogham-mcp` v0.10.4+ (typer default is None, falls back to `settings.default_profile`). Upgrade both.

**Profile changed but subprocesses still see the old value.**
The Go CLI emits **both** `DEFAULT_PROFILE` (Python's name) and `OGHAM_PROFILE` (Go's name) in the subprocess env. If you manually edited `~/.ogham/config.toml` without running `ogham init --yes`, the env file may still hold the old value -- re-run init or edit `config.env` directly.

**Switched embedding providers and search results look like noise.**
Stored vectors were indexed under the old provider; cosine distance against a new provider's query vector is random. Fix: `uv tool run --from ogham-mcp[postgres,<new-provider>] ogham re-embed-all --profile <name>`. BM25 keyword matches still work in the meantime.

## Config unification cheat sheet

Everything is in `~/.ogham/config.toml` (Go canonical) and mirrored to `~/.ogham/config.env` (Python-readable). Both written by `ogham init`; keep in sync by editing one and running `ogham init --yes` to regenerate the other.

| What you want to change | Where |
|---|---|
| Active profile | `ogham profile switch <name>` (writes both files) |
| Embedding provider / key | `ogham init` (or edit env file + re-run `ogham init --yes`) |
| Database connection | `ogham init` (or edit env file directly) |
| Sidecar extras (`gemini`, `voyage`, etc.) | Derived by `ogham init` from your provider + backend choices; override with `OGHAM_SIDECAR_EXTRAS=...` in your shell or `.env` |
| Full sidecar command | `OGHAM_SIDECAR_CMD="..."` shell override (highest priority) |

## Status and roadmap

| Version | What | Audience |
|---|---|---|
| v0.1 | Sidecar subcommands: `search`, `store`, `list`, `health`. Python sidecar spawn via MCP go-sdk. Dotenv loader. | Internal dogfood |
| v0.2 | `ogham plugin openclaw` and `ogham plugin agent-zero` manifest subcommands. Still sidecar-backed. | Internal dogfood |
| v0.3 | Native path becomes default. huh TUI `ogham init` writes TOML + env + `OGHAM_SIDECAR_EXTRAS`. Native `list / search / health / stats / profile / delete / cleanup / decay / audit / config show`. `ogham dashboard` subprocess-exec's the Prefab dashboard. UX: JSON + native by default, `--text` / `--legacy` overrides. | Internal dogfood |
| **v0.4** | Homebrew tap, cross-platform CI, Apple notarization, Windows signing. | **First public release** |

Dashboard and Prefab UI deliberately stay Python-side -- absorbing them would require rebuilding the frontend in Node, which the time saved does not justify.

## Development

```bash
go build ./...               # everything compiles
go vet ./...                 # lint
go test ./...                # unit tests
go build -tags gateway .     # build the gateway-passthrough variant
go test -tags gateway ./...  # test the gateway variant
```

Pre-commit hooks (`pre-commit install`) run `go fmt`, `go vet`, `go build`, large-file and private-key checks.

### Project layout

```
ogham-cli/
├── cmd/                     # cobra subcommands
│   ├── root.go
│   ├── health.go            # sidecar-backed health
│   ├── list.go              # native default, --legacy for sidecar
│   ├── search.go            # native default, --legacy for sidecar tool-layer enrichment
│   ├── store.go             # sidecar only for now (entity extractor port pending)
│   ├── serve.go             # MCP server mode
│   ├── auth.go / init.go / hooks.go / import_agent_zero.go
│   └── helpers.go           # connectSidecar, JSON emitter, result unwrap, fallback notice
├── internal/
│   ├── sidecar/             # MCP client wrapping a Python subprocess
│   ├── native/              # Go-native tool implementations (absorption surface)
│   │   ├── config.go        # TOML + env precedence
│   │   ├── envfile.go       # dotenv auto-loader
│   │   └── list.go          # first absorbed tool
│   ├── config/              # sidecar-mode TOML loader (APIKey + GatewayURL)
│   ├── gateway/             # HTTPS client (only compiled under //go:build gateway)
│   └── mcp/                 # MCP server-mode tool forwarding
└── main.go
```

## License

MIT
