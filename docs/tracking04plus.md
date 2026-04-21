# Ogham CLI — tracking (v0.4 → v0.6)

Last updated: 2026-04-21

---

## Currently in progress

- **#137 v0.5 MVP** — Days 1–6 shipped on main. v0.5.0-rc1 tag pending.

## Just closed

- **#138** — pre-v0.5 plumbing.
- **v0.4.0** — tagged, private release green.
- **Day 1** — entities.go + PICT matrix (28 rows) + 14 hand-picked tests + fuzz + bench. 95.7% coverage.
- **Day 2** — dates.go + scoring.go shipped (importance scoring + PICT matrix + fuzz + bench). 95.0%+ coverage. Both Ubuntu + macOS CI green.
- **Day 3 first increment** — OpenAI embedder absorbed (native POST /v1/embeddings, Bearer auth, dimensions param, OPENAI_BASE_URL anti-pollution). 8 httptest cases.
- **Day 3 second increment** — Voyage + Mistral embedders absorbed. Five of five providers native + cached. Live smoke tests verified against real Voyage/Mistral endpoints.
- **#142** — Gemini L2-normalize sub-3072 dims. Go side shipped (`:embedContent` migration + `l2Normalize` helper + 5 new tests). Python parity pushed alongside in `openbrain-sharedmemory`.
- **#143** — Go port of Python `EmbeddingCache` (shared SQLite file via modernc.org/sqlite + WAL). 90.7% coverage, PICT matrix, fuzz, benchmarks, fixture-consumer test. Wrapped every embedder via `NewCachedEmbedder` so provider calls skip on cache hits.
- **Day 4** — `internal/native/store.go` orchestrator shipped. Extraction → errgroup(embed, search) → surprise → auto-link candidates → Postgres INSERT. `cmd/store.go --native-store-preview --dry-run` wired in; verified end-to-end against local config (Gemini+Supabase): dates extracted, parallel embed+search, surprise 0.86 on fresh content.
- **Day 5** — Python parity harness locked. 97-memory corpus in `internal/native/extraction/testdata/parity/` with generator + pinned JSON. Baselines: entities 93.8% / dates 100% / importance 96.9%. All above locked thresholds (75% / 70% / 85%).
- **Supabase native store write** — `writeMemorySupabase` POSTs to `/rest/v1/memories` via PostgREST, sends embedding as pgvector text literal, gets uuid back via `Prefer: return=representation`. Live write verified (id `25611992-3cc0-...` committed in 690 ms).
- **#139** — gateway client context + retry cleanup. Every `*gateway.Client` method takes `ctx` first; `http.NewRequestWithContext` throughout; retry backoff uses `select { case <-time.After(): case <-ctx.Done(): }` so Ctrl+C mid-retry returns within ms. 2 new tests lock the cancel-during-backoff guarantee.
- **Day 6** — `--native-store-preview` flag removed; native is the default for `ogham store`. Kept as a hidden deprecated no-op. README updated: v0.5 architecture section, v0.5-v0.7 roadmap, Operators section on pooler-vs-direct DDL discipline.
- **Live smoke tests** — `//go:build live` harness + `make live` target. Ollama (embeddinggemma @ 512 + 768), OpenAI (text-embedding-3-small @ 512), Voyage (voyage-3-lite @ 512), Mistral (mistral-embed @ 1024) all verified against real endpoints.
- **#141** — new task for coverage debt across the rest of `internal/native/`.

## Currently blocked

- **#137** v0.5 MVP → blocked by #138
- **#140** public flip + Homebrew → blocked by #121 + #120

---

## Shipped on main

| # | Subject |
|---|---------|
| 132 | Go CLI v0.1.0 — search / store / list + `--json` on health |
| 133 | v0.3 finish — native health + dashboard subprocess exec |
| 134 | v0.3 extensions — list filters + profile ops + stats + live tests |
| 136 | v0.3 parity quickwins — delete / cleanup / decay / audit / config show |

---

## v0.4 release (active, tomorrow AM)

Not a formal task — the work IS the tag push.

- Release-prep commit `7e6408e` ("stage 040") is on `main`
- Housekeeping staged: `.gitignore` expanded, `Makefile` gains `snapshot` / `release-check` / `tag` targets
- `docs/release-v0.4.md` remains untracked (decision pending — commit as release playbook or keep local)

**Morning sequence:**
1. `git add .gitignore Makefile` → commit → push
2. `make snapshot` — local GoReleaser dry run, confirm archives land in `dist/`
3. `make tag VERSION=v0.4.0` — tags, pushes, CI publishes to private Releases page
4. Invite collaborators who need binaries (Iain at minimum)

Lands on the **private repo**. No public announcement until #140 clears.

---

## v0.5 pipeline

### #138 — Pre-v0.5 plumbing (IN PROGRESS)

Half-day of infrastructure before feature work. Breakdown:

| Sub | Area | What |
|-----|------|------|
| 138a | Root slog + `-v` | Persistent `--verbose` / `-v` flag in `cmd/root.go`, shared `slog` handler to stderr. `--quiet` = warn+, default = info+, `-v` = debug+. Lift pattern from `cmd/serve.go:19`. |
| 138b | Redact util | Move `redactURL` to `internal/native/redact.go`, rename `redactString`. Apply to every HTTP-error path in `supabase.go` + `embedding.go`. |
| 138c | Ollama config | Push `OLLAMA_URL` onto `Config.Embedding` as `BaseURL`. Sets precedent for v0.5 Day 3 embedders. |
| 138d | Sidecar-fallback coverage | Audit every command. `noteSidecarFallback()` should fire anywhere we silently shell to Python. |

Explicitly deferred:
- Gateway `context` + retry cleanup — tracked as #139
- `init()` consolidation across 21 cmd files — v0.6+
- `ExitError` distinct exit codes — nice-to-have
- Test isolation audit — nice-to-have
- `sidecar.go:89` `exec.CommandContext` — nice-to-have

### #137 — Native store absorption MVP

Blocked by #138. Daily slices per spec:

| Day | Scope |
|-----|-------|
| 1 | `internal/native/extraction/entities.go` + English stopwords |
| 2 | `internal/native/extraction/dates.go` + `scoring.go` (importance + surprise) |
| 3 | Voyage + OpenAI + Mistral embedders (mirror Gemini + Ollama shape) |
| 4 | `store.go` orchestrator + `cmd/store.go` wire-through behind `--native-store-preview` flag |
| 5 | Integration tests + Python parity check on 100-memory corpus |
| 6 | Buffer + README + remove preview flag + `v0.5.0-rc1` |

Target: store latency drops 1.8s (sidecar) → ~400ms (native).

Key decision: match Python behaviour exactly at v0.5 (dedup 0.8 threshold, entity tag prefixes `person:` / `file:` / `location:` / `entity:`, `date:YYYY-MM-DD` tags, `metadata.dates` key). Reconsider simplifications at v0.6 if user feedback pushes.

---

## v0.6 — big items (not yet tasked; ~4 days)

| Area | Scope |
|------|-------|
| Multi-language extraction | Stopwords for `en`, `de`, `fr`, `es`, `zh`. Port the 18-language Python list, curate down to the top 5 at v0.6 (rest at v0.7 or later). Entity regexes stay English-heavy at v0.6; CamelCase + file paths are language-agnostic. |
| Recurrence detection | "every Monday", "weekly standup", "jeden Dienstag". Surfaces as a `recurrence:` metadata key + tag. |
| Contradiction detection | When a new memory contradicts an existing one (same subject + predicate, different object) flag + link via `contradicts_id`. Parity with the Python `contradict_memory` tool. |
| `init()` consolidation | Deferred review follow-up from #138. 21 cmd files use `init()` for flag registration + `rootCmd.AddCommand`; fold into a single `registerCommands()` called from `main.go`. Matters once v0.6 commands start landing. |

Candidate follow-on task: **#141** (create when v0.5 MVP lands).

---

## v0.7 — intent detection + access tracking (~4 days)

| Area | Scope |
|------|-------|
| Intent detection | Reformulation, ordering, multi-hop, summary, temporal. Ports the Python intent-detection patterns. Gated on YAML multilingual word lists (sibling to task #123). |
| `record_access` on search | Bumps `access_count` + `last_accessed_at` on retrieved memories, feeding decay + hot-tier promotion. |

After v0.7, the Python sidecar is strictly optional — dashboard + compression + experimental tools only.

---

## v0.4.1 — distribution polish

Not yet tasked. Bundle these when #140 clears:

- Apple Developer ID code signing (~$99/yr) + GoReleaser `notarize` stanza
- Windows Authenticode signing (~$200-500/yr) via DigiCert or Sectigo
- SBOM generation via GoReleaser's built-in syft integration
- Re-enable Homebrew tap (uncomment `brews:` in `.goreleaser.yml` + restore `GORELEASER_TAP_TOKEN` env in `release.yml`)

---

## Release-infrastructure tasks

| # | Status | Subject | Blocked by |
|---|--------|---------|------------|
| 139 | pending | Gateway client context + retry cleanup (review follow-up) | — |
| 140 | pending | Flip ogham-cli repo public + enable Homebrew tap | #121, #120 |

---

## Upstream legal / compliance blockers

| # | Status | Subject |
|---|--------|---------|
| 121 | pending | **CRITICAL** — file Section 12.1 disclosure to Rackspace HR/manager |
| 120 | pending | **CRITICAL** — Fachanwalt für Arbeitsrecht consult on Section 12.1 |

---

## Integration / adjacent (lean on Go CLI but aren't CLI work)

| # | Status | Subject |
|---|--------|---------|
| 73 | pending | Agent Zero importer (Cemre candidate — revisit now Go CLI exists) |
| 127 | pending | Antigravity memory importer (Google Antigravity → Ogham) |
| 96 | pending | Microsoft AgentFramework.Memory.Ogham connector (Tier 1 post-launch) |

---

## Open decision — gateway codepath

`internal/gateway/` + the gateway wiring in `cmd/serve.go` were built for the now-parked managed service (`api.ogham-mcp.dev`). Keep or retire?

- **Keep** if Rackspace (as operator-of-record) or any self-host team would actually run a gateway.
- **Retire** if the pivot to "pure technology layer" means the gateway codepath is dead code. Deletes ~1500 lines, simplifies the review for v0.5/v0.6, obsoletes #139.

Decision affects whether **#139** is worth the effort. Revisit after v0.5 ships.

---

## Review follow-ups (deferred, not blocking anything)

From the 2026-04-20 code review — parked until a natural maintenance window:

- Test isolation audit — confirm every native test uses `isolateEnv(t)`
- `ExitError` type with distinct exit codes (config=2, network=3, auth=4, tool=5)
- `sidecar.go:89` — switch `exec.Command` to `exec.CommandContext`
- `helpers.go:42 splitCSV` — add unit test since tag parsing is load-bearing

---

## Testing standards (locked 2026-04-20)

Go-side due diligence matches the Python side. Every new package in
`internal/native/` lands with:

1. **Table-driven subtests** — hand-picked readable regression cases.
2. **PICT-generated combinatorial matrix** — `.pict` model file at
   `internal/native/<pkg>/testdata/<model>.pict`; generated matrix
   consumed by a table-driven test function. Regeneration check in CI
   so an edit to the model never ships without updating the test data.
3. **Coverage gate** — **90% line coverage** for `internal/native/...`.
   Enforced by `make cover` via `go test -cover -coverprofile=cover.out
   ./internal/native/...` + a threshold check. CI fails the PR / push
   when coverage falls below 90%.
4. **Fuzz** — `go test -fuzz` for any code that parses untrusted input
   (regex-based extractors, config parsers, URL redactors).
5. **Race detector** — `go test -race` on packages with concurrent
   code paths (store orchestrator, embedder pool, sidecar lifecycle).
6. **Benchmarks** — `go test -bench` for the hot path
   (extraction runs on every `store`; regression ceiling worth guarding).
7. **Python parity harness** — Day 5 corpus of ~100 memories, run
   through both Python and Go, diff the extracted outputs. Parity
   shipped means Go can absorb silently without behaviour drift.

### Revised v0.5 Day 1 — retrofit with PICT

- Model file: `internal/native/extraction/testdata/entities.pict`
  (axes: category × punctuation-position × case-mix × stopword-presence
  × unicode-class × duplicate-density × final-entity-count)
- Generated matrix: `internal/native/extraction/testdata/entities.pict.tsv`
  (committed; regeneration check in CI)
- Test func: `TestEntities_PICT` reads the tsv, drives `Entities()`,
  asserts category-specific invariants per row
- Keep the 14 hand-picked subtests as readable regressions
- Add `FuzzEntities` seeded from the PICT corpus
- Add `BenchmarkEntities` on a representative 500-char input

### Revised Days 2–3 — PICT test-first

Design the `.pict` model *before* writing the implementation. Generate
the expected-output table, stub the functions, land the tests failing,
then make them pass. Dates and scoring are both multi-axis enough to
benefit.

### CI

New workflow `.github/workflows/test.yml`, triggers on push + PR:

- `go test -race -cover -coverprofile=cover.out ./...`
- Coverage threshold check (fails if `internal/native/...` < 90%)
- PICT regeneration dry-run (fails if committed `.tsv` doesn't match)
- Runs on Go 1.26.x, Ubuntu + macOS matrix

Separate from `.github/workflows/release.yml` (which only fires on tag
push). One workflow per concern.
