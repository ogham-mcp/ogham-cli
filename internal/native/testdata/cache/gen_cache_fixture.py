#!/usr/bin/env python3
"""Generate a pinned EmbeddingCache SQLite fixture for Go cross-stack tests.

Data contract (matches ogham-mcp's src/ogham/embedding_cache.py exactly):

    Schema:
        CREATE TABLE embeddings (
            key        TEXT PRIMARY KEY,
            value      BLOB NOT NULL,
            created_at REAL NOT NULL DEFAULT (unixepoch('now')),
            sparse     TEXT
        )

    Key format:
        SHA-256 hex digest of "{provider}:{model}:{dim}:{text}" UTF-8,
        no trailing newline. Example:
            sha256("openai:text-embedding-3-small:512:hello").hexdigest()

    Value format:
        json.dumps(list[float]) encoded UTF-8.

    sparse:
        optional pgvector sparsevec string; NULL for dense-only callers.

    Eviction:
        DELETE ... ORDER BY created_at ASC LIMIT excess when count > max.

This fixture has 10 pinned rows with fixed `created_at` values so
regeneration is byte-deterministic. The Go consumer (Phase B) opens the
file, looks up each key, and asserts the decoded vector matches within
1e-7 (float32 machine epsilon).

Usage:
    python3 gen_cache_fixture.py > /dev/null   # overwrites fixture.db
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
FIXTURE_PATH = os.path.join(HERE, "fixture.db")

# Pinned corpus. (provider, model, dim, text, vector, sparse, created_at)
# Covers the three providers the Go cache will wrap plus a couple of
# edge cases (empty-ish text, unicode, sparse non-null).
FIXTURES = [
    (
        "openai",
        "text-embedding-3-small",
        512,
        "hello world",
        [0.1, 0.2, 0.3, 0.4],  # length != dim on purpose -- cache stores whatever it gets
        None,
        1000.0,
    ),
    (
        "openai",
        "text-embedding-3-small",
        512,
        "second entry",
        [0.01, 0.02, 0.03],
        None,
        1001.0,
    ),
    (
        "ollama",
        "embeddinggemma",
        768,
        "gemma local",
        [-0.5, 0.0, 0.5, 1.0, -1.0],
        None,
        1002.0,
    ),
    (
        "ollama",
        "embeddinggemma",
        512,
        "gemma mrl truncated",
        [0.11, 0.22, 0.33, 0.44],
        None,
        1003.0,
    ),
    (
        "gemini",
        "gemini-embedding-2-preview",
        512,
        "normalized sub-3072",
        [0.6, 0.8],  # unit vector |v|=1
        None,
        1004.0,
    ),
    (
        "voyage",
        "voyage-3-lite",
        512,
        "voyage doc path",
        [0.25, 0.25, 0.25, 0.25],
        None,
        1005.0,
    ),
    (
        "mistral",
        "mistral-embed",
        1024,
        "mistral fixed dim",
        [0.125] * 8,  # repeated pattern
        None,
        1006.0,
    ),
    (
        "openai",
        "text-embedding-3-small",
        512,
        "unicode: café ☕ — résumé",
        [0.01, -0.01, 0.02, -0.02],
        None,
        1007.0,
    ),
    (
        "openai",
        "text-embedding-3-small",
        512,
        "sparse alongside dense",
        [0.9, 0.1],
        "{1:0.5,3:0.25}/1024",  # pgvector sparsevec wire shape
        1008.0,
    ),
    (
        "openai",
        "text-embedding-3-small",
        512,
        "deterministic row 10",
        [0.001, 0.002, 0.003, 0.004, 0.005, 0.006, 0.007, 0.008, 0.009, 0.010],
        None,
        1009.0,
    ),
]


def make_key(provider: str, model: str, dim: int, text: str) -> str:
    """Match Python's `_current_embedding_model` + `_cache_key` contract."""
    payload = f"{provider}:{model}:{dim}:{text}"
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def build(path: str) -> list[tuple[str, str]]:
    """Rewrite `path` from scratch. Returns [(key, text), ...] for reference."""
    if os.path.exists(path):
        os.remove(path)

    conn = sqlite3.connect(path)
    try:
        conn.execute(
            """CREATE TABLE IF NOT EXISTS embeddings (
                key TEXT PRIMARY KEY,
                value BLOB NOT NULL,
                created_at REAL NOT NULL DEFAULT (unixepoch('now')),
                sparse TEXT
            )"""
        )
        emitted: list[tuple[str, str]] = []
        for provider, model, dim, text, vec, sparse, ts in FIXTURES:
            key = make_key(provider, model, dim, text)
            value = json.dumps(vec).encode("utf-8")
            conn.execute(
                "INSERT OR REPLACE INTO embeddings (key, value, created_at, sparse) "
                "VALUES (?, ?, ?, ?)",
                (key, value, ts, sparse),
            )
            emitted.append((key, text))
        conn.commit()
    finally:
        conn.close()
    return emitted


def main() -> int:
    emitted = build(FIXTURE_PATH)
    print(f"wrote {FIXTURE_PATH} ({len(emitted)} rows)")
    for key, text in emitted:
        print(f"  {key}  {text!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
