#!/bin/sh
# 1master installer — detects OS+arch and installs /usr/local/bin/1master.
#
# Usage:
#   curl -fsSL https://1master.uz/install.sh | sh
#   curl -fsSL https://1master.uz/install.sh | sh -s -- --version 0.1.0
#
# Env vars:
#   ONEMASTER_BASE     base URL for downloads (default: https://1master.uz/dl)
#   ONEMASTER_PREFIX   install dir            (default: /usr/local/bin)
#   ONEMASTER_VERSION  version to install     (default: latest)

set -eu

BASE="${ONEMASTER_BASE:-https://1master.uz/dl}"
PREFIX="${ONEMASTER_PREFIX:-/usr/local/bin}"
VERSION="${ONEMASTER_VERSION:-latest}"

# Allow --version flag for convenience.
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --prefix)  PREFIX="$2";  shift 2 ;;
    --base)    BASE="$2";    shift 2 ;;
    *) echo "Unknown flag: $1" >&2; exit 2 ;;
  esac
done

# ---- detect OS ----
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux)  OS="linux"  ;;
  darwin) OS="darwin" ;;
  *) echo "❌ Unsupported OS: $os (only linux and darwin are supported)" >&2; exit 1 ;;
esac

# ---- detect arch ----
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)        ARCH="amd64" ;;
  arm64|aarch64)       ARCH="arm64" ;;
  *) echo "❌ Unsupported architecture: $arch (only amd64 and arm64 are supported)" >&2; exit 1 ;;
esac

# ---- pick URL ----
if [ "$VERSION" = "latest" ]; then
  URL="$BASE/1master-$OS-$ARCH"
else
  URL="$BASE/v$VERSION/1master-$OS-$ARCH"
fi

# ---- pick downloader ----
if command -v curl >/dev/null 2>&1; then
  DOWNLOAD="curl -fsSL --retry 3"
elif command -v wget >/dev/null 2>&1; then
  DOWNLOAD="wget -q -O-"
else
  echo "❌ Need curl or wget to download." >&2
  exit 1
fi

# ---- pick sudo if we can't write the prefix ----
SUDO=""
if [ ! -w "$PREFIX" ]; then
  if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
      SUDO="sudo"
    else
      echo "❌ $PREFIX is not writable and sudo is not available." >&2
      echo "   Re-run with: ONEMASTER_PREFIX=\$HOME/.local/bin $0" >&2
      exit 1
    fi
  fi
fi

echo "→ Downloading 1master ($OS/$ARCH, $VERSION)"
echo "  from $URL"

TMP="$(mktemp -t 1master.XXXXXX)"
trap 'rm -f "$TMP"' EXIT INT TERM

if ! $DOWNLOAD "$URL" > "$TMP"; then
  echo "❌ Download failed." >&2
  exit 1
fi

# Sanity-check: the binary should be at least a few hundred KB.
size=$(wc -c < "$TMP")
if [ "$size" -lt 100000 ]; then
  echo "❌ Downloaded file is suspiciously small ($size bytes). Aborting." >&2
  head -c 500 "$TMP" >&2
  echo "" >&2
  exit 1
fi

$SUDO install -m 0755 "$TMP" "$PREFIX/1master"

echo ""
echo "✅ Installed 1master → $PREFIX/1master"
echo ""
echo "Next steps:"
echo "  1master auth <your-token>      # paste token from 1master.uz/dashboard"
echo "  1master http 3000              # expose local port → <your-username>.1master.uz"
echo ""
"$PREFIX/1master" version 2>/dev/null || true
