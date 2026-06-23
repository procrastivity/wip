#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="scaffold"
# shellcheck source=test/helpers.sh
source test/helpers.sh
# shellcheck source=lib/wip/wip-plumbing-scaffold-lib.bash
source lib/wip/wip-plumbing-scaffold-lib.bash

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

tmpl="$tmp/t.tmpl"
printf 'before {{title}} after\n' >"$tmpl"

# 1. plain value substitutes normally.
out="$(wip_scaffold_render "$tmpl" title="Hello World")"
assert_eq "before Hello World after" "$out" "plain value"

# 2. value containing & is preserved verbatim (not replaced by the matched
#    placeholder). This is the core HANDOFF-scaffold-render-sed-escaping bug.
out="$(wip_scaffold_render "$tmpl" title="A & B")"
assert_eq "before A & B after" "$out" "ampersand preserved"

# 3. leading/embedded ampersand.
out="$(wip_scaffold_render "$tmpl" title="& leading")"
assert_eq "before & leading after" "$out" "leading ampersand preserved"

# 4. backslashes preserved verbatim.
out="$(wip_scaffold_render "$tmpl" title='path\to\thing')"
assert_eq 'before path\to\thing after' "$out" "backslash preserved"

# 5. a realistic title with an ampersand (the reported xcind case).
printf '# {{title}} — BRIEF\n' >"$tmpl"
out="$(wip_scaffold_render "$tmpl" title="Document xcind TLS proxy-domain wildcard behavior & local trust")"
assert_eq "# Document xcind TLS proxy-domain wildcard behavior & local trust — BRIEF" "$out" "realistic & title"

# 6. forward slash in value survives (the 0x1f delimiter's reason for being).
printf 'path: {{p}}\n' >"$tmpl"
out="$(wip_scaffold_render "$tmpl" p="a/b/c")"
assert_eq "path: a/b/c" "$out" "slash survives"

# 7. a value containing the 0x1f delimiter byte is rejected (return 1, no
#    corrupted output).
printf '{{v}}\n' >"$tmpl"
set +e
out="$(wip_scaffold_render "$tmpl" "v=x"$'\037'"y" 2>/dev/null)"
rc=$?
set -e
assert_eq "1" "$rc" "0x1f value rejected (exit 1)"

# 8. a value containing a newline is rejected.
set +e
out="$(wip_scaffold_render "$tmpl" "v=line1"$'\n'"line2" 2>/dev/null)"
rc=$?
set -e
assert_eq "1" "$rc" "newline value rejected (exit 1)"

# 9. multiple keys, one with an ampersand.
printf '{{a}} and {{b}}\n' >"$tmpl"
out="$(wip_scaffold_render "$tmpl" a="R&D" b="Q&A")"
assert_eq "R&D and Q&A" "$out" "multi-key ampersands"

test_summary
