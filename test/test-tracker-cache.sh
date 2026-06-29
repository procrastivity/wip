#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-cache"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-03 (BRIEF §3 / ADR-0019 §A): the lifecycle-state cache (the durable
# headless floor) + its cached-vs-live surfacing in `status`. The cache lib is
# exercised directly; the status surfacing through `bin/wip-plumbing status`.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash

# --- cache lib direct -------------------------------------------------------
tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip"
assert_eq "{}" "$(_wip_tracker_cache_read "$tmp")" "absent file reads as {}"
assert_eq "null" "$(_wip_tracker_cache_get "$tmp" "demo/step-02")" "absent entry -> null"

set_out="$(_wip_tracker_cache_set "$tmp" "demo/step-02" "in-review" "ship" "2026-06-28")"
assert_eq "in-review" "$(jq -r '.state' <<<"$set_out")" "set echoes state"
assert_eq "ship" "$(jq -r '.reason' <<<"$set_out")" "set echoes reason"
assert_eq "in-review" "$(jq -r '.state' <<<"$(_wip_tracker_cache_get "$tmp" "demo/step-02")")" \
  "get returns the set state"

# Upsert overwrites in place; other keys are preserved.
_wip_tracker_cache_set "$tmp" "demo/step-03" "in-progress" "start" "2026-06-29" >/dev/null
_wip_tracker_cache_set "$tmp" "demo/step-02" "done" "review-complete" "2026-06-30" >/dev/null
assert_eq "done" "$(jq -r '.state' <<<"$(_wip_tracker_cache_get "$tmp" "demo/step-02")")" \
  "upsert overwrites step-02"
assert_eq "in-progress" "$(jq -r '.state' <<<"$(_wip_tracker_cache_get "$tmp" "demo/step-03")")" \
  "upsert preserves step-03"
assert_eq "2" "$(_wip_tracker_cache_read "$tmp" | jq 'length')" "two entries cached"

# A corrupt cache file never crashes a read (degrades to {}).
printf 'not json{{{' >"$(_wip_tracker_cache_path "$tmp")"
assert_eq "{}" "$(_wip_tracker_cache_read "$tmp")" "corrupt cache -> {} (no crash)"
assert_eq "null" "$(_wip_tracker_cache_get "$tmp" "demo/step-02")" "corrupt cache get -> null"

# --- status surfacing -------------------------------------------------------
fx="$(wip_mktemp)"
mkdir -p "$fx/.wip/initiatives/demo"
cat >"$fx/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  issue-tracker: { enabled: true, backend: linear }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-02
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-02 — Second** — current.\n' \
  >"$fx/.wip/initiatives/demo/roadmap.md"

# No cache yet -> tracker_available true (declared), tracker_state null.
s1="$(WIP_ROOT="$fx" bin/wip-plumbing status)"
assert_eq "true" "$(jq -r '.tracker_available' <<<"$s1")" "issue-tracker declared -> tracker_available true"
assert_eq "null" "$(jq -r '.tracker_state' <<<"$s1")" "no cache entry -> tracker_state null"

# Seed the cache for the active node -> status surfaces it.
_wip_tracker_cache_set "$fx" "demo/step-02" "in-review" "ship" "2026-06-28" >/dev/null
s2="$(WIP_ROOT="$fx" bin/wip-plumbing status)"
assert_eq "in-review" "$(jq -r '.tracker_state.state' <<<"$s2")" "status reads cached state for active node"
assert_eq "ship" "$(jq -r '.tracker_state.reason' <<<"$s2")" "status reads cached reason"

# issue-tracker not declared -> tracker_available false.
WIP_ROOT="$fx" yq -i 'del(.features."issue-tracker")' "$fx/.wip.yaml"
s3="$(WIP_ROOT="$fx" bin/wip-plumbing status)"
assert_eq "false" "$(jq -r '.tracker_available' <<<"$s3")" "no issue-tracker -> tracker_available false"

test_summary
