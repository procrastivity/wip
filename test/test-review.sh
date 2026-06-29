#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="review"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-05 (ADR-0019 §A/§D): the `review` surface. `review list` shows In-Review
# nodes from the cache floor (scoped per initiative); `review complete <node>`
# is the manual Done gate (emits {to:done, reason:review-complete}). The cache is
# seeded directly via the lib for deterministic state.

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-28
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
WIP=bin/wip-plumbing

tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
  - slug: other
    status: in-flight
    roadmap: .wip/initiatives/other/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x.\n' \
  >"$tmp/.wip/initiatives/demo/roadmap.md"

# Seed: demo/step-01 in-review, demo/step-02 in-progress, other/step-01 in-review.
_wip_tracker_cache_set "$tmp" "demo/step-01" "in-review" "ship" "2026-06-20" >/dev/null
_wip_tracker_cache_set "$tmp" "demo/step-02" "in-progress" "start" "2026-06-21" >/dev/null
_wip_tracker_cache_set "$tmp" "other/step-01" "in-review" "ship" "2026-06-22" >/dev/null

# --- review list: scoped, in-review only ------------------------------------
l="$(WIP_ROOT="$tmp" $WIP review list)"
assert_eq "demo" "$(jq -r '.initiative' <<<"$l")" "list: initiative echo"
assert_eq "1" "$(jq -r '.in_review | length' <<<"$l")" "list: only one in-review node for demo"
assert_eq "demo/step-01" "$(jq -r '.in_review[0].node' <<<"$l")" "list: the in-review node"
assert_eq "in-review" "$(jq -r '.in_review[0].state' <<<"$l")" "list: state field"
assert_eq "ship" "$(jq -r '.in_review[0].reason' <<<"$l")" "list: reason field"
# other/step-01 (in-review in a DIFFERENT initiative) must not leak in.
assert_eq "0" "$(jq -r '[.in_review[] | select(.node | startswith("other/"))] | length' <<<"$l")" \
  "list: other initiative's in-review does not leak"

# Explicit --initiative scopes to that initiative.
lo="$(WIP_ROOT="$tmp" $WIP review list --initiative other)"
assert_eq "other/step-01" "$(jq -r '.in_review[0].node' <<<"$lo")" "list --initiative other scopes correctly"

# --- review complete: manual Done gate --------------------------------------
c="$(WIP_ROOT="$tmp" $WIP review complete step-01)"
assert_eq "demo/step-01" "$(jq -r '.node' <<<"$c")" "complete: node echo"
assert_eq "done" "$(jq -r '.intent.to' <<<"$c")" "complete: intent to done"
assert_eq "review-complete" "$(jq -r '.intent.reason' <<<"$c")" "complete: reason review-complete"
assert_eq "true" "$(jq -r '.was_in_review' <<<"$c")" "complete: was_in_review true (it was)"
assert_eq "done" "$(jq -r '.["demo/step-01"].state' "$tmp/.wip/tracker-cache.json")" \
  "complete advances cache to done"

# After completion it drops out of the in-review list.
assert_eq "0" "$(jq -r '.in_review | length' <<<"$(WIP_ROOT="$tmp" $WIP review list)")" \
  "completed node leaves the in-review list"

# Completing a node that was NOT in-review still advances, but flags it.
c2="$(WIP_ROOT="$tmp" $WIP review complete step-02)"
assert_eq "false" "$(jq -r '.was_in_review' <<<"$c2")" "complete: was_in_review false for a non-in-review node"
assert_eq "done" "$(jq -r '.intent.to' <<<"$c2")" "complete: still advances to done"

# --- dry-run: reports intent, writes nothing --------------------------------
_wip_tracker_cache_set "$tmp" "demo/step-03" "in-review" "ship" "2026-06-23" >/dev/null
cd="$(WIP_ROOT="$tmp" $WIP --dry-run review complete step-03)"
assert_eq "done" "$(jq -r '.intent.to' <<<"$cd")" "dry-run: reports the done intent"
assert_eq "in-review" "$(jq -r '.["demo/step-03"].state' "$tmp/.wip/tracker-cache.json")" \
  "dry-run: cache unchanged"

# --- error envelopes --------------------------------------------------------
set +e
WIP_ROOT="$tmp" $WIP review >/dev/null 2>&1
assert_eq "2" "$?" "no subcommand -> exit 2"
WIP_ROOT="$tmp" $WIP review bogus >/dev/null 2>&1
assert_eq "2" "$?" "unknown subcommand -> exit 2"
WIP_ROOT="$tmp" $WIP review complete >/dev/null 2>&1
assert_eq "2" "$?" "complete without node -> exit 2"
WIP_ROOT="$tmp" $WIP review list --initiative nope >/dev/null 2>&1
assert_eq "3" "$?" "unknown initiative -> exit 3"
set -e

test_summary
