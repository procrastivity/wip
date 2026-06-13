#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="intake-validate"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# 1. valid file (H1 + Goal).
cat >"$tmp/good.md" <<'MD'
# Auth Rework

## Goal

Move auth from session-tokens to short-lived bearer tokens.
MD
out="$(bin/wip-plumbing intake validate "$tmp/good.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "good file ok"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "good file valid"
assert_eq "0" "$(jq -r '.missing|length' <<<"$out")" "good file no missing"

# 2. Summary acceptable in place of Goal.
cat >"$tmp/summary.md" <<'MD'
# Title

## Summary

Body.
MD
out2="$(bin/wip-plumbing intake validate "$tmp/summary.md")"
assert_eq "true" "$(jq -r '.valid' <<<"$out2")" "summary heading valid"

# 3. Missing H1 -> exit 4, missing[]=["title"].
cat >"$tmp/no-title.md" <<'MD'
## Goal

No H1 here.
MD
set +e
out3="$(bin/wip-plumbing intake validate "$tmp/no-title.md")"
rc=$?
set -e
assert_eq "4" "$rc" "no-title exit 4"
assert_eq "false" "$(jq -r '.valid' <<<"$out3")" "no-title invalid"
assert_eq "title" "$(jq -r '.missing[0]' <<<"$out3")" "no-title missing[0]"

# 4. Missing Goal/Summary -> exit 4.
cat >"$tmp/no-goal.md" <<'MD'
# Title

## Background

Words.
MD
set +e
out4="$(bin/wip-plumbing intake validate "$tmp/no-goal.md")"
rc=$?
set -e
assert_eq "4" "$rc" "no-goal exit 4"
assert_eq "goal-or-summary-section" "$(jq -r '.missing[0]' <<<"$out4")" "no-goal missing[0]"

# 5. Non-existent file -> exit 2.
set +e
bin/wip-plumbing intake validate "$tmp/missing.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing file exit 2"

# 6. No file -> exit 2.
set +e
bin/wip-plumbing intake validate >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "no file exit 2"

# 7. `intake classify` / `intake apply` -> exit 2 (deferred to step-07.5).
set +e
bin/wip-plumbing intake classify "$tmp/good.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "classify deferred exit 2"

set +e
bin/wip-plumbing intake apply "$tmp/good.md" --kind brief >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "apply deferred exit 2"

# 8. --kind flag rejected in v0.
set +e
bin/wip-plumbing intake validate "$tmp/good.md" --kind brief >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "--kind deferred exit 2"

test_summary
