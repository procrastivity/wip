#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-manifest-writer"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the manifest `active_step` clearer (_wip_ship_clear_active_step) driven
# END-TO-END through `bin/wip-plumbing ship`. Assertions are MANIFEST-SCOPED
# only — the ledger's `active_step_cleared` and the actual `.wip.yaml` state —
# never `marked_shipped` / the roadmap, whose values are step-02's lane.
# Contract: ADR-0016 (engineering/decisions/0016-closeout-write-contract.md).
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-27

declare -a TMP_DIRS=()
cleanup() {
  local d
  for d in "${TMP_DIRS[@]}"; do rm -rf "$d"; done
}
trap cleanup EXIT

# setup_manifest — write a fresh MULTI-INITIATIVE fixture into a new tmp root and
# set the globals `tmp` (root) and `manifest` (absolute .wip.yaml path):
#   demo  — active_step: step-03 (the pointer under test)
#   other — active_step: step-07 (proves OTHER initiatives are never touched)
#   clean — no active_step key   (the already-clear case)
# Each shipped initiative gets a roadmap whose steps exist, so `ship`'s
# step-in-roadmap guard passes and the call reaches the manifest writer.
setup_manifest() {
  tmp="$(mktemp -d)"
  TMP_DIRS+=("$tmp")
  mkdir -p "$tmp/.wip/initiatives/demo" "$tmp/.wip/initiatives/clean"
  cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-03
    roadmap: .wip/initiatives/demo/roadmap.md
  - slug: other
    status: in-flight
    active_step: step-07
    roadmap: .wip/initiatives/other/roadmap.md
  - slug: clean
    status: in-flight
    roadmap: .wip/initiatives/clean/roadmap.md
YAML
  cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-03 — Manifest writer** (small) — current.
- **step-99 — Future work** (small) — later.
MD
  cat >"$tmp/.wip/initiatives/clean/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — Bootstrap** (small) — current.
MD
  manifest="$tmp/.wip.yaml"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# active_step_of <slug> — yq re-read of an initiative's active_step (empty when
# the key is unset; immune to formatting, unlike grep on a shared key name).
active_step_of() {
  SLUG="$1" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""
  ' "$manifest"
}

# ---------------------------------------------------------------------------
# 1. Clear-on-match: ship the step the pointer names → `updated`, the matched
#    initiative's active_step key is removed, and an idempotent re-run is `noop`.
# ---------------------------------------------------------------------------
setup_manifest
out="$(run demo step-03)"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "match: active_step_cleared updated"
assert_eq "" "$(active_step_of demo)" "match: demo active_step removed"
out2="$(run demo step-03)"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out2")" "match: re-run is noop (idempotent)"

# ---------------------------------------------------------------------------
# 2. Leave other initiatives untouched + different-step `skipped`.
#    Clearing demo must not disturb `other`'s pointer; shipping a step the
#    pointer does NOT name leaves the pointer in place, silently.
# ---------------------------------------------------------------------------
setup_manifest
run demo step-03 >/dev/null
assert_eq "step-07" "$(active_step_of other)" "other: pointer untouched after clearing demo"

setup_manifest
out="$(run demo step-99)"
assert_eq "skipped" "$(jq -r '.active_step_cleared' <<<"$out")" "skipped: different step → skipped"
assert_eq "step-03" "$(active_step_of demo)" "skipped: demo active_step left in place"

# ---------------------------------------------------------------------------
# 3. Already-clear no-op: an initiative with no active_step key → `noop`, and
#    it still has no active_step afterward.
# ---------------------------------------------------------------------------
setup_manifest
out="$(run clean step-01)"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out")" "already-clear: active_step_cleared noop"
assert_eq "" "$(active_step_of clean)" "already-clear: clean still has no active_step"

# ---------------------------------------------------------------------------
# 4. --dry-run: matching pointer reports `updated` and `dry_run: true`, but
#    `.wip.yaml` is byte-identical (no write) and demo's pointer is unchanged.
# ---------------------------------------------------------------------------
setup_manifest
before="$(mktemp)"
TMP_DIRS+=("$before")
cp "$manifest" "$before"
out="$(run demo step-03 --dry-run)"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "dry-run: active_step_cleared updated"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "dry-run: dry_run true in ledger"
assert_cmp "$before" "$manifest" "dry-run: .wip.yaml byte-identical (no write)"
assert_eq "step-03" "$(active_step_of demo)" "dry-run: demo active_step unchanged"

test_summary
