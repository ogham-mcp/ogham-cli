# Changelog

User-facing changes to `ogham-cli` (the Go binary). The Python
`ogham-mcp` server has its own changelog in the `openbrain-sharedmemory`
/ `ogham-mcp` repos.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), loosely.

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
