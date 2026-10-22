#!/usr/bin/env bash
# Build goquarkdemo: same product binary with mock drive listing for screenshots.
# Requires: go build -tags demo
#
# Usage:
#   ./scripts/build-demo.sh                  # → ./bin/goquarkdemo
#   OUT=/tmp/goquarkdemo ./scripts/build-demo.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH" >&2
  exit 1
fi

VERSION="$("$ROOT/scripts/version.sh")-demo"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OUT="${OUT:-$ROOT/bin/goquarkdemo}"

mkdir -p "$(dirname "$OUT")"

LDFLAGS=(
  -s -w
  -X "main.Version=${VERSION}"
  -X "main.Commit=${COMMIT}"
  -X "main.BuildDate=${DATE}"
)

echo "building goquarkdemo ${VERSION} (${COMMIT}) → ${OUT}  [-tags demo]"
CGO_ENABLED=0 \
  go build -tags demo -trimpath -ldflags "${LDFLAGS[*]}" -o "$OUT" ./cmd/goquark

echo "ok: $OUT"
if [[ "$(go env GOOS)" == "$(go env GOOS)" ]]; then
  "$OUT" version || true
fi
