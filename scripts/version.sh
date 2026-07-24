#!/usr/bin/env bash
# Print project version from VERSION file (single source of truth).
# Usage: ./scripts/version.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
v="$(tr -d '[:space:]' <"$ROOT/VERSION")"
if [[ -z "$v" ]]; then
  echo "VERSION file empty" >&2
  exit 1
fi
# strip optional leading v
v="${v#v}"
echo "$v"
