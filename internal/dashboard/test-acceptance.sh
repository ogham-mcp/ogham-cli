#!/usr/bin/env bash
#
# Dashboard prototype acceptance script.
#
# Drives agent-browser against a freshly-built `ogham dashboard` bound to
# 127.0.0.1:9999. Takes three screenshots (overview, overview-filtered,
# search) and asserts the stat cards + memory rows render.
#
# Env: assumes a scratch Postgres on :5433 with >=1 memory in the
# `scratch` profile. See docs/plans/2026-04-22-go-dashboard-action-plan.md
# step 9 for the seed commands.
#
# Usage:
#   OGHAM_BIN=/tmp/ogham-dashboard-test ./test-acceptance.sh
#   ./test-acceptance.sh              # builds fresh binary into /tmp

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OGHAM_BIN="${OGHAM_BIN:-/tmp/ogham-dashboard-test}"
PORT="${PORT:-9999}"
OUT_DIR="${OUT_DIR:-/tmp}"

# Fresh build unless the caller supplied a pre-built binary.
if [[ "${OGHAM_BIN}" == "/tmp/ogham-dashboard-test" ]]; then
  echo "[acceptance] building ${OGHAM_BIN}"
  (cd "${REPO_ROOT}" && go build -o "${OGHAM_BIN}" .)
fi

# Scratch DB config. Embeddings aren't required for Overview; Search
# needs them, so VOYAGE_API_KEY must be set if the test tries /search.
export OGHAM_PROFILE="${OGHAM_PROFILE:-scratch}"
export DATABASE_URL="${DATABASE_URL:-postgresql://scratch:scratch_dev_local@localhost:5433/scratch}"
# Force pgx when the user's ~/.ogham/config.toml also points at a
# Supabase project -- otherwise ResolveBackend picks Supabase and the
# dashboard queries land in the wrong database.
export DATABASE_BACKEND="${DATABASE_BACKEND:-postgres}"
export EMBEDDING_PROVIDER="${EMBEDDING_PROVIDER:-voyage}"
export EMBEDDING_MODEL="${EMBEDDING_MODEL:-voyage-4-lite}"
export EMBEDDING_DIM="${EMBEDDING_DIM:-512}"

echo "[acceptance] starting dashboard on :${PORT}"
"${OGHAM_BIN}" dashboard --port "${PORT}" --no-open >"${OUT_DIR}/dashboard.log" 2>&1 &
DASH_PID=$!
trap 'kill ${DASH_PID} 2>/dev/null || true' EXIT INT TERM

# Wait for the healthz endpoint to answer instead of a flat sleep.
for i in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

if ! curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
  echo "[acceptance] FAIL: server never became healthy"
  cat "${OUT_DIR}/dashboard.log" >&2
  exit 1
fi

echo "[acceptance] server healthy. driving agent-browser..."

# Close any previous browser session so we start fresh.
agent-browser close >/dev/null 2>&1 || true

fail=0
run() {
  echo "[acceptance] \$ $*"
  if ! "$@"; then
    echo "[acceptance] STEP FAILED: $*" >&2
    fail=1
  fi
}

run agent-browser open "http://127.0.0.1:${PORT}/"
run agent-browser screenshot "${OUT_DIR}/overview.png"

# Assert headline elements exist.
h1=$(agent-browser get text "h1" 2>/dev/null || true)
echo "[acceptance] h1 text: ${h1}"
case "${h1}" in
  *Ogham*) ;;
  *)
    echo "[acceptance] FAIL: h1 missing 'Ogham'"
    fail=1
    ;;
esac

card_count=$(agent-browser get count "[data-slot=card]" 2>/dev/null || echo 0)
echo "[acceptance] card count: ${card_count}"
if [[ "${card_count}" -lt 4 ]]; then
  echo "[acceptance] FAIL: expected >=4 cards, got ${card_count}"
  fail=1
fi

# HTMX filter interaction. Use `keyboard type` after focusing the input;
# hx-trigger="input changed" catches that reliably across CDP drivers.
run agent-browser click "input[name=q]"
run agent-browser keyboard type "docker"
# HTMX's delay:250ms + round-trip to /filter; 1s is ample.
sleep 1
run agent-browser screenshot "${OUT_DIR}/overview-filtered.png"

# Search page. If embeddings aren't configured this will render the
# error banner rather than results, which is still a valid render.
run agent-browser open "http://127.0.0.1:${PORT}/search?q=canonical"
run agent-browser screenshot "${OUT_DIR}/search.png"

run agent-browser close

if [[ "${fail}" -ne 0 ]]; then
  echo "[acceptance] FAILED"
  cat "${OUT_DIR}/dashboard.log" >&2
  exit 1
fi

echo "[acceptance] PASS"
echo "Screenshots: ${OUT_DIR}/overview.png ${OUT_DIR}/overview-filtered.png ${OUT_DIR}/search.png"
