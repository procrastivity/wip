#!/usr/bin/env bash
# install.sh - install wip to PREFIX (default /usr/local)
set -euo pipefail

PREFIX="${1:-/usr/local}"
SRC="$(cd "$(dirname "$0")" && pwd)"

echo "Installing wip to $PREFIX ..."

install -d "$PREFIX/bin" "$PREFIX/lib/wip" "$PREFIX/share/wip"

install -m755 "$SRC/bin/wip" "$PREFIX/bin/wip"
install -m755 "$SRC/bin/wip-plumbing" "$PREFIX/bin/wip-plumbing"

# Drop stale files from a prior install before re-copying.
rm -rf "$PREFIX/lib/wip"
install -d "$PREFIX/lib/wip"
cp -R "$SRC/lib/wip/." "$PREFIX/lib/wip/"

# Drop stale files from a prior install before re-copying.
rm -rf \
  "$PREFIX/share/wip/.claude-plugin" \
  "$PREFIX/share/wip/commands" \
  "$PREFIX/share/wip/agents"
cp -R "$SRC/.claude-plugin" "$PREFIX/share/wip/"
cp -R "$SRC/commands" "$PREFIX/share/wip/"
cp -R "$SRC/agents" "$PREFIX/share/wip/"

install -m644 "$SRC/README.md" "$PREFIX/share/wip/README.md"

echo "Installed wip to $PREFIX"
echo "  Binary: $PREFIX/bin/wip"
echo "  Plugin: $PREFIX/share/wip/.claude-plugin"
echo ""
echo "Add the plugin via:"
echo "  claude plugin install $PREFIX/share/wip"
echo ""
echo "Uninstall with:"
echo "  $SRC/uninstall.sh $PREFIX"
echo ""
"$PREFIX/bin/wip" --version || true
