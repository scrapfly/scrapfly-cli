#!/usr/bin/env sh
#
# Scrapfly CLI installer.
#
# Usage:
#   curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh
#   curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh -s -- --version v0.1.0 --prefix /usr/local
#   curl -fsSL https://scrapfly.io/scrapfly-cli/install | SCRAPFLY_VERSION=v0.1.0 sh
#
# Flags / env:
#   --version <tag>      (or $SCRAPFLY_VERSION) pin a release. Default: latest.
#   --prefix <dir>       install dir for the binary. Default: /usr/local/bin
#                        (falls back to $HOME/.local/bin if not writable).
#   --dest   <path>      explicit file path for the binary (overrides --prefix).
#   --repo   <org/repo>  override the source repo. Default: scrapfly/scrapfly-cli.
#
# Artifacts expected in the release:
#   scrapfly-macos-amd64    (also arm64)
#   scrapfly-linux-amd64    (also arm64)
#   scrapfly-windows-amd64.exe (also arm64; manual install on Windows)

set -eu

REPO="${REPO:-scrapfly/scrapfly-cli}"
VERSION="${SCRAPFLY_VERSION:-}"
PREFIX="${PREFIX:-/usr/local/bin}"
DEST=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --prefix)  PREFIX="$2"; shift 2 ;;
    --dest)    DEST="$2"; shift 2 ;;
    --repo)    REPO="$2"; shift 2 ;;
    -h|--help)
      sed -n '3,22p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fatal() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

raw_os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fatal "unsupported CPU: $arch" ;;
esac

case "$raw_os" in
  darwin) os=macos  asset="scrapfly-macos-${arch}" ;;
  linux)  os=linux  asset="scrapfly-linux-${arch}" ;;
  *)      fatal "unsupported OS: $raw_os (Windows users: download scrapfly-windows-*.exe from GitHub Releases)" ;;
esac

if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
  VERSION="latest"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
fi

info "target: $os-$arch ($asset)"
info "source: $url"

# Resolve final path. --dest wins; otherwise fall back from --prefix to
# $HOME/.local/bin when the prefix isn't writable.
if [ -z "$DEST" ]; then
  DEST="$PREFIX/scrapfly"
  if ! ( [ -w "$PREFIX" ] || [ -w "$(dirname "$PREFIX")" ] ); then
    fallback="$HOME/.local/bin"
    info "$PREFIX not writable, falling back to $fallback"
    mkdir -p "$fallback"
    DEST="$fallback/scrapfly"
  fi
fi
mkdir -p "$(dirname "$DEST")"

if command -v curl >/dev/null 2>&1; then
  curl -fSL "$url" -o "$DEST"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$DEST" "$url"
else
  fatal "need curl or wget"
fi
chmod 0755 "$DEST"

# macOS Gatekeeper: strip the quarantine attribute we inherit from the curl
# download. Until the binary is notarized by an Apple Developer ID, this is
# what lets `./scrapfly` run without a right-click-Open dance.
if [ "$os" = "macos" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$DEST" >/dev/null 2>&1 || true
fi

info "installed: $DEST"
"$DEST" version || true

case "$PATH" in
  *"$(dirname "$DEST")"*) ;;
  *) info "note: $(dirname "$DEST") is not on your PATH; add it to start using scrapfly." ;;
esac
