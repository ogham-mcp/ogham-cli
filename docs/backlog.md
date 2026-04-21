# ogham-cli backlog

Light-touch sketch of what's queued after v0.7.0-rc1. One-liner per item;
detail lives in the commit/PR that actually picks it up.

## Releases

- **v0.7.0 final** -- tag once rc1 bakes clean for a few days.
- **Homebrew tap flip** -- pending employment-policy clearance (#121). Gated.

## Capabilities still in the Python sidecar

- **`re_embed_all` native port** -- iterate rows, re-embed via existing
  `NewEmbedder`, batch `UPDATE`. Primitives all exist; ~0.5 day.
- **`compress_old_memories` native port** -- blocked on Go chat-LLM
  client (Ollama, OpenAI, Anthropic). 1--2 week project; the last
  genuinely Python-bound capability once solved.
- **Prefab dashboard** -- stays Python forever. No plan to port.

## Extraction + scoring

- **Wire `scoring.go` / `dates.go` to the YAML language loader.**
  Infrastructure landed in `a49ac7c`; needs a language-detection
  signal (per-memory tag, config override, or content autodetect).
- **Recurrence detection** -- "every Tuesday", "weekly standup".
  Python shipped; Go not yet.
- **Narrower person-name regex** -- current pattern over-triggers on
  CamelCase code identifiers.

## Graph

- **`suggest_connections` Supabase RPC wrapper** -- postgres-only
  natively; needs a server-side RPC before the Supabase backend path
  can reach parity.

## Testing + coverage

- **Lift `internal/mcp` coverage from 41%** via pgcontainer-tagged
  handler round-trip tests.
- **Lift `internal/sidecar` coverage from 37%** via a test-only stub
  MCP server that exercises supervise + reconnect for real.
- **Lift `cmd/` coverage** via cobra golden-file tests.
- **Close `internal/native/...` to 90%** (#141) -- add Supabase
  httptest for graph + search paths.

## Known bugs / polish

- **`OLLAMA_URL` env var honoured in the cached embedder path** --
  verified via pg_coverage_test.go Store test; worth a dedicated
  regression test too.

## Deferred / low priority

- Agent Zero importer dogfood run (code ready; needs a real export
  to bite-test).
- Antigravity memory importer (#127).
- Intent detection (reformulation / ordering / multi-hop / summary /
  temporal) -- planned for v0.7 but can slip.
- `record_access` on retrieved memories (LRU-style reinforcement).
