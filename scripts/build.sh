#!/usr/bin/env bash
# Build goquark with version injected from VERSION file (not hardcoded in source).
# Usage:
#   ./scripts/build.sh                  # ./bin/goquark for host
#   ./scripts/build.sh linux amd64      # cross → dist/goquark-linux-amd64
#   OUT=/tmp/gq ./scripts/build.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH" >&2
  exit 1
fi

VERSION="$("$ROOT/scripts/version.sh")"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

if [[ $# -ge 2 ]]; then
  GOOS="$1"
  GOARCH="$2"
  OUT="${OUT:-$ROOT/dist/goquark-${GOOS}-${GOARCH}}"
else
  GOOS="$(go env GOOS)"
  GOARCH="$(go env GOARCH)"
  OUT="${OUT:-$ROOT/bin/goquark}"
fi

mkdir -p "$(dirname "$OUT")"

LDFLAGS=(
  -s -w
  -X "main.Version=${VERSION}"
  -X "main.Commit=${COMMIT}"
  -X "main.BuildDate=${DATE}"
)

echo "building goquark ${VERSION} (${COMMIT}) → ${OUT}  GOOS=${GOOS} GOARCH=${GOARCH}"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -ldflags "${LDFLAGS[*]}" -o "$OUT" ./cmd/goquark

echo "ok: $OUT"
# only run version if host-native binary
if [[ "$GOOS" == "$(go env GOOS)" && "$GOARCH" == "$(go env GOARCH)" ]]; then
  "$OUT" version || true
fi
