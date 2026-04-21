#!/usr/bin/env python3
"""Generate the Day 5 parity fixture for Go extraction tests.

Runs Python's src/ogham/extraction.py over a curated 100-memory corpus
and pins the expected outputs as JSON. The Go side's parity test at
internal/native/extraction/parity_test.go reads this file and asserts
that Go's Entities / DatesAt / Importance agree with Python within
tolerance.

v0.5 parity scope (per docs/tracking04plus.md): Go extracts a strict
subset of Python's entity categories -- entity:, file:, error:,
person:. Python also emits event:/activity:/emotion:/relationship:/
quantity:/preference:, which are v0.6+ work (#141 / broader
internal/native/ retrofit). This generator filters the Python entity
list down to the shared subset before writing it to the fixture so
the Go test compares apples to apples.

Run:
    cd /path/to/ogham-mcp-dev-repo
    .venv/bin/python3 \\
        ../ogham-cli/internal/native/extraction/testdata/parity/gen_parity_fixture.py

Regenerate whenever:
    * the corpus below changes
    * Python's extract_entities / extract_dates / compute_importance
      change in a way that affects the corpus
    * a new entity category is absorbed on the Go side (widen the
      SHARED_PREFIXES filter below)
"""

from __future__ import annotations

import json
import os
import sys
from datetime import datetime

# Allow running from ogham-cli without installing ogham-mcp: prepend the
# sibling sharedmemory repo's src/ to sys.path.
_HERE = os.path.dirname(os.path.abspath(__file__))
_OGHAM_SRC = os.path.abspath(
    os.path.join(_HERE, "..", "..", "..", "..", "..", "openbrain-sharedmemory", "src")
)
if os.path.isdir(_OGHAM_SRC) and _OGHAM_SRC not in sys.path:
    sys.path.insert(0, _OGHAM_SRC)

from ogham.extraction import (  # noqa: E402
    compute_importance,
    extract_dates,
    extract_entities,
)

# v0.5 shared entity prefixes between Go and Python. Anything outside
# this set is Python-only (event:/activity:/emotion:/relationship:/
# quantity:/preference:) and therefore excluded from parity comparison.
SHARED_PREFIXES = ("entity:", "file:", "error:", "person:")

# Pinned reference date: makes relative-date extraction reproducible.
# Every "yesterday" / "last week" / "in three days" in the corpus is
# resolved against this timestamp rather than today's real clock.
REFERENCE_DATE = datetime(2026, 4, 21, 12, 0, 0)

# 100-memory corpus. Hand-curated to hit the axes that matter:
# - ISO dates, natural dates, relative dates, mixed
# - CamelCase entities, file paths, error types, person names
# - Importance signals: decision / error / architecture / code / tags
# - Unicode, empty-ish, very long
# - Parity edge cases: content that Python would tag with event: /
#   emotion: (filtered out) mixed with shared-subset tags.
CORPUS: list[dict] = [
    # --- Decisions / architecture ---
    {"content": "We decided to use Voyage embeddings for Ogham because text-embedding-3-small underperformed on LoCoMo.", "tags": ["type:decision"]},
    {"content": "Refactored internal/native/store.go to use errgroup.WithContext for the parallel embed + search legs.", "tags": ["project:ogham", "type:refactor"]},
    {"content": "Agreed on 512-dim vectors across all providers to match the current Supabase schema vector(512).", "tags": ["type:decision", "schema:vector-512"]},
    {"content": "The gateway codepath is marked for deletion once the v0.5 MVP ships -- it's dead code after the pivot to pure technology layer.", "tags": ["type:decision"]},
    {"content": "Locked testing standards for internal/native/: 90% coverage, PICT matrices, fuzz, benchmarks, live smoke tests.", "tags": ["type:decision", "testing"]},
    {"content": "Picked modernc.org/sqlite over mattn/go-sqlite3 to keep CGO_ENABLED=0 so cross-compile stays trivial.", "tags": ["type:decision"]},
    {"content": "Decision: Gemini's raw-HTTP client stays, google.golang.org/genai rejected on binary-weight grounds (~15 MB grpc+protobuf).", "tags": ["type:decision", "dependencies"]},
    {"content": "We will normalize Gemini embeddings client-side when requested dim < 3072 to match Google's docs.", "tags": ["type:decision", "gemini"]},
    {"content": "Dedup threshold locked at 0.8 for v0.5; revisit at v0.6 if we see false merges in the field.", "tags": ["type:decision"]},
    {"content": "Move intent detection patterns to YAML multilingual word lists so we can add languages without touching Go code.", "tags": ["type:decision"]},

    # --- Errors / bugs with error types ---
    {"content": "ConnectionRefusedError when hitting Ollama on localhost:11434 after Docker restart -- the container hadn't bound yet.", "tags": ["type:bug"]},
    {"content": "ValueError: Embedding dimension mismatch: got 1024, expected 512 -- forgot to pass dimensions on ada-002.", "tags": ["type:bug", "embeddings"]},
    {"content": "FileNotFoundError on /Users/example/.ogham/config.toml during the first run -- init wizard hadn't been run yet.", "tags": ["type:bug"]},
    {"content": "SSLCertVerificationError against api.voyageai.example.com -- Azure proxy certificate chain wasn't trusted.", "tags": ["type:bug"]},
    {"content": "TimeoutError on 30-second HTTP client when Gemini cold-starts a new model.", "tags": ["type:bug", "gemini"]},
    {"content": "KeyError 'embedding' in response from Mistral when output_dimension was unexpectedly sent to mistral-embed.", "tags": ["type:bug", "mistral"]},
    {"content": "The health check returned UnauthorizedError because my OPENAI_API_KEY had expired last week.", "tags": ["type:bug"]},
    {"content": "Mixing IntegrityError with concurrent INSERT -- need to add ON CONFLICT DO NOTHING to the memories table.", "tags": ["type:bug", "db"]},

    # --- File paths ---
    {"content": "Updated internal/native/cache/embedding_cache.go to add the Default() singleton.", "tags": ["project:ogham"]},
    {"content": "Ported src/ogham/embeddings.py's _embed_gemini to Go at internal/native/embedding.go.", "tags": []},
    {"content": "Wrote tests/test_gemini_normalization.py with 5 cases covering the helper, sub-3072 fix, and 3072 no-op.", "tags": []},
    {"content": "Bumped tests/conftest.py to include a shared fixture for the parity harness.", "tags": []},
    {"content": "Added testdata/cache/fixture.db as a cross-stack round-trip fixture for /Users/example/foo/bar.txt path extraction.", "tags": []},
    {"content": "Deleted internal/native/embedding_cached_test.go after moving the wrapping tests to testhelpers_test.go.", "tags": []},
    {"content": "The makefile target pict-regen now scans internal/native/**/testdata/*.pict so new packages pick it up.", "tags": []},
    {"content": "Log output goes to /var/log/ogham/app.log on Linux; macOS uses ~/Library/Logs/ogham/app.log.", "tags": []},

    # --- CamelCase entities / products / libs ---
    {"content": "The EmbeddingCache type has Open, Get, Put, Contains, Len, Clear, Stats methods plus a Key() helper.", "tags": []},
    {"content": "Uses FastMCP with StdioTransport and exposes store_memory, hybrid_search, list_recent as tools.", "tags": []},
    {"content": "Railway bundles Caddy, DashboardAPI, Gateway, BillingWorker, and Valkey into five containers.", "tags": []},
    {"content": "The CachedEmbedder wraps GeminiEmbedder, OllamaEmbedder, OpenAIEmbedder, VoyageEmbedder, MistralEmbedder.", "tags": []},
    {"content": "Supabase RPC hybrid_search_memories takes query_text, query_embedding, match_count, filter_profile arguments.", "tags": []},
    {"content": "Rebuilt the NetworkInspector on top of httptest.Server so unit tests don't need real Voyage or Mistral endpoints.", "tags": []},
    {"content": "We track OpenClaw, ByteRover, Cognee, Mem0, and SupermemoryAI in the competitor watch page.", "tags": ["competitors"]},
    {"content": "HeidelbergLab is the internal name for the research preview; rename before public launch.", "tags": []},

    # --- Person names ---
    {"content": "Kevin Burns shipped the Voyage + Mistral absorption in commit bfd3378 on 2026-04-21.", "tags": []},
    {"content": "Emma Carter tested the v0.4 release against his self-hosted stack; found no regressions.", "tags": []},
    {"content": "Max Fischer linked the Ogham demo video from his YouTube channel in the comments section.", "tags": []},
    {"content": "Maya Martins is interested in picking up the Agent Zero importer as a contributor task.", "tags": []},
    {"content": "Peter Müller mentioned Freiberufler status at the last industry meetup, not Gewerbe.", "tags": []},
    {"content": "Rachel Green asked whether Ogham supports Clerk Organizations for team shared memory -- it's on the v0.7 roadmap.", "tags": []},

    # --- Relative + absolute dates ---
    {"content": "The v0.5 release is scheduled for 2026-05-01 with a code freeze starting 2026-04-28.", "tags": []},
    {"content": "We reviewed the BEAM benchmark on Tuesday and saw R@10 climb from 0.73 to 0.81.", "tags": []},
    {"content": "Let's talk again tomorrow about the Contoso pitch deck.", "tags": []},
    {"content": "Three weeks ago I pushed the Gateway refactor to staging; it's been stable since.", "tags": []},
    {"content": "Yesterday Kevin signed off on the v0.4 private release.", "tags": []},
    {"content": "In five days the Hetzner migration kicks off and we cut over cloud.ogham-mcp.dev.", "tags": []},
    {"content": "Last week the Clerk webhook flow went live; signup to first store latency dropped 40%.", "tags": []},
    {"content": "Mid-April 2026 we plan the ogham-cli public release pending legal review.", "tags": []},
    {"content": "Meeting on 2026-04-25 at 10:00 CET with the DACH regional team.", "tags": []},
    {"content": "The 2026-03-16 launch went smoothly; no PagerDuty alerts for 72 hours post-launch.", "tags": []},

    # --- Importance signal: code fences + long content ---
    {"content": "```go\nfunc Store(ctx context.Context, cfg *Config, content string, opts StoreOptions) (*StoreResult, error) {\n    // orchestrator body\n}\n```\nThis is the v0.5 store orchestrator signature.", "tags": ["type:snippet"]},
    {"content": "Short note with `inline` backticks only, no fenced block.", "tags": []},
    {"content": "Long design doc: " + "The store orchestrator chains extraction -> embed + search in parallel -> surprise -> auto-link -> DB write. " * 10, "tags": ["type:design"]},

    # --- Shared subset mixed with Python-only categories ---
    {"content": "Kevin was frustrated that the first live Voyage call returned 401; turned out VOYAGE_API_KEY in .env was expired.", "tags": []},
    {"content": "Lisa's team offsite on 2026-06-15 is a fun event; we're blocking the week before for prep.", "tags": []},
    {"content": "My favorite editor is still Neovim; wrote the init.lua at /Users/example/.config/nvim/init.lua.", "tags": []},
    {"content": "3 tanks of compressed CO2 arrived at the lab on 2026-04-20.", "tags": []},
    {"content": "My colleague Anna Marchetti sent me a ClerkOrganization invite yesterday.", "tags": []},

    # --- Unicode / tricky casing ---
    {"content": "The ResumeParser handles résumé uploads from the Career portal at /srv/careers/uploads.", "tags": []},
    {"content": "München office asked for OghamMcp training; scheduled for 2026-05-10 in Frankfurt.", "tags": []},
    {"content": "日本語 support goes into v0.7; the YAML word list for Japanese is ~800 stopwords.", "tags": []},

    # --- Pure error/exception dumps ---
    {"content": "Traceback (most recent call last):\n  File \"/src/ogham/cli.py\", line 42\n  RuntimeError: could not connect to Postgres at direct endpoint.", "tags": ["type:traceback"]},
    {"content": "Caused by: PermissionDenied: service-account is missing storage.objects.get on bucket ogham-prod-backups.", "tags": ["type:traceback"]},
    {"content": "panic: runtime error: invalid memory address or nil pointer dereference\ngoroutine 7 [running]: native.(*geminiEmbedder).Embed", "tags": ["type:traceback"]},

    # --- Hybrid: mixes file + CamelCase + dates + person ---
    {"content": "On 2026-03-10 Kevin Burns ported src/ogham/extraction.py to internal/native/extraction/entities.go in a 3-hour session.", "tags": ["project:ogham"]},
    {"content": "The CI workflow at .github/workflows/test.yml runs make cover against Go 1.26 since 2026-04-15.", "tags": []},
    {"content": "Nora Svensson merged PR #142 on 2026-04-21 fixing the GeminiEmbedder normalization bug.", "tags": ["type:merge"]},
    {"content": "Built an EmbeddingCacheBench to compare Gemini vs Voyage at /Users/example/bench/cache.go -- Voyage is 40% faster cold.", "tags": []},

    # --- Empty-ish / whitespace / minimal ---
    {"content": "ok.", "tags": []},
    {"content": "Short note with no signals.", "tags": []},
    {"content": "https://ogham-mcp.dev is the canonical site.", "tags": []},

    # --- Tags-heavy importance path ---
    {"content": "Short memo that should still score high on importance because of the 4-tag bundle.",
     "tags": ["type:decision", "project:ogham", "priority:high", "owner:kevin"]},

    # --- Repetition / duplication stress ---
    {"content": "EmbeddingCache EmbeddingCache EmbeddingCache EmbeddingCache -- make sure dedup works.", "tags": []},
    {"content": "/var/log/ogham.log /var/log/ogham.log /var/log/ogham.log repeated paths should not all land as tags.", "tags": []},

    # --- Mixed-language memory ---
    {"content": "Wir haben entschieden, EmbeddingCache zu verwenden. Kevin Burns prüft den Implementierungsstand am Dienstag.", "tags": []},
    {"content": "El archivo /etc/ogham/config.env tiene todas las variables de entorno que necesitamos.", "tags": []},

    # --- More file-path shapes ---
    {"content": "Config file lives at ~/.config/ogham/config.toml on XDG systems, ~/.ogham/config.toml otherwise.", "tags": []},
    {"content": "Absolute windows path C:\\Users\\kevin\\ogham-cli\\go.mod should NOT tag a file:path entity (unix-style only).", "tags": []},
    {"content": "Relative paths like ./scripts/sync.sh and ../ogham-mcp/README.md show up in commit messages.", "tags": []},

    # --- More person-name shapes ---
    {"content": "Eleanor Whitfield-Morgan (two-word hyphenated last name) reviewed the BEAM harness design.", "tags": []},
    {"content": "Peterson Lee is a common misreading of two distinct people in the Contoso EMEA org.", "tags": []},
    {"content": "Dr. Alan Turing first formalised the halting problem; see Turing (1936).", "tags": []},

    # --- More date shapes ---
    {"content": "Scheduled a 1:1 for next Tuesday at 14:00 CET to review the sprint.", "tags": []},
    {"content": "The last commit on this branch was 30 minutes ago; no further work pending.", "tags": []},
    {"content": "Planning the Q3 2026 roadmap review for early July.", "tags": []},

    # --- Edge cases for Go's PICT regen: dense entity mix ---
    {"content": "The FastAPIApp calls ValkeyClient, NeonPool, VoyageEmbedder, SupabaseBackend in a single request.", "tags": ["stack"]},
    {"content": "See PR #42 at https://github.com/ogham-mcp/ogham-cli/pull/42 for the Gemini normalization fix.", "tags": []},

    # --- Near-duplicate content, different dates ---
    {"content": "Kevin pushed commit abc123 on 2026-04-21.", "tags": []},
    {"content": "Kevin pushed commit def456 on 2026-04-22.", "tags": []},

    # --- Sentences Go should NOT produce person: tags for ---
    {"content": "If You Run Into Trouble with the wizard, try the manual setup.", "tags": []},
    {"content": "And She said the deployment was green on both regions.", "tags": []},
    {"content": "Be Sure To check the health endpoint before cutting over.", "tags": []},

    # --- Very long content (>500 chars triggers importance bonus) ---
    {"content": "The v0.5 MVP comprises five days of work: Day 1 entities, Day 2 dates + scoring, Day 3 embedders (OpenAI, Voyage, Mistral), Day 4 store orchestrator, Day 5 parity harness, Day 6 rc1 tag. Each day lands with PICT + fuzz + bench + CI gate and a Python-parity assertion. The orchestrator uses errgroup.WithContext for parallel embed + search legs, matches Python's store_memory tool shape exactly, and routes through the shared SQLite cache so sidecar + native users see identical vectors.", "tags": ["type:design"]},

    # --- Contradiction/negation phrasing ---
    {"content": "Actually we're not using Voyage -- switched to Gemini at dim 512 for the Hetzner deployment.", "tags": []},
    {"content": "Scratch that: ByteRover is NOT a competitor; they sit in a different segment.", "tags": []},

    # --- Sprint checkpoint-style content ---
    {"content": "Sprint wrap: 6 commits landed, 510 Python tests passing, all Go packages green under race detector.", "tags": ["type:checkpoint"]},

    # --- Mixed signals / full-spectrum ---
    {"content": "ArchitectureDecision 2026-04-21: MistralEmbedder rejects dim != 1024 for mistral-embed at construction. Kevin approved. See internal/native/embedding.go lines 102-107.", "tags": ["type:decision", "arch:adr"]},
]


def shared_entities(raw: list[str]) -> list[str]:
    return sorted(tag for tag in raw if tag.startswith(SHARED_PREFIXES))


def main() -> int:
    out_path = os.path.join(_HERE, "parity.json")
    records = []
    for i, item in enumerate(CORPUS):
        content = item["content"]
        tags = item.get("tags") or []
        py_entities = extract_entities(content)
        py_dates = extract_dates(content)
        py_importance = compute_importance(content, tags)
        records.append({
            "index": i,
            "content": content,
            "tags": tags,
            # Full Python output for reference (and for v0.6+ tightening).
            "python_entities_full": sorted(py_entities),
            # Shared subset -- Go should match this within tolerance.
            "python_entities_shared": shared_entities(py_entities),
            "python_dates": sorted(py_dates),
            "python_importance": py_importance,
        })
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({
            "reference_date": REFERENCE_DATE.isoformat() + "Z",
            "shared_entity_prefixes": list(SHARED_PREFIXES),
            "count": len(records),
            "records": records,
        }, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"wrote {out_path} ({len(records)} records)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
