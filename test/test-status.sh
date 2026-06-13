#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="status"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  solo: { enabled: true }
current_initiative: demo
initiatives:
  - slug: demo
    title: Demo
    status: in-flight
    active_step: step-02
    brief: .wip/initiatives/demo/BRIEF.md
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
- **step-03 — Third** — later.
MD

out="$(WIP_ROOT="$tmp" bin/wip-plumbing status)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.initiative' <<<"$out")" "initiative"
assert_eq "in-flight" "$(jq -r '.status' <<<"$out")" "status field"
assert_eq "1" "$(jq -r '.round.n' <<<"$out")" "round 1"
assert_eq "One" "$(jq -r '.round.title' <<<"$out")" "round title"
assert_eq "step-02" "$(jq -r '.active_step.id' <<<"$out")" "active step id"
assert_eq "Second" "$(jq -r '.active_step.title' <<<"$out")" "active step title"
assert_eq "false" "$(jq -r '.active_step.shipped' <<<"$out")" "active step not shipped"
assert_eq "true" "$(jq -r '.solo_available' <<<"$out")" "solo available"
assert_eq "0" "$(jq -r '.signals | length' <<<"$out")" "no signals"

# --initiative <unknown> -> exit 3.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing status --initiative bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"

# Manifest active_step names a shipped step -> signal manifest-step-ahead.
tmp2="$(mktemp -d)"
mkdir -p "$tmp2/.wip/initiatives/demo"
sed 's/active_step: step-02/active_step: step-01/' "$tmp/.wip.yaml" >"$tmp2/.wip.yaml"
cp "$tmp/.wip/initiatives/demo/roadmap.md" "$tmp2/.wip/initiatives/demo/roadmap.md"
out2="$(WIP_ROOT="$tmp2" bin/wip-plumbing status)"
assert_eq "1" "$(jq -r '.signals | map(select(. == "manifest-step-ahead")) | length' <<<"$out2")" "manifest-step-ahead signal"
rm -rf "$tmp2"

# No current_initiative + no --initiative -> exit 3.
tmp3="$(mktemp -d)"
cat >"$tmp3/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
initiatives: []
YAML
set +e
WIP_ROOT="$tmp3" bin/wip-plumbing status >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "no initiative -> exit 3"
rm -rf "$tmp3"

# solo disabled -> solo_available false.
tmp4="$(mktemp -d)"
mkdir -p "$tmp4/.wip/initiatives/demo"
sed 's/solo: { enabled: true }/solo: { enabled: false }/' "$tmp/.wip.yaml" >"$tmp4/.wip.yaml"
cp "$tmp/.wip/initiatives/demo/roadmap.md" "$tmp4/.wip/initiatives/demo/roadmap.md"
out4="$(WIP_ROOT="$tmp4" bin/wip-plumbing status)"
assert_eq "false" "$(jq -r '.solo_available' <<<"$out4")" "solo disabled -> false"
rm -rf "$tmp4"

# dirty_wip_files is an array (empty on a non-git tree).
assert_eq "array" "$(jq -r '.dirty_wip_files | type' <<<"$out")" "dirty_wip_files array"

test_summary
