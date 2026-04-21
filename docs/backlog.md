# ogham-cli backlog

Light-touch sketch of what's queued after v0.7.0-rc1. One-liner per item;
detail lives in the commit/PR that actually picks it up.

## Releases

- **v0.7.0 final** -- tag once rc1 bakes clean for a few days.
- **Homebrew tap flip** -- post-Section 12.1 disclosure (#121). Gated.

## Deprecations

- **`--legacy` alias removal** -- scheduled for v0.8. The flag was
  renamed to `--sidecar` in v0.7.0-rc4 (commit ba78312). The old name
  still parses + still routes through the Python MCP, but is hidden
  from `--help` and emits a one-line slog.Warn on use. The rename
  reflects the architecture lock (2026-04-22): Python MCP = retrieval-
  quality brain, Go CLI = enterprise-friendly access door.
  `--python` continues to alias `--sidecar` and is not deprecated.

## Capabilities still in the Python sidecar

- **`re_embed_all` native port** -- iterate rows, re-embed via existing
  `NewEmbedder`, batch `UPDATE`. Primitives all exist; ~0.5 day.
- **`compress_old_memories` native port** -- blocked on Go chat-LLM
  client (Ollama, OpenAI, Anthropic). 1--2 week project; the last
  genuinely Python-bound capability once solved.
- **Prefab dashboard** -- stays Python forever. No plan to port.

## Extraction + scoring

- ~~Wire `scoring.go` / `dates.go` to the YAML language loader.~~ --
  done. Per-memory language signal flows via `StoreOptions.Language`
  (falls back to `metadata["language"]`, then `"en"`). Scoring +
  date parsing now resolve word sets from the resolved language's
  YAML; English baseline is preserved for mixed-language content.
  Adding a new language: append to the 18-lang set under
  `internal/native/extraction/languages/` and populate the
  `today_words`, `tomorrow_words`, `yesterday_words`, `modifier_*`,
  `period_*`, `unit_*`, `ago_markers`, `in_markers`, and
  `recurrence_patterns` blocks (en.yaml + de.yaml are the reference).
- ~~Recurrence detection~~ -- done. `internal/native/extraction/
  recurrence.go` + `recurrence_patterns` YAML block, wired into
  `native/store.go` so stored memories carry
  `metadata.recurrence` + `recurrence:<normalised>` /
  `recurrence:<dayname>` tags. English + German ship populated;
  other 16 languages have empty `recurrence_patterns` blocks ready
  for content fill-in (date anchors + quantified-relative markers
  are now populated for all 18 languages; see the next bullet).
- ~~Date-anchor blocks in all 18 language YAMLs~~ -- done. Prefix-ago
  ("il y a 2 semaines", "hace 2 semanas", "vor 2 Wochen", "há 2
  semanas", "قبل 3 أيام") + suffix-ago ("ago", "fa", "geleden",
  "temu", "назад", "тому", "önce", "पहले", "전", "ó shin") +
  prefix-in / suffix-in markers are wired into `parseQuantifiedRelativePack`
  via a data-driven longest-prefix / longest-suffix match in
  `internal/native/extraction/dates.go`. Hard-coded English + German
  branches removed. `buildRelativeRe` now uses Unicode-aware word
  boundaries (`[^\p{L}]`) so Cyrillic / Devanagari / Arabic markers
  don't regress on Go RE2's ASCII-only `\b`. Deferred to v0.8:
  ja/zh quantified forms (no whitespace between digit + unit +
  marker) and postposed modifier parsing ("an tseachtain seo caite"
  for Irish, "पिछले सोमवार" for Hindi). Anchors + units stay empty
  in those cases with TODO notes in the YAMLs.
- ~~Narrower person-name regex~~ -- done. Three-rule classifier
  (punct gate / multi-lang stopwords union / per-language denylist)
  lifts parity from 93.8% to 97.9% and drops all known tech-term
  false positives (Docker Postgres, Scratch DB, Next.js, Managed
  Agents, method-enumeration bigrams). See
  `internal/native/extraction/entities.go` and
  `internal/native/extraction/languages/multilang_stopwords.txt`
  (regenerate with the `stop_words` Python package union).

## Graph

- **`suggest_connections` Supabase RPC wrapper** -- postgres-only
  natively; needs a server-side RPC before the Supabase backend path
  can reach parity.

## Testing + coverage

- ~~Lift `internal/mcp` coverage from 41%~~ -- done, 67.7% (commit
  e63f910).
- ~~Lift `internal/sidecar` coverage from 37%~~ -- done, 87.6%
  (commit ae776f2, self-referential binary pattern).
- ~~Close `internal/native/...` to ~80%~~ -- done, 78.4% (commit
  8bb2cdf). 90% gate remains aspirational for this package.
- **Lift `cmd/` coverage beyond the current 15.8%** -- needs a
  `NewRootCmd()` refactor so rootCmd can be rebuilt fresh per test
  case. Cobra's help-state leaks across `Execute()` calls otherwise.
- **Lift `internal/gateway` coverage from 44%** -- currently only
  the happy-path JSON shapes are exercised; error branches uncovered.
- **Managed Agents end-to-end validation pass** -- run the v0.7.0
  binary in an Anthropic Managed Agents session with a scratch
  Supabase project. Verify real MCP tool calls (store + search
  round-trip, graph walk, profile switch). ~$5 per session.

## Known bugs / polish

- **`OLLAMA_URL` env var honoured in the cached embedder path** --
  verified via pg_coverage_test.go Store test; worth a dedicated
  regression test too.
- **Stale-binary trap on `cmd/store.go` legacy-fallback notice** --
  pre-v0.5 binaries print "store has no native Go path yet;
  routing through the Python sidecar" and then fail if the sidecar
  doesn't have a matching config. Current releases ship v0.5+ so
  end users won't hit this, but dev-local rebuilds missed for a
  few days will. Consider adding a `--version-check` guard on the
  fallback path that errors out with "please upgrade" rather than
  silently routing.

## Deferred / low priority

- Agent Zero importer dogfood run (code ready; needs a real export
  to bite-test).
- Antigravity memory importer (#127).
- Intent detection (reformulation / ordering / multi-hop / summary /
  temporal) -- planned for v0.7 but can slip.
- `record_access` on retrieved memories (LRU-style reinforcement).
