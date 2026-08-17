# Changelog

User-facing changes to `ogham-cli` (the Go binary). The Python
`ogham-mcp` server has its own changelog in the [ogham-mcp
repo](https://github.com/ogham-mcp/ogham-mcp).

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), loosely.

## v0.13.1 (2026-08-17)

> v0.13.0 was tagged and never published. Its release run built all five
> platform binaries and then failed at the changelog step during a
> GitHub partial outage, when the compare API returned 404 for every
> repository. The tag is left in place as a record; v0.13.1 is the first
> published release of this work.

### Fixed (release tooling)

- **The release changelog is generated from the local git history
  instead of the GitHub compare API.** `changelog.use: github` put a
  live third-party call on the release critical path, and an outage
  demonstrated it can fail a release after every binary has already
  built. `release.yml` already checks out with `fetch-depth: 0`, so the
  history is present. Verified with `GITHUB_TOKEN` emptied. (#40)

- **`go.mod` / `go.sum` committed in their tidied form.** GoReleaser
  runs `go mod tidy` as a before-hook, so every prior release built
  against a manifest the repo did not have committed. Six directly
  imported dependencies had been recorded as indirect. (#40)


### Security

- **All GitHub Actions pinned to commit SHAs** — `actions/checkout`,
  `actions/setup-go` and `goreleaser/goreleaser-action` across both
  workflows, with `goreleaser` itself pinned to `~> v2.17` instead of
  resolving `latest`. Adds `scripts/verify-action-pins.sh` and an
  `action-pins` CI job that fails when a version comment does not match
  the SHA it annotates, so a pin cannot silently drift from its label.
  This repo publishes the release binaries, so a compromised floating
  tag would reach users directly. (TBU-234)

  Landed in `1a59638` rather than in a commit of its own: it was
  committed locally before the #33 branch was cut, so the squash merge
  folded both together. Noted here because the commit title mentions
  only the extraction fix.

### Fixed

- **All-caps bigrams no longer become `person:` entities.** The v0.12.0
  context gate leaned entirely on `person_name_context_words`, and 85%
  of that list's matches come from the generic prepositions
  `by`/`to`/`from`/`with` — so `Grant EXECUTE to SECURITY DEFINER`
  reproduced `person:SECURITY DEFINER`, the exact tag v0.12.0 set out to
  remove. Two gates now: an all-caps pair is rejected on shape before
  any cue is consulted, and the generic prepositions license a candidate
  only when immediately adjacent. Verb and role cues (`met`, `said`,
  `author`, `cc`) keep the full three-token window, so genuine bylines
  are unaffected. (#33, TBU-242)

- **Every `person:` candidate now requires a licensing cue.** Titlecase
  pairs were previously exempt, so that a bare name list would still
  tag. Measured against a live store that exemption was the largest
  single source of junk in the entity graph: `person` was 74.6% of all
  entities (4,140 of 5,549), a third of those were ungated Titlecase
  pairs, and a random sample of 30 contained no people at all —
  `Durable Objects`, `REST API`, `GitHub Actions`, `Neural Graph`.
  Titlecase is not a discriminator: `Kevin Burns` and `Durable Objects`
  are the same shape. (#34, TBU-242)

  **Behaviour change.** An uncued name list —
  `"Kevin Burns, Owen Fletcher and Luis Ramirez agreed."` — no longer
  produces `person:` tags. Names carrying any cue (`by`, `from`, `user`,
  `met`, `cc`, `author`, …) are unaffected. Existing rows are untouched;
  this changes extraction going forward only.

### Notes

- The parity corpus cannot see the defect above — it holds three
  all-caps bigrams, all rejected earlier by the stopword filter — so the
  fix moves the entities parity rate by 0.0%. Person-name changes should
  be judged on the explicit fixtures in `entities_person_test.go`, never
  on the corpus rate. `TestParityCorpusCannotSeeAllCapsBigrams` fails if
  the corpus ever gains one.

- The entities parity rate now sits at **76.3% against a 75% floor**.
  The fixture has not been regenerated since 2026-04-21, so the gate
  compares current Go against a four-month-old Python and understates
  real agreement. Resolve before the next extraction change — TBU-243.

## v0.12.1 (2026-08-10)

### Fixed

- **`install.sh` now verifies the download against the release's
  `checksums.txt`** before extracting or installing. The release has
  published checksums since v0.4.0 and the installer never checked
  them -- while ad-hoc codesigning the binary and stripping
  `com.apple.quarantine`, i.e. removing the OS integrity check without
  substituting one of its own.

  A mismatch aborts with expected-vs-actual and installs nothing.
  Verification is fail-closed: no `shasum`/`sha256sum` on the box is an
  error, not a silent skip. `--skip-checksum` is the explicit opt-out.

  Worth knowing why "the archive extracted fine" was never sufficient:
  `tar` exits 0 on a `.tar.gz` with garbage appended after the gzip
  stream and yields a byte-identical payload. Extraction success does
  not imply the bytes are the published ones.

### Added

- **`BASE_URL`** in `install.sh` for mirrors and air-gapped artifact
  stores holding the same `<asset>` + `checksums.txt` layout. Defaults
  to GitHub releases.

## v0.12.0 (2026-08-10)

PostToolUse hook correctness. Six defects in the native post-tool path,
found by auditing it against the Python hook's root cause (upstream
TBU-231 / TBU-206 / TBU-232), plus a scope change to what gets captured
by default. Tracked as #26 and #30.

### Changed

- **BREAKING (capture scope): `hooks install` no longer captures Bash.**
  `defaultPostToolMatcher` is now `Write|Edit`, was `Write|Edit|Bash`.
  Decided on measurement: on the reference store, `hook:post-tool` was
  9,580 of 11,957 memories (80%) with `tool:Bash` the dominant tag at
  7,819, and only 104 of those rows -- 1.1% -- had ever been recalled.
  Sampling the recalled ones showed raw stdout captures rather than
  anything worth keeping. Write and Edit carry a durable target (a file
  path) and are unaffected.

  Existing installs keep their current matcher until they re-run
  `ogham hooks install`, or edit `settings.json` by hand.

- **PostToolUse memories no longer contain tool output.** Content is now
  the command or path plus a derived outcome. Bash memories previously
  appended up to 2000 chars of raw response.

- **Failed commands are recorded as such** -- `Bash (failed): <cmd>` plus
  an `outcome:error` tag, read from the response's `is_error`. Nothing
  previously read an outcome at all, so a failing command was
  indistinguishable from a successful one.

### Fixed

- **Duplicate suppression now works.** `filters.Deduper` kept state in a
  process-global map, but the hook runs as a fresh process per tool call,
  so the map was always empty and no duplicate was ever suppressed. State
  moved to on-disk markers under `OGHAM_DEDUPE_DIR` (default
  `<cache>/ogham/dedupe`), claimed with `O_CREATE|O_EXCL` so the check is
  atomic across processes.

- **Outcome is read from the response, not the request, and only from a
  structured field.** A string-shaped response now yields an unknown
  outcome rather than a guessed one, so the upstream failure mode -- a
  regex matching the payload envelope's own `is_error` key and marking
  every success as an error -- cannot occur here.

- **Junk `person:` entity tags.** `hasContextWordBefore` had been
  implemented and tested since v0.7 but was never called, so any
  capitalised bigram became a person -- `person:SECURITY DEFINER`,
  `person:Native PostToolUse`. `addPersonNames` now requires a licensing
  context word for bigrams that are not plain Titlecase. Ordinary names
  are unaffected. Python parity on the entities corpus drops from 96.9%
  to 89.7% against a 75% floor; all 16 newly-dropped tags are junk and no
  real name in the corpus is lost.

- **`hooks install` now reports a Python `ogham-mcp` installation in the
  same `settings.json`** instead of silently appending beside it. Both
  sets fire on every event and write to the same store, and the Python
  `PostToolUse` entry is unscoped (`matcher: ""`), so it re-introduces
  the noise the scoping above removes. New `--replace-python` flag
  removes the Python entries as part of the install. `hooks uninstall`
  likewise now reports Python entries left behind, rather than printing
  "nothing to remove" while captures continue.

### Notes

- This stops the generator; it does not clean up rows already stored.
- Whether `Write`/`Edit` capture earns its keep is still unmeasured --
  the Bash decision above is the template for revisiting it.

## v0.11.0 (2026-07-03)

Native typed-edge context graph verbs. `ogham triple` and `ogham join`
bring the upstream `ogham-mcp` v0.16 typed-edge graph to the Go CLI as
native, direct-to-Postgres commands (via pgx) -- no gateway round-trip --
each with a `--sidecar` fallback to the matching Python MCP tool.

### Added

- **`ogham triple <subject> <predicate> <object>`** -- store a typed edge
  in the entity graph (mirrors the `store_triple` MCP tool). Subject and
  object may be a canonical entity name or an alias; the predicate must be
  one of the 16 v1 vocabulary terms (`DEPENDS_ON`, `OWNS`, `ASSIGNED_TO`,
  `DECIDED`, `MENTIONS`, `BLOCKS`, `PART_OF`, `SUPPORTS`, `EVOLVED_INTO`,
  `RELATED_TO`, and their inverses). Writes are supersession-aware: a new
  edge for an existing `(subject, predicate, object, profile)` stamps the
  prior edge's `valid_to` and points its `superseded_by` at the new row.
  Flags: `--profile`, `--fact-id` (source-memory UUID), `--metadata` (JSON).

- **`ogham join <start-entity>`** -- breadth-first traversal along a
  predicate path (mirrors `query_join`). Flags: `--path` (comma-separated
  predicate path, required), `--hop-limit` (required, must be >= the path
  length), `--direction` (`outgoing` | `incoming`, default `outgoing`),
  `--profile`. Reads only current edges (`valid_to IS NULL`), terminates
  cleanly on cycles via a visited set, and returns entities in BFS
  traversal order plus the edges walked and their source-memory citations.
  An unresolvable start entity or a dead-end hop is a legitimate empty
  result (`{"entities":[],"edges":[],"citations":[]}`), not an error.

Both verbs run natively against the Postgres backend by default (JSON
output; `--text` for human-readable) and route through the Python sidecar
with `--sidecar` / `--python`.

### Compatibility

- **Requires a Postgres database carrying the entity-graph schema shipped
  by `ogham-mcp` v0.16** (the `entity_edges`, `entity_edge_predicates`,
  and `entity_aliases` tables). Against an older database the verbs report
  the missing relation.
- `--sidecar` requires `ogham-mcp` v0.16 or newer (which exposes the
  `store_triple` / `query_join` MCP tools).

## v0.10.0 (2026-06-18)

Open Knowledge Format (OKF) v0.1 export passthrough. With the matching
Python `ogham-mcp` v0.15.0 sidecar, `ogham export --format okf` now
produces an OKF v0.1 conformant bundle directory that round-trips
through `ogham import` and is portable to any other OKF-speaking tool.

### Added

- **`om export --format okf`** -- new value on the `--format` allowlist.
  The sidecar (Python `ogham-mcp` v0.15.0 or newer) writes a bundle
  directory (markdown documents with YAML frontmatter, per the
  [Google Cloud OKF v0.1 spec](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md))
  to its current working directory and returns the path. We print that
  path. The `-o` flag still works for capturing the path string to a
  file (useful for scripting), but the bundle itself lives where the
  sidecar put it.

  Existing `--format json` and `--format markdown` behaviour is
  unchanged.

### Compatibility

- **Requires `ogham-mcp` v0.15.0 or newer** for `--format okf`. Older
  sidecars reject the value at the tool boundary. `json` and `markdown`
  still work against any supported sidecar.

### Out of scope (v0.11)

- Native OKF *import* from `om import` requires the Go CLI to detect
  directory arguments and forward the path rather than `ReadFile`-ing.
  Until then, round-trip via `ogham import` on the Python CLI.

## v0.9.1 (2026-06-17)

Sidecar-routed `ogham export` / `ogham import` now round-trips. Three
drifts were stacked under the user-visible "import doesn't work" report
in issue #20 -- only the wire-shape one was on the surface; the other
two would have bitten the moment that one was fixed.

### Fixed

- **`ogham import` wire shape (#20).** The Python tool signature is
  `import_memories_tool(data: str, ...)`. The CLI was constructing
  `data` as a parsed `map[string]any`, which FastMCP's Pydantic
  validator rejected before the tool body ran. We now send the JSON
  payload as a string, matching the contract.

- **`ogham export -o file.json` wrote the MCP envelope, not the export.**
  The on-disk file was `{"status","profile","format","data":"<json>"}`
  instead of the actual export payload. Even with the wire-shape fix,
  re-importing would silently no-op (`json.loads` finds no top-level
  `memories` key, returns `imported=0`). Export now writes the inner
  payload directly, so `ogham export -o backup.json && ogham import
  backup.json` round-trips cleanly.

- **`--profile` flag was phantom on both commands.** Neither Python tool
  accepts a `profile` argument -- they hardcode `get_active_profile()`
  internally. `--profile work` was silently dropped, leaving the call
  to hit whatever was active. `--profile` is now plumbed through the
  sidecar via `OGHAM_PROFILE=<name>` env override (Python's
  `get_active_profile()` honours that env var first), so the user's
  active-profile sentinel file is never touched.

### Compatibility

- Pre-fix `ogham export -o file.json` outputs (envelope shape) are
  auto-detected and unwrapped by `ogham import`, so existing backups
  on disk still work without manual editing.

### Tests

- 8 new unit tests covering the regression, legacy envelope tolerance,
  passthrough, and validation paths.
- Live end-to-end smoke against Supabase + Gemini sidecar: 3 memories
  round-tripped intact (`imported=3, skipped=0`), active profile
  preserved.

## v0.9.0 (2026-06-05)

Hooks no longer need a gateway. `PostToolUse` runs entirely in Go
against the local backend: classify the event, mask secrets,
SIGKILL-safe queue the result, drain at next `SessionStart`. The
Railway gateway is no longer in the hot path and no longer required
for any hook feature. v0.8's `"skipped: gateway api_key not
configured"` install hint is retired.

### Added

- **`shared/` cross-stack data artifact (#16).** Promotes the
  signal/noise filter + secret-masking ruleset out of the Python
  ogham-mcp repo into an independent versioned tag stream
  (`shared-data-vX.Y.Z`) consumed by both sides. Vendored into Go via
  `git subtree` and `//go:embed`, SHA-locked in CI on both
  consumers.

  Why this matters: the prior "keep parallel copies in sync by hand"
  convention had already failed for the language YAMLs (~100-line
  drift between repos). For security-critical secret-detection
  regex, a missed pattern would leak credentials into the memory
  store. The shared-data artifact + parity gate removes that failure
  mode structurally. All 45 secret patterns are RE2-compatible and
  enforced by `TestSecretPatternsCompileAsRE2`.

- **`internal/native/filters/` -- native post-tool engine (#17).**
  Pure-Go port of the classification, dedup, and secret-masking
  primitives previously stranded behind the gateway.

  - `Classify(toolName) Verdict` -- `O(1)` map lookup against
    `always_skip_tools` / `routine_tools` / `response_gated_tools`
    plus an Ogham-self-reference guard (`mcp__ogham__`, `ogham_`,
    `store_memory`, `hybrid_search` prefixes).
  - `Deduper.IsDuplicate(sessionID, toolName, target)` -- 5-minute
    window, 30-minute prune threshold, clock-injectable for tests.
  - `MaskSecrets(text)` -- four layers applied in order:
    1. Bare tokens (45 patterns: GitHub PAT, AWS, Anthropic,
       OpenAI, Stripe, Azure, GCP, Slack, Discord, Telegram,
       Linear, Mailgun, Grafana, Doppler, Vault, ...).
    2. `KEY=value` for service prefixes -- value masked, key kept.
    3. URL credentials (`://user:pass@host`).
    4. Generic env-key=value (`api_key=`, `password=`, `database_url=`,
       ...).

  Patterns pre-compile at package init. Hot-path numbers (Apple M1
  Pro):

  ```
  Classify              22 ns/call
  MaskSecrets safe      47 us
  MaskSecrets w/ token  65 us
  MaskSecrets 2 KB     2.7 ms     <- worst case
  IsDuplicate (new)    9.8 us
  ```

- **`internal/native/outbox/` -- SIGKILL-safe directory queue (#18).**
  Durable buffer between ephemeral hook writers and the
  SessionStart-time drainer. Each writer creates one
  `{unix-nano}-{8-hex}.jsonl` file using
  `open -> write -> fsync -> rename`. POSIX guarantees rename
  atomicity, so a SIGKILL between any two steps leaves either
  nothing, a stray `.tmp` (cleaned next drain), or a complete
  `.jsonl` -- never a torn record.

  Bounds match the v0.9 council perf seat: drain caps at 1000
  records per call, context deadline bounds wall-time (30s in the
  SessionStart hook). Stray `.tmp` files older than 5 min are
  cleaned up; fresher ones are spared (could be a live writer).
  Malformed JSON or future `schema_version` is quarantined by
  renaming to `.malformed` so postmortem stays possible without
  blocking the queue.

  Numbers (Apple M1 Pro):

  ```
  Write                5.1 ms    <- hook hot path
  Drain (100 records)  9.5 ms    <- SessionStart cost
  ```

  `OGHAM_OUTBOX_DIR` overrides the default
  `$UserCacheDir/ogham/outbox` for tests and sysadmins who want the
  queue on a different volume.

### Changed

- **`hooks install` always wires PostToolUse (#19).** v0.8's
  apiKey-conditional skip for `PostToolUse` is gone. The native
  PostToolUse path (Classify -> MaskSecrets -> outbox) works without
  any gateway key, so every fresh install now gets the full hook
  set: `SessionStart`, `PostCompact`, `PostToolUse` (matcher:
  `Write|Edit|Bash`). Users with active gateway setups can still
  pass `--gateway` to force the legacy synchronous gateway path
  per-event.

- **`hooks run post-tool` defaults to native (#19).** Routing in
  `hooksRunCmd` now sends `post-tool` through `runNativePostTool`
  when a native backend is configured. The flow:

  ```
  PostToolUse fire -> filters.Classify
                   -> filters.MaskSecrets
                   -> filters.IsDuplicate (process-local)
                   -> outbox.Write              (POSIX-atomic)
                   EXIT 0

  next SessionStart fire -> outbox.Drain (30 s bounded, 1000-cap)
                         -> native.Store        (Supabase/Postgres)
  ```

  Minimal extraction in this release: `Bash` captures command +
  truncated response; `Edit` / `Write` capture file_path. Richer
  extraction (diff summarisation, gh-action classification, the full
  port of Python's `_extract_memory_content`) is on the v0.10+
  track.

- **`hooks run session-start` drains the outbox first (#19).** Any
  queued PostToolUse records from prior hook fires ship to the
  store before the session context is rendered. Drain failures log
  to stderr but never block the session-start return -- the
  remaining records stay queued for the next attempt.

- **`runGatewayPostTool` install-hint message updated (#19).** The
  v0.8 message implied users needed to wire a gateway api_key to
  enable post-tool capture. Now that the native path is the
  default, the message is only emitted when `--gateway` is passed
  explicitly without a configured key, and it points users at
  dropping the flag rather than wiring a key. The `"skipped:
  gateway api_key not configured"` install hint that confused
  users in v0.8 is retired.

### Fixed

- **`hooks install` no longer prints the stale "Skipped PostToolUse:
  gateway api_key not configured" message.** v0.9.0 wired PostToolUse
  unconditionally in `buildOghamHookSet` but `installClaudeCodeHooks`
  still ran its v0.8 apiKey-conditional print path -- so installs
  emitted a false-negative line claiming the hook was skipped even
  while it was successfully written to `settings.json`. The install
  output now prints the full `SessionStart, PostToolUse, PostCompact`
  event list and explicitly notes the native path needs no api_key.
  The hook set itself was correct in v0.9.0 -- only the install
  message lied.

- **Bare-token secret patterns now cover ~45 services, up from
  ~15.** v0.8 inherited a partial Python-side bare-token regex.
  The shared-data extraction (#16) added the full set: Grafana,
  Linear, Postman, Vault, Twilio, Mailgun, Shopify, Planetscale,
  Doppler, Heroku, GCP service-account JSON, Azure SAS / connection
  strings, Stripe restricted, GitHub fine-grained PATs, plus the
  ones already covered. Test corpus exercises GitHub, Anthropic,
  Ogham, Supabase, AWS, Neon, and Slack token shapes.



### Added

- **`ogham inscribe` -- explicit commit primitive (#11).** The legacy
  PreCompact `inscribe` hook wrote a metadata-only stub on every
  compaction (`session_id` / `cwd` / `timestamp` -- no transcript
  content). At scale that dilutes recall: every search has to sift
  through dozens of metadata stubs that say nothing about what actually
  happened in the session.

  v0.8 separates `commit` from `distill`: the caller distills (whether
  by transcript reader, skill, scribe, or future plugin), and
  `ogham inscribe` is the durable commit target.

  Content sources (mutually exclusive):

  - Positional args (joined by spaces) -- `ogham inscribe "prepared note"`
  - `--file PATH` -- read content from a file
  - `--stdin` -- read content from stdin explicitly (also auto-detected
    when stdin is a pipe and no explicit source is given)
  - `--transcript-path PATH` -- read a Claude Code PreCompact transcript
    JSONL, concatenate user+assistant turns raw (tool calls / tool
    results / images skipped). No LLM distillation; if you want a
    distilled summary, pipe content through your own LLM first.

  Standard flags: `--profile`, `--tags`, `--summary`, `--source`,
  `--dry-run`. Inscribed content is auto-tagged `type:inscribed` so
  downstream search / maintenance can distinguish from interactive
  `ogham store` writes.

  Composes cleanly with the superpowers-memory bridge spec §4.3:
  signal-gated capture -> staged JSONL buffer -> distilled flush, where
  the flush step is exactly `ogham inscribe --file <distilled.md>`.
  ogham-cli stays a fast, reliable commit target instead of accidentally
  growing into claude-mem-in-Go.

- **`ogham plugin claude-code` -- Anthropic Plugins scaffold emitter (#9).**
  Mutating `~/.claude/settings.json` is the opposite of `plugin`'s stated
  design intent ("emit manifests, host-portable, no curl-bash installer
  required"). The new emitter produces a first-class Claude Code plugin
  scaffold instead:

  ```
  ~/.claude/skills/ogham/
    .claude-plugin/plugin.json
    hooks/hooks.json
    bin/ogham           # copy of the running binary
  ```

  Hook commands use `${CLAUDE_PLUGIN_ROOT}/bin/ogham` in exec form
  (command + args), the Anthropic-prescribed pattern for plugin-scoped
  hooks. This is intentionally different from `hooks install`, which
  uses `os.Executable()` -- the absolute-path form is right for
  `~/.claude/settings.json` (the install dir never changes), but a
  plugin's install dir changes on update, so a baked absolute path
  there goes stale. `${CLAUDE_PLUGIN_ROOT}` re-resolves on every fire.

  Default target: `~/.claude/skills/ogham/` (skills-directory plugin
  layout). Loads next session as `ogham@skills-dir`, no marketplace
  plumbing required. `--scope project` writes to `./.claude/skills/ogham/`
  instead. `--output PATH` overrides the target entirely.

  Composes with the #10 gateway-key check: when no `api_key` is
  configured the emitted `hooks.json` omits PostToolUse, same logic as
  `hooks install`. PreCompact / PostCompact use the `manual|auto`
  matcher (avoids double-firing on `/compact`); PostToolUse, when
  wired, uses `Write|Edit|Bash`.

  `--migrate-from-settings` strips Go-owned hook entries from
  `~/.claude/settings.json` (verb-shape regex; Python `ogham hooks
  <verb>` lines stay intact) so the same hooks don't fire twice via
  both paths. Round-trip in one command.

  `--with-mcp` (opt-in) also emits `.mcp.json` registering ogham as a
  plugin-scope MCP server. Default: refused. At plugin scope every
  subagent gains access to `ogham_*` MCP tools, which violates the
  superpowers-memory bridge's "subagents structurally never touch the
  store" invariant. Opt in only if you understand that scope rule.

  Other flags: `--dry-run` prints the scaffold plan as JSON (no
  filesystem writes), `--force` overwrites existing files (default:
  refused), `--skip-binary-copy` skips the `bin/ogham` copy (useful
  for CI-built scaffolds where the binary is pre-staged).

  `hooks install` continues to work as a convenience -- no removal in
  v0.8. The two paths are documented side-by-side, the plugin path is
  the recommended one going forward.

### Changed

- **`hooks install` no longer wires PreCompact -> inscribe by default
  (#11).** The legacy native inscribe writes a metadata-only stub on
  every compaction (`session_id` / `cwd` / `timestamp` -- no transcript
  content), which dilutes recall at scale. Fresh installs from v0.8
  onwards wire only SessionStart, PostCompact (recall), and PostToolUse
  (when an api_key is configured -- see below). Existing users keep
  their PreCompact entry until they run `ogham hooks uninstall` then
  `ogham hooks install` to refresh.

  The same change applies to `ogham plugin claude-code`: emitted plugin
  scaffolds no longer include PreCompact.

  The `ogham hooks run inscribe` event runner stays in place for users
  with legacy entries; its docstring now marks it deprecated and points
  at the explicit `ogham inscribe` verb.

- **`hooks install` is now gateway-key-aware (#10).** Previously the
  installer unconditionally wired `PostToolUse -> post-tool` with
  `matcher: ""`, so post-tool fired on every tool call. But post-tool's
  smart filtering (classification, dedup, secret masking) lives only on
  the gateway path -- on a machine with no gateway api_key configured,
  every tool call spawned a subprocess that exited non-zero and Claude
  Code logged a hook error on every turn.

  v0.8 splits the install behaviour:

  - **No api_key configured (native-only setup):** `PostToolUse` is
    skipped entirely. The installer prints a one-line hint pointing at
    `ogham auth login` for users who want to enable it later. The
    remaining events (SessionStart, PostCompact -- see #11 for why
    PreCompact dropped out of the default set) still wire and run
    natively against Supabase / Postgres.
  - **api_key configured:** `PostToolUse` wires with
    `matcher: "Write|Edit|Bash"` (write-class tools only) rather than
    `""`. Read-class tools (Read, Grep, Glob) get filtered out by the
    gateway anyway -- now they don't even reach it. Reduces subprocess
    spawns and gateway requests roughly 3-5x in a typical session.

- **Runtime defense-in-depth for stale settings.json (#10).** When the
  post-tool hook fires but no gateway api_key is configured (settings.json
  pre-dating v0.8's install skip), `ogham hooks run post-tool` now exits
  0 with a one-time stderr notice instead of a non-zero per-call error.
  The notice mentions both remediation paths (`hooks install` to silence,
  `auth login` then `hooks install` to enable) and uses a marker file in
  `os.UserCacheDir()/ogham/` so it surfaces once and stays quiet after.

### Fixed

- **`requireGateway` now reads the persistent config file.** A latent
  bug: `cmd/hooks.go::requireGateway` called `config.Load("")` instead of
  `config.Load(config.DefaultPath())`, so it only honoured the
  `OGHAM_API_KEY` env var -- never what `ogham auth login` had written to
  `~/.ogham/config.toml`. Users who configured the gateway via the auth
  command would still hit "no api_key configured" on every hook fire.
  Every other call site (auth, serve, import_agent_zero) was already
  passing `DefaultPath()`. Now hooks does too.

## v0.7.4 (2026-06-04)

### Fixed

- **`hooks install` wrote hook commands that don't exist (#7).** The four
  events (SessionStart, PostToolUse, PreCompact, PostCompact) all wrote
  `ogham-cli hooks run <verb>` into Claude Code's `~/.claude/settings.json`,
  but the shipped binary is named `ogham`, not `ogham-cli`. Anyone who
  ran `hooks install` between v0.4.0 (2026-03-22, introduced in commit
  `fac78dc`) and v0.7.3 has a settings file that fires "command not
  found" on every SessionStart, PostToolUse, PreCompact, and PostCompact
  event -- non-destructive, but no memory features worked.

  v0.7.4 generates the hook command from `os.Executable()` (absolute
  path to the running binary, mirroring the pattern `ogham plugin` has
  used for openclaw / agent-zero emitters since v0.4). The hook now
  works regardless of binary name or `$PATH` state.

  Detection of stale entries uses verb-shape, not binary name: the
  three-token `ogham hooks run <verb>` form is Go-side; the two-token
  `ogham hooks <verb>` form is Python ogham-mcp. The idempotent
  pre-pass and `hooks uninstall` only touch Go-owned entries, leaving
  any Python `ogham hooks <verb>` lines intact.

- **`hooks install` printed Kiro setup instructions with the same broken
  `ogham-cli` command.** Now uses the resolved absolute binary path
  consistently.

### Added

- **`ogham hooks uninstall`** -- removes Go-owned ogham hook entries from
  Claude Code's `settings.json`. Remediation path for anyone whose
  config has been silently broken since v0.4.0.

  If you ran `hooks install` previously, run:

  ```
  ogham hooks uninstall   # cleans up the broken entries
  ogham hooks install     # writes the v0.7.4 fixed config
  ```

- **`install.sh --force` flag**, with a PATH-collision check that
  refuses to install over an existing `ogham` binary on PATH unless
  `--force` is set or the existing binary is at the install target
  itself (so in-place upgrades still work transparently). The check
  catches the common case of installing the Go CLI on a machine that
  already has the Python `ogham-mcp` package, where typing `ogham`
  would otherwise become ambiguous and depend on PATH order.

### Notes

- `hooks install` continues to mutate the global `~/.claude/settings.json`.
  v0.8 will add `ogham plugin claude-code` as a manifest-emitting
  alternative (parallel to existing `openclaw` / `agent-zero` plugin
  emitters) using `${CLAUDE_PLUGIN_ROOT}` for path resolution -- the
  Anthropic-prescribed pattern for plugin-scoped hooks. `hooks install`
  stays as a convenience.
- The native `inscribe` hook still writes a metadata-only stub at
  PreCompact -- low signal at scale (issue #7 finding #5). v0.8 will
  reshape `inscribe` from hook to explicit `ogham inscribe` verb that
  accepts prepared content; the signal-gated capture pattern documented
  in the superpowers-memory bridge spec is the intended target.

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
