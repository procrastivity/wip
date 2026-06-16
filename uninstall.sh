#!/usr/bin/env bash
# uninstall.sh - uninstall wip from PREFIX (default /usr/local)
set -euo pipefail

PREFIX="${1:-/usr/local}"
# shellcheck disable=SC2034
SRC="$(cd "$(dirname "$0")" && pwd)"

rm -f "$PREFIX/bin/wip"
rm -f "$PREFIX/bin/wip-plumbing"
rm -rf "$PREFIX/lib/wip"
rm -rf "$PREFIX/share/wip"

echo "Uninstalled wip from $PREFIX"
