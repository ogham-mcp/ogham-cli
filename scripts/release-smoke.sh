#!/usr/bin/env bash
# Post-release smoke test: exercise the PUBLISHED artifact the way a user
# would, not the way `go test` does.
#
# Why this exists. Three defects reached a tag in the v0.13.4 - v0.13.7
# run, and `go test ./...` was green for every one of them:
#
#   v0.13.4  `hooks run drain` blocked on a stdin it never reads. Only
#            visible when stdin is an open pipe -- a script, a cron
#            entry, CI -- never in a terminal and never in a unit test.
#   v0.13.6  install.sh would silently overwrite the Python ogham-mcp
#            entry point, because its guard compared paths rather than
#            asking what the file was.
#   v0.13.7  the guard added in v0.13.6 then refused to upgrade a real
#            ogham-cli, because it probed `version` (JSON by default)
#            instead of `version --text`. The unit test missed it by
#            using a fixture that was wrong in exactly the same way.
#
# Every one was found by hand, after release. This script is that hand.
#
# Usage:  scripts/release-smoke.sh v0.13.7
# Exits non-zero on the first failed assertion.

set -euo pipefail

TAG="${1:?usage: release-smoke.sh <tag>}"
REPO="${REPO:-ogham-mcp/ogham-cli}"
VERSION="${TAG#v}"

# Install under a name that will not be on anyone's $PATH. install.sh
# refuses when a DIFFERENT binary of the same name is already on PATH
# (the #7 collision check), which would make this script pass or fail
# depending on what the machine running it happens to have installed. The
# point is to exercise --name, not any particular string.
NAME="ogham-smoke"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
BIN_DIR="$WORK/bin"
mkdir -p "$BIN_DIR"

pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1" >&2; exit 1; }

echo "== release smoke: ${TAG} on $(uname -s)/$(uname -m) =="

# ---------------------------------------------------------------------
# 0. Wait for the release assets. GoReleaser creates the release and
#    uploads archives as separate steps, so a `release: published` event
#    can arrive before checksums.txt is fetchable.
# ---------------------------------------------------------------------
echo "-- waiting for assets"
asset_url="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"
for i in $(seq 1 60); do
  if curl -fsI "$asset_url" >/dev/null 2>&1; then
    pass "checksums.txt is published (after $((i * 10))s at most)"
    break
  fi
  if [ "$i" -eq 60 ]; then
    fail "checksums.txt never appeared at ${asset_url}"
  fi
  sleep 10
done

# ---------------------------------------------------------------------
# 1. Install using the installer AS PUBLISHED AT THE TAG. Not the copy in
#    the working tree -- the point is to test what a user actually runs.
#    install.sh verifies the SHA-256 against checksums.txt itself, so a
#    successful install is also a checksum assertion.
# ---------------------------------------------------------------------
echo "-- install"
curl -fsSL -o "$WORK/install.sh" "https://raw.githubusercontent.com/${REPO}/${TAG}/install.sh" \
  || fail "could not fetch install.sh at ${TAG}"

bash "$WORK/install.sh" --version "$TAG" --install-dir "$BIN_DIR" --name "$NAME" >"$WORK/install.log" 2>&1 \
  || { cat "$WORK/install.log" >&2; fail "install.sh failed"; }
BIN="$BIN_DIR/$NAME"
[ -x "$BIN" ] || fail "installer did not produce an executable at ${BIN}"
pass "installed via published install.sh, as --name ${NAME}"

# ---------------------------------------------------------------------
# 2. It is the version we asked for, and it identifies itself the way
#    install.sh's own identity probe needs it to (v0.13.7).
# ---------------------------------------------------------------------
echo "-- identity"
ver_text="$("$BIN" version --text 2>/dev/null || true)"
case "$ver_text" in
  ogham-cli/*) pass "version --text identifies the product: ${ver_text%% *}" ;;
  *) fail "version --text = '${ver_text}', want a line starting 'ogham-cli/'" ;;
esac
case "$ver_text" in
  *"$VERSION"*) pass "reports ${VERSION}" ;;
  *) fail "version --text = '${ver_text}', want it to contain ${VERSION}" ;;
esac

# ---------------------------------------------------------------------
# 3. Re-running the installer over our own binary must upgrade silently.
#    This is the v0.13.7 regression: the guard refused every in-place
#    upgrade for a whole release.
# ---------------------------------------------------------------------
echo "-- upgrade in place"
if ! bash "$WORK/install.sh" --version "$TAG" --install-dir "$BIN_DIR" --name "$NAME" >"$WORK/upgrade.log" 2>&1; then
  cat "$WORK/upgrade.log" >&2
  fail "installer refused to upgrade its own binary"
fi
if grep -q "NOT an ogham-cli binary" "$WORK/upgrade.log"; then
  fail "installer did not recognise its own binary on upgrade"
fi
pass "upgrade over our own binary proceeds without prompting"

# ---------------------------------------------------------------------
# 4. And it still refuses a target that is NOT ours. This is what the
#    guard is for (v0.13.6): a venv console shim sitting at the target.
# ---------------------------------------------------------------------
echo "-- refuses a foreign target"
FOREIGN="$WORK/foreign"
mkdir -p "$FOREIGN"
printf '#!/usr/bin/env python3\nprint("python ogham-mcp")\n' > "$FOREIGN/real-shim"
chmod +x "$FOREIGN/real-shim"
ln -s "$FOREIGN/real-shim" "$FOREIGN/$NAME"
if bash "$WORK/install.sh" --version "$TAG" --install-dir "$FOREIGN" --name "$NAME" >"$WORK/refuse.log" 2>&1; then
  fail "installer overwrote a symlink it should have refused"
fi
grep -q "NOT an ogham-cli binary" "$WORK/refuse.log" || {
  cat "$WORK/refuse.log" >&2
  fail "refusal did not explain itself"
}
[ -L "$FOREIGN/$NAME" ] || fail "the symlink was damaged despite the refusal"
pass "refuses a foreign target and leaves it intact"

# ---------------------------------------------------------------------
# 5. --name must stay a bare filename. This script is run as
#    `curl | bash`; a name that can hold a slash writes anywhere.
# ---------------------------------------------------------------------
echo "-- rejects a path as --name"
if bash "$WORK/install.sh" --version "$TAG" --install-dir "$BIN_DIR" --name ../escape >"$WORK/name.log" 2>&1; then
  fail "--name ../escape was accepted"
fi
pass "--name ../escape rejected"

# ---------------------------------------------------------------------
# 6. `hooks run drain` must not block on stdin (v0.13.4). Give it a pipe
#    that stays open for 8s with nothing in it, and require it to return
#    in under 5.
#
#    A FIFO, not `( sleep 8 ) | drain`: backgrounding a pipeline and
#    waiting on it waits for `sleep` as well, so that measures the
#    pipeline rather than the consumer. It reported 8s for a binary that
#    actually returned instantly. Here drain runs in the foreground with
#    its stdin on the FIFO, so the elapsed time is its own -- and the
#    writer exits after 8s, which bounds the whole thing without a
#    watchdog.
#
#    The timing IS the assertion. What drain PRINTS is not portable: a
#    CI runner has no backend configured and gets an error, while a
#    developer machine has one and drains an empty queue in silence. An
#    earlier version of this script asserted on the error text and
#    failed on the second kind of machine -- for the environment, not
#    for the product.
# ---------------------------------------------------------------------
echo "-- drain does not block on stdin"
export OGHAM_OUTBOX_DIR="$WORK/outbox"
mkdir -p "$OGHAM_OUTBOX_DIR"
FIFO="$WORK/stdin.fifo"
mkfifo "$FIFO"
# Holds the pipe open, writes nothing, and never closes early.
( sleep 8 > "$FIFO" ) &
writer=$!
start="$(date +%s)"
"$BIN" hooks run drain < "$FIFO" >"$WORK/drain.log" 2>&1 || true
elapsed=$(( $(date +%s) - start ))
kill "$writer" 2>/dev/null || true
wait "$writer" 2>/dev/null || true
[ "$elapsed" -lt 5 ] || fail "drain took ${elapsed}s against an 8s pipe -- it is reading stdin again"
pass "drain returned in ${elapsed}s against an open pipe"

# ---------------------------------------------------------------------
# 7. `hooks status` runs and reports the outbox. Needs a settings.json
#    for detectClient() to return claude-code, so give it a throwaway
#    HOME rather than touching the runner's.
# ---------------------------------------------------------------------
echo "-- hooks status"
FAKE_HOME="$WORK/home"
mkdir -p "$FAKE_HOME/.claude"
echo '{}' > "$FAKE_HOME/.claude/settings.json"
HOME="$FAKE_HOME" "$BIN" hooks status </dev/null >"$WORK/status.log" 2>&1 \
  || { cat "$WORK/status.log" >&2; fail "hooks status exited non-zero"; }
grep -q "Outbox:" "$WORK/status.log" || {
  cat "$WORK/status.log" >&2
  fail "hooks status did not report the outbox"
}
pass "hooks status reports the outbox"

echo "== all release smoke checks passed for ${TAG} =="
