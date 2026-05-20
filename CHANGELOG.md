# Changelog

User-facing changes to `ogham-cli` (the Go binary). The Python
`ogham-mcp` server has its own changelog in the [ogham-mcp
repo](https://github.com/ogham-mcp/ogham-mcp).

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), loosely.

## v0.7.3 (2026-05-21)

### Fixed

- **`ogham hooks run <event>` 401 on Supabase-only setups (#6).** The
  four hook events (session-start, post-tool, inscribe, recall) all
  routed through the gateway client with `X-Api-Key: <cfg.APIKey>`.
  On machines where the only configured auth was a Supabase
  service_role key, the empty api_key header produced `401 Missing
  authentication` every time Claude Code fired SessionStart,
  PreCompact, or PostCompact. Headless CI and sandboxed agents had no
  way to authenticate at all.

  Adds a native path: when `cfg.ResolveBackend()` resolves to a working
  postgres / supabase backend, `session-start`, `recall`, and
  `inscribe` run locally against your store -- no gateway round-trip
  required. `--gateway` flag forces the legacy path explicitly for
  Pro+ users who want server-side smart hooks. `post-tool` stays
  gateway-only for now; its smart filtering (classification, dedup,
  secret masking) hasn't been ported. The error message when no auth
  is configured points at the right config knobs instead of returning
  a bare 401.

### Added

- **`ogham health --extended` (Path B of #5).** Partial port of v0.13's
  8-dimension health from the Python sidecar. Three dimensions live:
  `DB freshness` (last memory write recency with 24h / 72h / 30d
  scoring curve), `Corpus size` (memory count thresholds), and `E2E
  probe` (store -> hybrid_search -> delete round-trip). Output is a
  scored 0-10 readout per dimension with stoplight zones
  (GREEN/AMBER/RED) plus an overall mean. Text mode pretty-prints,
  JSON mode emits the full `ExtendedHealthReport`. The remaining 5
  dimensions (schema_integrity, hybrid_search_latency, wiki_coverage,
  profile_health, concurrency) are deferred to a follow-up release;
  the report carries `ported_dimensions` / `total_dimensions` /
  `deferred_notice` fields so callers see the partial state
  explicitly. New `--profile` flag for per-invocation profile
  selection on `ogham health`.

- **`make deps-check` and `make security-scan` Makefile targets.**
  `deps-check` surfaces outdated Go modules via `go list -u -m all`
  (informational, never blocks daily iteration). `security-scan` runs
  `gosec` (SAST) + `govulncheck` (known CVEs) via `go run @latest`,
  cached after first invocation. `make ship-check` bundles both with
  `lint` + `test` as a pre-release gate. `make check` stays fast
  (`lint` + `test`) for daily flow.

- **`gosec` + `govulncheck` pre-commit hooks.** Local hooks in
  `.pre-commit-config.yaml` that fire on `.go` file changes. Mirrors
  the openbrain-sharedmemory bandit pattern -- secrets/vuln checks
  happen at commit time so issues surface before they land on main.

## v0.7.2 (2026-04-28)

### Fixed

- **Supabase anon-key 401 trap (#2).** The CLI accepted a Supabase anon
  JWT in `supabase_key` with no warning, then returned a raw
  `supabase rpc hybrid_search_memories: http 401: {Invalid API key}` on
  every read because anon doesn't have SELECT on `memories` or EXECUTE
  on `hybrid_search_memories` — that's by design, Ogham's RLS expects
  the secret key. `ogham config show` now classifies the configured key
  (recognises `sb_secret_*`, `sb_publishable_*`, JWT `role=anon`, and
  JWT `role=service_role`) and flags an unprivileged key in both `--text`
  output (`(anon — RPCs will 401)` next to the masked key plus a
  `[warnings]` section) and JSON (`database.supabase_key_kind` and a
  top-level `warnings` array). On 401, every Supabase request path
  prepends an operator-facing hint pointing at Supabase Dashboard →
  Settings → API → Project API keys.

### Added

- **`install.sh` one-liner (#1).** Platform-detecting install script at
  the repo root. Detects `darwin` / `linux` / `windows` + `amd64` /
  `arm64`, downloads the matching release asset, drops the binary into
  `$INSTALL_DIR` (default `~/.local/bin`), ad-hoc codesigns +
  `xattr -dr com.apple.quarantine` on macOS so Gatekeeper doesn't block
  first launch, and self-verifies via `ogham version`. Supports
  `--version <tag>` for pinned installs and `--install-dir <path>` /
  `INSTALL_DIR` env var for a custom install location. No `gh` CLI
  dependency — plain `curl` against `github.com/.../releases/...`.

  ```bash
  curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash
  ```

## v0.7.0-rc4 (2026-04-22)

### Changed

- Renamed `--legacy` to `--sidecar`. The old name misread as
  "deprecated, will be removed" when the Python MCP is actually the
  canonical retrieval-quality brain and the Go CLI is an
  enterprise-friendly access door. `--sidecar` is the new primary flag;
  `--legacy` is retained as a hidden backward-compat alias that still
  works but emits a one-line deprecation warning on use. The alias
  will be removed in v0.8. `--python` continues to alias `--sidecar`.

### Added

- `ogham capabilities` -- new subcommand that prints the authoritative
  matrix of which MCP tools are implemented natively in Go versus
  which still require the Python sidecar (`--sidecar`), plus which
  search augmentations are only available through the sidecar (intent
  detection, strided retrieval, query reformulation, MMR re-ranking,
  spreading activation). Default output is grouped text for humans;
  `--json` emits a byte-stable structured payload for scripts and
  dashboards.
- `--sidecar` persistent flag on every subcommand (primary name for
  what used to be `--legacy`). Help text describes the full retrieval
  pipeline it unlocks.

### Deprecated

- `--legacy` -- still works and still routes through the Python
  sidecar, but hidden from `--help` and emits a deprecation warning.
  Scheduled for removal in v0.8.

### Prior rc4 work (extraction parity lift, already landed)

Commits in rc4 prior to the `--sidecar` rename and `capabilities`
subcommand:

- `feat(extraction): add language signal to StoreOptions + scoring + dates` -- `788c0e0`
- `feat(extraction): add recurrence detection (EN/DE)` -- `2c58588`
- `fix(extraction): tighten person-name regex (three-rule approach)` -- `f13c915`
- `docs(backlog): mark three extraction items done` -- `ecfdfd9`
- `feat(extraction): extend prefix-ago date parsing to all 18 languages` -- `dd4e2a5`

Parity on the 97-memory corpus lifted from 93.8% to 97.9% (narrower
person-name classifier drops all known tech-term false positives).
Date-anchor blocks now populated for all 18 languages; Unicode-aware
word boundaries replace Go RE2's ASCII-only `\b` so Cyrillic /
Devanagari / Arabic markers don't regress.

## v0.7.0-rc3 (2026-04-21)

Prior release candidate. See git history (`git log v0.7.0-rc2..v0.7.0-rc3`).

## v0.7.0-rc2 (2026-04-21)

Prior release candidate. Hybrid MCP proxy + 24 native tools absorbed
(Batches A/B/C/E). See git history.

## v0.7.0-rc1 (2026-04-18)

First rc of the v0.7 series. See git history.

## Earlier versions

- v0.5.0-rc1 / v0.5.0-rc2 -- native store absorption (extraction +
  5 embedders + shared SQLite cache, orchestrator chains extraction
  -> parallel embed + search -> surprise -> auto-link -> write)
- v0.4.0 -- release infrastructure (GoReleaser, GitHub Actions,
  release playbook). Private-repo release; Homebrew tap deferred
  pending employer disclosure.

Older `v0.1` / `v0.2` / `v0.3` history lives in git (`git log`).
