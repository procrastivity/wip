#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-skeleton"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: this test covers the `ship` verb PLUMBING only — dispatch, argparse,
# error codes (exit 2/3/4), ledger field PRESENCE/SHAPE, and --dry-run
# threading. It is deliberately WRITER-AGNOSTIC so it stays green both with
# the step-01 inert stubs in place AND after the real writers land in
# step-02 (roadmap marker) / step-03 (active_step clear).
#
# It does NOT assert real writer behavior, status aggregation, or end-to-end
# idempotency — those are owned by the per-lane tests
# (test-ship-roadmap-writer.sh / test-ship-manifest-writer.sh) and step-04's
# end-to-end. See ADR-0016 (engineering/decisions/0016-closeout-write-contract.md)
# and the step-03 escalation (ledger todo 379, comment 322).
# ---------------------------------------------------------------------------

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-27

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-02
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
MD

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# in_set <value> <allowed...> -> echoes "yes" if value is one of the allowed
# words, else "no". Lets us assert membership in a status set without coupling
# to a specific value (no assert_match helper exists in helpers.sh).
in_set() {
  local v="$1"
  shift
  local w
  for w in "$@"; do
    [[ "$v" == "$w" ]] && {
      echo yes
      return
    }
  done
  echo no
}

# 1. Dispatch + happy path: `ship` routes to wip_plumbing_cmd_ship and emits the
#    locked ledger. Structural asserts only — field PRESENCE/SHAPE, never exact
#    writer values (those depend on the real step-02/03 writers).
out="$(run demo step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.slug' <<<"$out")" "slug echo"
assert_eq "step-02" "$(jq -r '.step' <<<"$out")" "step echo"
assert_eq "2026-06-27" "$(jq -r '.shipped_date' <<<"$out")" "shipped_date from WIP_NOW"
assert_eq "yes" "$(in_set "$(jq -r '.marked_shipped' <<<"$out")" updated noop skipped)" \
  "marked_shipped present and in {updated,noop,skipped}"
assert_eq "yes" "$(in_set "$(jq -r '.active_step_cleared' <<<"$out")" updated noop skipped)" \
  "active_step_cleared present and in {updated,noop,skipped}"
assert_eq "yes" "$(in_set "$(jq -r '.changed' <<<"$out")" true false)" \
  "changed is a boolean (true|false)"
assert_eq "null" "$(jq -r '.dry_run' <<<"$out")" "dry_run absent without flag"

# 2. Missing <slug> -> exit 2 (usage).
set +e
out_s="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "missing slug exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out_s")" "missing slug kind usage"

# 3. Missing <step-id> -> exit 2 (usage).
set +e
out_m="$(run demo 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "missing step-id exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out_m")" "missing step-id kind usage"

# 4. Unknown initiative -> exit 3 (unknown-initiative).
set +e
out3="$(run bogus step-02 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"
assert_eq "unknown-initiative" "$(jq -r '.error.kind' <<<"$out3")" "unknown initiative kind"

# 5. Step not in roadmap -> exit 4 (step-not-in-roadmap).
set +e
out4="$(run demo step-99 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "step not in roadmap exit 4"
assert_eq "step-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out4")" "step not in roadmap kind"

# 6. Idempotency (plumbing-level only): two runs both exit 0. The real
#    no-change/identical-ledger idempotency guarantee depends on the live
#    writers and is covered by the per-lane writer tests + step-04 end-to-end,
#    not here.
set +e
a="$(run demo step-02)"
rca=$?
b="$(run demo step-02)"
rcb=$?
set -e
assert_eq "0" "$rca" "first run exit 0"
assert_eq "0" "$rcb" "second run exit 0"

# 7. --dry-run (global position) surfaces dry_run: true.
out_d="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run ship demo step-02)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out_d")" "dry-run global flag surfaced"

# 8. --dry-run (after the verb) also threads through.
out_d2="$(run demo step-02 --dry-run)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out_d2")" "dry-run after-verb flag surfaced"

test_summary
