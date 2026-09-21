#!/usr/bin/env bash
#
# ogham-cli install script.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash -s -- --version v0.13.1
#   INSTALL_DIR=/usr/local/bin curl -sSL https://.../install.sh | bash
#   BASE_URL=https://mirror.example/ogham/v0.13.1 bash install.sh
#   bash install.sh --name omcli        # install under a different name
#
# What it does:
#   1. Detects platform (darwin / linux / windows) and arch (amd64 / arm64)
#   2. Downloads the matching release tarball/zip from GitHub
#   3. Verifies its SHA-256 against the release's checksums.txt
#   4. Extracts the binary into $INSTALL_DIR (default ~/.local/bin), under
#      the name given by --name (default `ogham`), refusing to overwrite a
#      file that is not this binary
#   5. On macOS: ad-hoc codesigns + removes com.apple.quarantine so Gatekeeper
#      stops blocking the unnotarized binary
#   6. Prints `ogham --version` so the install is self-verifying
#
# No `gh` CLI required -- uses plain curl against github.com release assets.

set -euo pipefail

REPO="ogham-mcp/ogham-cli"
# BASE_URL lets an operator point the installer at a mirror or an
# air-gapped artifact store holding the same asset + checksums.txt
# layout. Defaults to GitHub releases. Also what the negative-path
# checksum test drives against a local file:// tree.
BASE_URL="${BASE_URL:-}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
# INSTALL_NAME is the filename written to disk. It is NOT the name inside
# the release archive (ARCHIVE_BINARY, always `ogham`), and the two are
# deliberately separate: the Python ogham-mcp package owns the name
# `ogham` on any machine where it is installed, so this CLI is installed
# there as `omcli` or `om` instead. cmd/hooks.go knows all four names --
# see oghamGoBinaryNames -- and the installer has to be able to produce
# them or the hook matcher's knowledge is academic.
INSTALL_NAME="${BINARY_NAME:-ogham}"
FORCE="${FORCE:-0}"
SKIP_CHECKSUM="${SKIP_CHECKSUM:-0}"

# --version <tag> overrides the default of "latest". Useful for pinning a
# specific release in CI or when you need to roll back.
# --name <name> installs under a different filename (default `ogham`).
# --force overrides both the PATH-collision check and the target-identity
# check (see below) so an upgrade always proceeds without a prompt.
# --skip-checksum bypasses SHA-256 verification. Escape hatch for an
# environment with no sha256 tool; not something to reach for casually.
while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    --version=*)
      VERSION="${1#--version=}"
      shift
      ;;
    --install-dir)
      INSTALL_DIR="$2"
      shift 2
      ;;
    --install-dir=*)
      INSTALL_DIR="${1#--install-dir=}"
      shift
      ;;
    --name)
      INSTALL_NAME="$2"
      shift 2
      ;;
    --name=*)
      INSTALL_NAME="${1#--name=}"
      shift
      ;;
    --force)
      FORCE=1
      shift
      ;;
    --skip-checksum)
      SKIP_CHECKSUM=1
      shift
      ;;
    -h|--help)
      sed -n '2,20p' "$0"
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

# --name must be a bare filename. Without this, `--name ../../../bin/sh`
# would make an installer that writes wherever it is pointed, and this
# script runs under `curl | bash`.
case "$INSTALL_NAME" in
  ""|.|..|*/*)
    echo "--name must be a bare filename, not a path: got '${INSTALL_NAME}'" >&2
    exit 2
    ;;
esac
case "$INSTALL_NAME" in
  *[!A-Za-z0-9._-]*)
    echo "--name may only contain letters, digits, dot, underscore and hyphen: got '${INSTALL_NAME}'" >&2
    exit 2
    ;;
esac

TARGET_PATH="${INSTALL_DIR%/}/${INSTALL_NAME}"

# PATH-collision check (#7): the Python ogham-mcp package and the Go
# ogham-cli both ship a binary named `ogham`. If a user installs the Go
# CLI on a machine that already has the Python binary on PATH, the
# resulting shell-typed `ogham` will be ambiguous and depend on PATH
# order. Refuse to install in that case unless --force is passed; an
# in-place upgrade (existing binary == our install target) proceeds
# without prompting.
EXISTING_ON_PATH="$(command -v "$INSTALL_NAME" 2>/dev/null || true)"
if [ -n "$EXISTING_ON_PATH" ] && [ "$EXISTING_ON_PATH" != "$TARGET_PATH" ] && [ "$FORCE" != "1" ]; then
  echo "==> A binary named ${INSTALL_NAME} is already on \$PATH:" >&2
  echo "      ${EXISTING_ON_PATH}" >&2
  echo "    This is most likely the Python ogham-mcp package (separate product)." >&2
  echo "    Installing the Go CLI to ${TARGET_PATH} will create a name collision -- which one" >&2
  echo "    wins on the shell depends on PATH order and is easy to confuse." >&2
  echo "" >&2
  echo "    Options:" >&2
  echo "      1. Re-run with --name omcli to install under a different name." >&2
  echo "      2. Re-run with --force to install anyway (you'll manage PATH order yourself)." >&2
  echo "      3. Re-run with --install-dir=<path> to install somewhere off PATH (e.g. ~/tools/bin/)." >&2
  echo "      4. Uninstall the Python ogham-mcp first if you don't need it." >&2
  exit 1
fi

# Target-identity check (#51 follow-up). The check above asks a question
# about LOCATION -- "is something else called ogham on PATH?" -- and that
# is not the same question as "is the file I am about to destroy mine?".
#
# The gap is not theoretical. On a machine that develops ogham-mcp,
# ~/.local/bin/ogham is a symlink into that project's venv, which is
# exactly $INSTALL_DIR/ogham. The PATH check therefore sees
# EXISTING == TARGET, concludes "in-place upgrade", and `mv -f` silently
# replaces the Python entry point with this binary. The user loses a
# working install of a different product and gets no warning at all.
#
# So: before overwriting anything, prove the thing being overwritten is
# ours. Refuse otherwise, and say which name to use instead.
target_is_ogham_cli() {
  path="$1"
  # A symlink is never how this installer leaves a binary -- it uses
  # `mv`. A venv console-script shim, on the other hand, is precisely
  # this. Checked first because it needs no execution.
  if [ -L "$path" ]; then
    return 1
  fi
  [ -f "$path" ] || return 1
  # A shebang means a script, so not a compiled Go binary. Detected by
  # reading two bytes rather than by running it: this branch exists to
  # avoid executing a file we have already failed to identify.
  if [ "$(head -c 2 "$path" 2>/dev/null)" = "#!" ]; then
    return 1
  fi
  [ -x "$path" ] || return 1
  # Last resort, and the only positive identification: ask it. Our
  # `version` output starts with the module name.
  probe_out="$("$path" version 2>/dev/null || true)"
  case "$probe_out" in
    ogham-cli/*) return 0 ;;
    *) return 1 ;;
  esac
}

if [ -e "$TARGET_PATH" ] || [ -L "$TARGET_PATH" ]; then
  if [ "$FORCE" != "1" ] && ! target_is_ogham_cli "$TARGET_PATH"; then
    echo "==> ${TARGET_PATH} already exists and is NOT an ogham-cli binary." >&2
    if [ -L "$TARGET_PATH" ]; then
      echo "    It is a symlink to: $(readlink "$TARGET_PATH")" >&2
      echo "    That is what a Python venv console script looks like -- the ogham-mcp" >&2
      echo "    package owns the name 'ogham' wherever it is installed." >&2
    else
      echo "    It did not identify itself as ogham-cli when asked for its version." >&2
    fi
    echo "    Installing would destroy it, and this installer will not do that silently." >&2
    echo "" >&2
    echo "    Options:" >&2
    echo "      1. Re-run with --name omcli (or --name om) to install alongside it." >&2
    echo "      2. Re-run with --install-dir=<path> to install elsewhere." >&2
    echo "      3. Re-run with --force if you really do want to replace that file." >&2
    exit 1
  fi
fi

# Platform detection. Match the GoReleaser asset naming in the release
# manifest -- darwin/linux/windows + amd64/arm64. Anything else fails fast
# rather than downloading a tarball that doesn't exist.
RAW_OS="$(uname -s)"
case "$RAW_OS" in
  Darwin)               OS="darwin" ;;
  Linux)                OS="linux" ;;
  MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
  *) echo "Unsupported OS: $RAW_OS" >&2; exit 1 ;;
esac

RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $RAW_ARCH" >&2; exit 1 ;;
esac

# ARCHIVE_BINARY is what GoReleaser puts INSIDE the archive; it is fixed
# by the build and is not the name we install under (INSTALL_NAME).
if [ "$OS" = "windows" ]; then
  ASSET_NAME="ogham-cli-${OS}-${ARCH}.zip"
  ARCHIVE_BINARY="ogham.exe"
  # Windows needs the extension to execute at all, so add it back if the
  # caller's --name dropped it.
  case "$INSTALL_NAME" in
    *.exe) ;;
    *) INSTALL_NAME="${INSTALL_NAME}.exe"; TARGET_PATH="${TARGET_PATH}.exe" ;;
  esac
else
  ASSET_NAME="ogham-cli-${OS}-${ARCH}.tar.gz"
  ARCHIVE_BINARY="ogham"
fi

# GitHub redirects /releases/latest/download/<asset> to the actual latest
# tag's asset. For pinned versions the path is /releases/download/<tag>/<asset>.
if [ -n "$BASE_URL" ]; then
  DOWNLOAD_URL="${BASE_URL%/}/${ASSET_NAME}"
  CHECKSUM_URL="${BASE_URL%/}/checksums.txt"
  TAG_LABEL="${VERSION} (from ${BASE_URL})"
elif [ "$VERSION" = "latest" ]; then
  DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"
  CHECKSUM_URL="https://github.com/${REPO}/releases/latest/download/checksums.txt"
  TAG_LABEL="latest"
else
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET_NAME}"
  CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
  TAG_LABEL="$VERSION"
fi

echo "==> Platform: ${OS}/${ARCH}"
echo "==> Version: ${TAG_LABEL}"
echo "==> Install dir: ${INSTALL_DIR}"
echo "==> Install as: ${INSTALL_NAME}"

if [ ! -d "$INSTALL_DIR" ]; then
  mkdir -p "$INSTALL_DIR"
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "==> Downloading ${ASSET_NAME}..."
# -L follows redirects, -f makes curl exit non-zero on HTTP errors so a
# 404 doesn't silently produce a broken tarball.
curl -fsSL -o "${TMPDIR}/${ASSET_NAME}" "$DOWNLOAD_URL"

# Verify the download against the release's published checksums.txt
# BEFORE extracting or installing. This script ad-hoc codesigns the
# binary and strips com.apple.quarantine on macOS -- it removes the OS
# safety net, so it owes the user an integrity check of its own.
#
# GoReleaser publishes checksums.txt next to the archives, listing
# "<sha256>  <asset-name>" per line. We grep our line out and hand it to
# the platform's checker with cwd set to TMPDIR, since the manifest
# names files without a path.
if [ "$SKIP_CHECKSUM" = "1" ]; then
  echo "==> Skipping checksum verification (--skip-checksum)."
else
  if command -v shasum >/dev/null 2>&1; then
    SHA_CMD="shasum -a 256"
  elif command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD="sha256sum"
  else
    echo "No sha256 tool found (looked for shasum, sha256sum)." >&2
    echo "Install one, or re-run with --skip-checksum to bypass verification." >&2
    exit 1
  fi

  echo "==> Verifying checksum..."
  if ! curl -fsSL -o "${TMPDIR}/checksums.txt" "$CHECKSUM_URL"; then
    echo "Could not download checksums.txt from ${CHECKSUM_URL}." >&2
    echo "Re-run with --skip-checksum to install without verification." >&2
    exit 1
  fi

  if ! grep -F "  ${ASSET_NAME}" "${TMPDIR}/checksums.txt" > "${TMPDIR}/expected.sha256"; then
    echo "checksums.txt has no entry for ${ASSET_NAME}." >&2
    exit 1
  fi

  if ! ( cd "$TMPDIR" && $SHA_CMD -c expected.sha256 >/dev/null 2>&1 ); then
    echo "CHECKSUM MISMATCH for ${ASSET_NAME}." >&2
    echo "  expected: $(cut -d' ' -f1 < "${TMPDIR}/expected.sha256")" >&2
    echo "  actual:   $(cd "$TMPDIR" && $SHA_CMD "$ASSET_NAME" | cut -d' ' -f1)" >&2
    echo "The download does not match the published release. Not installing." >&2
    exit 1
  fi
  echo "    OK ($(cut -d' ' -f1 < "${TMPDIR}/expected.sha256" | cut -c1-16)...)"
fi

echo "==> Extracting..."
if [ "$OS" = "windows" ]; then
  unzip -o "${TMPDIR}/${ASSET_NAME}" -d "$TMPDIR" >/dev/null
else
  tar -xzf "${TMPDIR}/${ASSET_NAME}" -C "$TMPDIR"
fi

if [ ! -f "${TMPDIR}/${ARCHIVE_BINARY}" ]; then
  echo "Extracted archive does not contain ${ARCHIVE_BINARY}." >&2
  echo "Contents:" >&2
  ls -1 "$TMPDIR" >&2
  exit 1
fi

echo "==> Installing to ${TARGET_PATH}..."
mv -f "${TMPDIR}/${ARCHIVE_BINARY}" "$TARGET_PATH"
chmod +x "$TARGET_PATH"

# macOS-specific: the released binaries are not Apple-notarised, so a
# fresh download arrives quarantined and Gatekeeper refuses to launch it.
# Ad-hoc codesigning + `xattr -dr` is the well-known unblock; both are in
# the base macOS toolchain so no Xcode install is required.
if [ "$OS" = "darwin" ]; then
  if command -v codesign >/dev/null 2>&1; then
    echo "==> Ad-hoc signing binary..."
    codesign --force --sign - "$TARGET_PATH" >/dev/null 2>&1 || \
      echo "    (codesign failed -- you may need to run it manually)" >&2
  fi
  echo "==> Removing quarantine attribute..."
  xattr -dr com.apple.quarantine "$TARGET_PATH" 2>/dev/null || true
fi

echo "==> Done."
# `ogham version` is a subcommand, not a flag. Old --version probe is
# kept as a fallback in case future builds add the flag.
"$TARGET_PATH" version 2>/dev/null \
  || "$TARGET_PATH" --version 2>/dev/null \
  || true

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo
    echo "Note: ${INSTALL_DIR} is not on \$PATH."
    echo "Add to your shell profile:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac
