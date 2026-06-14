#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-help"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# --version
out="$(bin/wip --version)"
assert_eq "wip 0.2.0-dev" "$out" "--version output"

# --help exits 0 and mentions ask + provider.
out="$(bin/wip --help)"
assert_grep "^usage: wip " <(printf '%s\n' "$out") "/dev/stdin usage line present" || true
# assert_grep needs a file, so write to a tempfile.
tmpout="$(mktemp)"
trap 'rm -f "$tmpout"' EXIT
bin/wip --help >"$tmpout"
assert_grep "usage: wip " "$tmpout" "usage line"
assert_grep "ask " "$tmpout" "mentions ask"
assert_grep "provider " "$tmpout" "mentions provider"
assert_grep "WIP_LLM_BASE_URL" "$tmpout" "mentions provider env"

# `wip help` (no flag) exits 0 with the same output.
bin/wip help >"$tmpout"
assert_grep "usage: wip " "$tmpout" "help-verb usage line"

# `wip -h` exits 0.
set +e
bin/wip -h >/dev/null 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "-h exits 0"

# Bare `wip` (no verb) exits 2 with usage on stdout.
set +e
bin/wip >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "no verb -> exit 2"

test_summary
