#!/usr/bin/env bash
# Install GoQuark from the latest GitHub Release (no local Go build required).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ButterFuture/GoQuark/main/scripts/install.sh | bash
#   ./scripts/install.sh
#   ./scripts/install.sh --version v1.0.0
#   ./scripts/install.sh --dir ~/.local/bin
#   GOQUARK_INSTALL_DIR=/opt/bin ./scripts/install.sh
#
# Env:
#   GOQUARK_REPO          default ButterFuture/GoQuark
#   GOQUARK_INSTALL_DIR   default: first writable of ~/.local/bin, /usr/local/bin
#   GOQUARK_VERSION       e.g. v1.0.0 or 1.0.0 (default: latest)
set -euo pipefail

REPO="${GOQUARK_REPO:-ButterFuture/GoQuark}"
VERSION="${GOQUARK_VERSION:-}"
INSTALL_DIR="${GOQUARK_INSTALL_DIR:-}"
BIN_NAME="goquark"

usage() {
  cat <<'EOF'
Install GoQuark from GitHub Releases.

  ./scripts/install.sh [--version vX.Y.Z] [--dir DIR] [--help]

  --version   Release tag (default: latest)
  --dir       Install directory (default: ~/.local/bin or /usr/local/bin)
  --help      Show this help

One-liner:
  curl -fsSL https://raw.githubusercontent.com/ButterFuture/GoQuark/main/scripts/install.sh | bash
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version|-v) VERSION="${2:-}"; shift 2 ;;
    --dir|-d) INSTALL_DIR="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 1 ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: required command not found: $1" >&2
    exit 1
  }
}

need curl
# sha256sum or shasum
if command -v sha256sum >/dev/null 2>&1; then
  SHA_CMD=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  SHA_CMD=(shasum -a 256)
else
  echo "ERROR: need sha256sum or shasum" >&2
  exit 1
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7l|armv6l) arch=arm ;;
  *)
    echo "ERROR: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac
case "$os" in
  linux|darwin) ;;
  msys*|mingw*|cygwin*)
    os=windows
    ;;
  *)
    echo "ERROR: unsupported OS: $os (use Releases page for Windows .exe)" >&2
    exit 1
    ;;
esac

if [[ -z "$INSTALL_DIR" ]]; then
  if [[ -d "${HOME}/.local/bin" ]] || mkdir -p "${HOME}/.local/bin" 2>/dev/null; then
    INSTALL_DIR="${HOME}/.local/bin"
  elif [[ -w /usr/local/bin ]]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
else
  mkdir -p "$INSTALL_DIR"
fi

api="https://api.github.com/repos/${REPO}/releases"
if [[ -z "$VERSION" || "$VERSION" == "latest" ]]; then
  echo "==> fetching latest release from ${REPO} …"
  meta="$(curl -fsSL "${api}/latest")"
else
  tag="$VERSION"
  [[ "$tag" == v* ]] || tag="v${tag}"
  echo "==> fetching release ${tag} from ${REPO} …"
  meta="$(curl -fsSL "${api}/tags/${tag}")"
fi

tag="$(printf '%s' "$meta" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "$tag" ]]; then
  echo "ERROR: could not parse release tag (repo private? no releases?)" >&2
  exit 1
fi
ver="${tag#v}"

# Asset naming matches CI / release: goquark_<ver>_<os>_<arch>[.exe]
ext=""
[[ "$os" == "windows" ]] && ext=".exe"
asset="goquark_${ver}_${os}_${arch}${ext}"
sums="SHA256SUMS"

# Prefer browser_download_url from JSON (simple extract)
asset_url="$(printf '%s' "$meta" | tr ',' '\n' | sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*'"${asset}"'\)".*/\1/p' | head -1)"
if [[ -z "$asset_url" ]]; then
  # fallback: constructed URL
  asset_url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
fi
sums_url="https://github.com/${REPO}/releases/download/${tag}/${sums}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "==> download ${asset}"
if ! curl -fL --retry 3 -o "${tmp}/${asset}" "$asset_url"; then
  echo "ERROR: download failed: $asset_url" >&2
  echo "       Check https://github.com/${REPO}/releases for available assets." >&2
  exit 1
fi

echo "==> download ${sums} (checksum)"
if curl -fL --retry 3 -o "${tmp}/${sums}" "$sums_url" 2>/dev/null; then
  (
    cd "$tmp"
    # line may be "hash  name" or "hash *name"
    if grep -E "[[:space:]]${asset}\$" "$sums" >/dev/null 2>&1 || grep -F "$asset" "$sums" >/dev/null 2>&1; then
      # verify only our file
      expected="$(grep -F "$asset" "$sums" | head -1 | awk '{print $1}')"
      actual="$("${SHA_CMD[@]}" "$asset" | awk '{print $1}')"
      if [[ "$expected" != "$actual" ]]; then
        echo "ERROR: checksum mismatch for ${asset}" >&2
        echo "  expected: $expected" >&2
        echo "  actual:   $actual" >&2
        exit 1
      fi
      echo "==> checksum OK"
    else
      echo "WARN: ${asset} not listed in SHA256SUMS — skip verify" >&2
    fi
  )
else
  echo "WARN: SHA256SUMS not found — skip verify" >&2
fi

dest="${INSTALL_DIR}/${BIN_NAME}"
[[ "$os" == "windows" ]] && dest="${INSTALL_DIR}/${BIN_NAME}.exe"
chmod +x "${tmp}/${asset}"
# replace atomically
cp "${tmp}/${asset}" "${dest}.new"
mv -f "${dest}.new" "$dest"
chmod +x "$dest"

echo
echo "Installed: $dest"
echo "Version:   ${tag}"
if ! command -v "$BIN_NAME" >/dev/null 2>&1; then
  echo
  echo "Note: ${INSTALL_DIR} is not on your PATH. Add:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
echo
echo "Try:"
echo "  goquark version"
echo "  goquark login"
echo "  goquark tui"
