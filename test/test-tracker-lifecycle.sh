#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-lifecycle"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-04 (ADR-0019 §A): Tier-0 lifecycle intent emission at the boundaries.
# `workplan init --activate` emits {to:in-progress, reason:start}; `ship` emits
# {to:in-review, reason:ship} — both into the cache floor, headless (no forge, no
# transport). Gated on issue-tracker enabled; suppressed under forge stand-down
# (one writer) and under --dry-run (no cache write).

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-28

mkfx() { # mkfx <dir> [--forge] [--no-tracker]
  local dir="$1" forge="" tracker=1
  shift
  while (($#)); do
    case "$1" in
      --forge) forge=1 ;;
      --no-tracker) tracker=0 ;;
    esac
    shift
  done
  mkdir -p "$dir/.wip/initiatives/demo"
  {
    printf 'version: 1\nfeatures:\n  wip: { enabled: true, root: .wip }\n'
    ((tracker)) && printf '  issue-tracker: { enabled: true, backend: linear }\n'
    [[ -n "$forge" ]] && printf '  forge: { enabled: true }\n'
    printf 'current_initiative: demo\ninitiatives:\n  - slug: demo\n    status: in-flight\n'
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
  } >"$dir/.wip.yaml"
  printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — current. [tracker: BDS-90]\n' \
    >"$dir/.wip/initiatives/demo/roadmap.md"
}
cache_state() { jq -r --arg k "demo/step-01" '.[$k].state // "ABSENT"' "$1/.wip/tracker-cache.json" 2>/dev/null || printf 'NOFILE'; }

WIP=bin/wip-plumbing

# --- start boundary: activate -> in-progress --------------------------------
tmp="$(wip_mktemp)"
mkfx "$tmp"
a="$(WIP_ROOT="$tmp" $WIP workplan init demo step-01 --activate)"
assert_eq "in-progress" "$(jq -r '.intent.to' <<<"$a")" "activate -> intent to in-progress"
assert_eq "start" "$(jq -r '.intent.reason' <<<"$a")" "activate -> reason start"
assert_eq "demo/step-01" "$(jq -r '.intent.node' <<<"$a")" "activate -> node key"
assert_eq "in-progress" "$(cache_state "$tmp")" "activate writes cache floor"

# --- ship boundary: in-review (state advances over in-progress) -------------
sh="$(WIP_ROOT="$tmp" $WIP ship demo step-01)"
assert_eq "in-review" "$(jq -r '.intent.to' <<<"$sh")" "ship -> intent to in-review"
assert_eq "ship" "$(jq -r '.intent.reason' <<<"$sh")" "ship -> reason ship"
assert_eq "in-review" "$(jq -r '.transition' <<<"$sh")" "ship transition in-review"
assert_eq "in-review" "$(cache_state "$tmp")" "ship advances cache floor to in-review"

# --- forge stand-down suppresses the ship intent (one writer) ---------------
tmpF="$(wip_mktemp)"
mkfx "$tmpF" --forge
shF="$(WIP_ROOT="$tmpF" WIP_FORGE_STATUS_CMD=true $WIP ship demo step-01)"
assert_eq "stood-down" "$(jq -r '.transition' <<<"$shF")" "forge -> transition stood-down"
assert_eq "null" "$(jq -r '.intent // null' <<<"$shF")" "forge stand-down -> no ship intent emitted"
assert_eq "NOFILE" "$(cache_state "$tmpF")" "forge stand-down -> ship writes no cache"

# --- issue-tracker disabled -> no intent at either boundary -----------------
tmpN="$(wip_mktemp)"
mkfx "$tmpN" --no-tracker
aN="$(WIP_ROOT="$tmpN" $WIP workplan init demo step-01 --activate)"
assert_eq "null" "$(jq -r '.intent // null' <<<"$aN")" "no issue-tracker -> activate emits no intent"
shN="$(WIP_ROOT="$tmpN" $WIP ship demo step-01)"
assert_eq "null" "$(jq -r '.intent // null' <<<"$shN")" "no issue-tracker -> ship emits no intent"
assert_eq "NOFILE" "$(cache_state "$tmpN")" "no issue-tracker -> no cache written"

# --- dry-run: intent in ledger, but cache NOT written -----------------------
tmpD="$(wip_mktemp)"
mkfx "$tmpD"
aD="$(WIP_ROOT="$tmpD" $WIP --dry-run workplan init demo step-01 --activate)"
assert_eq "in-progress" "$(jq -r '.intent.to' <<<"$aD")" "dry-run activate still reports intent"
assert_eq "NOFILE" "$(cache_state "$tmpD")" "dry-run activate writes no cache"
shD="$(WIP_ROOT="$tmpD" $WIP ship demo step-01 --dry-run)"
assert_eq "in-review" "$(jq -r '.intent.to' <<<"$shD")" "dry-run ship still reports intent"
assert_eq "NOFILE" "$(cache_state "$tmpD")" "dry-run ship writes no cache"

test_summary
