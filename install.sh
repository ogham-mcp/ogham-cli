#!/usr/bin/env bash
#
# ogham-cli install script.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash -s -- --version v0.13.1
#   INSTALL_DIR=/usr/local/bin curl -sSL https://.../install.sh | bash
#   BASE_URL=https://mirror.example/ogham/v0.13.1 bash install.sh
#
# What it does:
#   1. Detects platform (darwin / linux / windows) and arch (amd64 / arm64)
#   2. Downloads the matching release tarball/zip from GitHub
#   3. Verifies its SHA-256 against the release's checksums.txt
#   4. Extracts the binary into $INSTALL_DIR (default ~/.local/bin)
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
FORCE="${FORCE:-0}"
SKIP_CHECKSUM="${SKIP_CHECKSUM:-0}"

# --version <tag> overrides the default of "latest". Useful for pinning a
# specific release in CI or when you need to roll back.
# --force overrides the PATH-collision check (see below) so an upgrade
# over an existing install always proceeds without a prompt.
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

# PATH-collision check (#7): the Python ogham-mcp package and the Go
# ogham-cli both ship a binary named `ogham`. If a user installs the Go
# CLI on a machine that already has the Python binary on PATH, the
# resulting shell-typed `ogham` will be ambiguous and depend on PATH
# order. Refuse to install in that case unless --force is passed; an
# in-place upgrade (existing ogham == our install target) proceeds
# without prompting.
EXISTING_OGHAM="$(command -v ogham 2>/dev/null || true)"
TARGET_PATH="${INSTALL_DIR%/}/ogham"
if [ -n "$EXISTING_OGHAM" ] && [ "$EXISTING_OGHAM" != "$TARGET_PATH" ] && [ "$FORCE" != "1" ]; then
  echo "==> An ogham binary is already on \$PATH:" >&2
  echo "      ${EXISTING_OGHAM}" >&2
  echo "    This is most likely the Python ogham-mcp package (separate product)." >&2
  echo "    Installing the Go CLI to ${TARGET_PATH} will create a name collision -- which one" >&2
  echo "    wins on the shell depends on PATH order and is easy to confuse." >&2
  echo "" >&2
  echo "    Options:" >&2
  echo "      1. Re-run with --force to install anyway (you'll manage PATH order yourself)." >&2
  echo "      2. Re-run with --install-dir=<path> to install somewhere off PATH (e.g. ~/tools/bin/)." >&2
  echo "      3. Uninstall the Python ogham-mcp first if you don't need it." >&2
  exit 1
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

if [ "$OS" = "windows" ]; then
  ASSET_NAME="ogham-cli-${OS}-${ARCH}.zip"
  BINARY_NAME="ogham.exe"
else
  ASSET_NAME="ogham-cli-${OS}-${ARCH}.tar.gz"
  BINARY_NAME="ogham"
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

if [ ! -f "${TMPDIR}/${BINARY_NAME}" ]; then
  echo "Extracted archive does not contain ${BINARY_NAME}." >&2
  echo "Contents:" >&2
  ls -1 "$TMPDIR" >&2
  exit 1
fi

echo "==> Installing to ${INSTALL_DIR}/${BINARY_NAME}..."
mv -f "${TMPDIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

# macOS-specific: the released binaries are not Apple-notarised, so a
# fresh download arrives quarantined and Gatekeeper refuses to launch it.
# Ad-hoc codesigning + `xattr -dr` is the well-known unblock; both are in
# the base macOS toolchain so no Xcode install is required.
if [ "$OS" = "darwin" ]; then
  if command -v codesign >/dev/null 2>&1; then
    echo "==> Ad-hoc signing binary..."
    codesign --force --sign - "${INSTALL_DIR}/${BINARY_NAME}" >/dev/null 2>&1 || \
      echo "    (codesign failed -- you may need to run it manually)" >&2
  fi
  echo "==> Removing quarantine attribute..."
  xattr -dr com.apple.quarantine "${INSTALL_DIR}/${BINARY_NAME}" 2>/dev/null || true
fi

echo "==> Done."
# `ogham version` is a subcommand, not a flag. Old --version probe is
# kept as a fallback in case future builds add the flag.
"${INSTALL_DIR}/${BINARY_NAME}" version 2>/dev/null \
  || "${INSTALL_DIR}/${BINARY_NAME}" --version 2>/dev/null \
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
