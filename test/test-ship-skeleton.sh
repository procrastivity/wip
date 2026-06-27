#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-skeleton"
# shellcheck source=test/helpers.sh
source test/helpers.sh

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

# 1. Dispatch + happy path: `ship` routes to wip_plumbing_cmd_ship and emits the
#    locked ledger. With both stubs inert (default noop), changed is false.
out="$(run demo step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.slug' <<<"$out")" "slug echo"
assert_eq "step-02" "$(jq -r '.step' <<<"$out")" "step echo"
assert_eq "2026-06-27" "$(jq -r '.shipped_date' <<<"$out")" "shipped_date from WIP_NOW"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out")" "marked_shipped default noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out")" "active_step_cleared default noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out")" "changed false when both noop"
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

# 6. Status aggregation: roadmap `updated` -> marked_shipped reflects it, changed true.
out_r="$(WIP_ROOT="$tmp" WIP_SHIP_FAKE_ROADMAP_STATUS=updated bin/wip-plumbing ship demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out_r")" "roadmap status reflects env"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out_r")" "manifest still noop"
assert_eq "true" "$(jq -r '.changed' <<<"$out_r")" "changed true when roadmap updated"

# 7. Status aggregation: manifest `updated` -> active_step_cleared reflects it, changed true.
out_mn="$(WIP_ROOT="$tmp" WIP_SHIP_FAKE_MANIFEST_STATUS=updated bin/wip-plumbing ship demo step-02)"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out_mn")" "manifest status reflects env"
assert_eq "true" "$(jq -r '.changed' <<<"$out_mn")" "changed true when manifest updated"

# 8. Status aggregation: manifest `skipped` + roadmap noop -> surfaced, changed false.
out_sk="$(WIP_ROOT="$tmp" WIP_SHIP_FAKE_MANIFEST_STATUS=skipped bin/wip-plumbing ship demo step-02)"
assert_eq "skipped" "$(jq -r '.active_step_cleared' <<<"$out_sk")" "manifest skipped surfaced"
assert_eq "false" "$(jq -r '.changed' <<<"$out_sk")" "changed false when skipped + noop"

# 9. Status aggregation: both `updated` -> changed true.
out_both="$(WIP_ROOT="$tmp" WIP_SHIP_FAKE_ROADMAP_STATUS=updated WIP_SHIP_FAKE_MANIFEST_STATUS=updated \
  bin/wip-plumbing ship demo step-02)"
assert_eq "true" "$(jq -r '.changed' <<<"$out_both")" "changed true when both updated"

# 10. Idempotency: two runs with both stubs noop -> identical ledger, changed
#     false, exit 0 both times.
set +e
a="$(run demo step-02)"
rca=$?
b="$(run demo step-02)"
rcb=$?
set -e
assert_eq "0" "$rca" "first run exit 0"
assert_eq "0" "$rcb" "second run exit 0"
assert_eq "$a" "$b" "idempotent: identical ledger"
assert_eq "false" "$(jq -r '.changed' <<<"$b")" "idempotent: changed false"

# 11. --dry-run (global position) surfaces dry_run: true.
out_d="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run ship demo step-02)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out_d")" "dry-run global flag surfaced"

# 12. --dry-run (after the verb) also threads through.
out_d2="$(run demo step-02 --dry-run)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out_d2")" "dry-run after-verb flag surfaced"

test_summary
