#!/usr/bin/env bash
#
# ogham-cli install script.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash
#   curl -sSL https://raw.githubusercontent.com/ogham-mcp/ogham-cli/main/install.sh | bash -s -- --version v0.7.0
#   INSTALL_DIR=/usr/local/bin curl -sSL https://.../install.sh | bash
#
# What it does:
#   1. Detects platform (darwin / linux / windows) and arch (amd64 / arm64)
#   2. Downloads the matching release tarball/zip from GitHub
#   3. Extracts the binary into $INSTALL_DIR (default ~/.local/bin)
#   4. On macOS: ad-hoc codesigns + removes com.apple.quarantine so Gatekeeper
#      stops blocking the unnotarized binary
#   5. Prints `ogham --version` so the install is self-verifying
#
# No `gh` CLI required -- uses plain curl against github.com release assets.

set -euo pipefail

REPO="ogham-mcp/ogham-cli"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
FORCE="${FORCE:-0}"

# --version <tag> overrides the default of "latest". Useful for pinning a
# specific release in CI or when you need to roll back.
# --force overrides the PATH-collision check (see below) so an upgrade
# over an existing install always proceeds without a prompt.
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
    -h|--help)
      sed -n '2,18p' "$0"
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
if [ "$VERSION" = "latest" ]; then
  DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"
  TAG_LABEL="latest"
else
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET_NAME}"
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
