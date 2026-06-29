#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-end-to-end"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the COMPOSED `ship` verb driven END-TO-END through `bin/wip-plumbing
# ship` — proving ONE invocation fires BOTH lanes (roadmap `✅ shipped` marker
# AND `active_step` clear), the `changed` OR aggregation, and cross-artifact
# end-to-end idempotency + ledger stability. Lane-internal mechanics (marker
# placement, date normalization, other-initiative isolation, error codes) are
# owned by the per-lane tests (test-ship-roadmap-writer.sh /
# test-ship-manifest-writer.sh) and test-ship-skeleton.sh — NOT re-tested here.
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

# setup_e2e [marker] [active] — write a fresh single-`demo` fixture into a new
# tmp root and set the globals `tmp` (root), `roadmap`, and `manifest`.
#   marker : "unmarked" (default) → step-02 bullet has no ✅ marker;
#            "marked"             → step-02 bullet already `✅ shipped 2026-06-27`.
#   active : `active_step` pointer value (default "step-02", the matching
#            pointer); pass "" to OMIT the key entirely (already-clear case).
# The roadmap carries a real step-01 and step-02 so `ship`'s step-in-roadmap
# guard passes and the call reaches both writers; step-01 doubles as a valid
# "different existing step" target for the `skipped` rows.
setup_e2e() {
  # `${2-...}` (no colon) so an explicitly-passed empty `active` (omit the key)
  # is honored rather than falling back to the default pointer.
  local marker="${1:-unmarked}" active="${2-step-02}"
  tmp="$(mktemp -d)"
  TMP_DIRS+=("$tmp")
  mkdir -p "$tmp/.wip/initiatives/demo"

  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    printf 'current_initiative: demo\n'
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    [[ -n "$active" ]] && printf '    active_step: %s\n' "$active"
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
  } >"$tmp/.wip.yaml"

  local step02='- **step-02 — Refresh tokens** (small) — current.'
  [[ "$marker" == marked ]] &&
    step02='- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.'
  {
    printf '# Roadmap\n'
    printf '\n'
    printf '## Round 1 — Build\n'
    printf '\n'
    printf '%s\n' '- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
    printf '%s\n' "$step02"
  } >"$tmp/.wip/initiatives/demo/roadmap.md"

  manifest="$tmp/.wip.yaml"
  roadmap="$tmp/.wip/initiatives/demo/roadmap.md"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# active_step_of <slug> — yq re-read of an initiative's active_step (empty when
# the key is unset; immune to formatting). Copied from test-ship-manifest-writer.sh.
active_step_of() {
  SLUG="$1" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""
  ' "$manifest"
}

# step02_line — echo the roadmap's step-02 bullet first line.
step02_line() { grep -F '**step-02 —' "$roadmap"; }

# ---------------------------------------------------------------------------
# Case A — composed first run: ONE invocation fires BOTH lanes. The roadmap
#   bullet is marked AND `active_step` is cleared from the same call, with both
#   lane statuses `updated`, `changed: true`, and `dry_run` absent.
# ---------------------------------------------------------------------------
setup_e2e
out="$(run demo step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "demo" "$(jq -r '.slug' <<<"$out")" "A: slug echo"
assert_eq "step-02" "$(jq -r '.step' <<<"$out")" "A: step echo"
assert_eq "2026-06-27" "$(jq -r '.shipped_date' <<<"$out")" "A: shipped_date from WIP_NOW"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "A: marked_shipped updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "A: active_step_cleared updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"
# No forge declared -> ship carries the Tier-0 in-review transition intent (ADR-0018).
assert_eq "in-review" "$(jq -r '.transition' <<<"$out")" "A: transition in-review (no forge)"
assert_eq "null" "$(jq -r '.dry_run' <<<"$out")" "A: dry_run absent without flag"
# Observable composition proof: BOTH artifacts changed from the one call.
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "A: roadmap bullet marked shipped"
assert_eq "" "$(active_step_of demo)" "A: demo active_step cleared"

# ---------------------------------------------------------------------------
# Case B — end-to-end idempotency + stable ledger. Snapshot BOTH artifacts
#   AFTER run 1, then prove run 2 is a double-noop that writes nothing to
#   either file, and that the steady-state ledger is byte-identical run-vs-run.
# ---------------------------------------------------------------------------
setup_e2e
run demo step-02 >/dev/null # run 1 — mutates both artifacts
snap_roadmap="$(mktemp)"
TMP_DIRS+=("$snap_roadmap")
cp "$roadmap" "$snap_roadmap"
snap_manifest="$(mktemp)"
TMP_DIRS+=("$snap_manifest")
cp "$manifest" "$snap_manifest"
out2="$(run demo step-02)" # run 2 — steady state
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out2")" "B: run2 marked_shipped noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out2")" "B: run2 active_step_cleared noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "B: run2 changed false"
assert_cmp "$snap_roadmap" "$roadmap" "B: roadmap byte-identical across re-run"
assert_cmp "$snap_manifest" "$manifest" "B: .wip.yaml byte-identical across re-run"
out3="$(run demo step-02)" # run 3 — steady state again
assert_eq "$out2" "$out3" "B: steady-state ledger stable (run2 == run3)"

# ---------------------------------------------------------------------------
# Case C — composed `--dry-run`: both lane statuses are still computed
#   (`updated`/`updated`, `changed: true`, `dry_run: true`) but NEITHER artifact
#   is written — the composed no-write guarantee.
# ---------------------------------------------------------------------------
setup_e2e
before_roadmap="$(mktemp)"
TMP_DIRS+=("$before_roadmap")
cp "$roadmap" "$before_roadmap"
before_manifest="$(mktemp)"
TMP_DIRS+=("$before_manifest")
cp "$manifest" "$before_manifest"
out="$(run demo step-02 --dry-run)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "C: dry-run marked_shipped updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "C: dry-run active_step_cleared updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "C: dry-run changed true"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "C: dry-run dry_run true"
assert_cmp "$before_roadmap" "$roadmap" "C: roadmap unwritten under --dry-run"
assert_cmp "$before_manifest" "$manifest" "C: .wip.yaml unwritten under --dry-run"

# ---------------------------------------------------------------------------
# Case D — `changed` OR truth table. Each row is its own fresh fixture and
#   asserts BOTH lane statuses AND the resulting `changed`. Case A already
#   covers updated+updated→true; Case B covers noop+noop→false.
# ---------------------------------------------------------------------------

# D1: updated + noop → true (unmarked bullet, active_step already unset).
setup_e2e unmarked ""
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "D updated+noop: marked_shipped updated"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out")" "D updated+noop: active_step_cleared noop"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D updated+noop: changed true"

# D2: updated + skipped → true (unmarked bullet, active_step points at a
#     DIFFERENT existing step; that pointer is left in place — closeout is never
#     blocked by a non-matching pointer).
setup_e2e unmarked step-01
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "D updated+skipped: marked_shipped updated"
assert_eq "skipped" "$(jq -r '.active_step_cleared' <<<"$out")" "D updated+skipped: active_step_cleared skipped"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D updated+skipped: changed true"
assert_eq "step-01" "$(active_step_of demo)" "D updated+skipped: non-matching pointer left in place"

# D3: noop + updated → true (bullet already marked, active_step: step-02 matches).
setup_e2e marked step-02
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out")" "D noop+updated: marked_shipped noop"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "D noop+updated: active_step_cleared updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D noop+updated: changed true"
assert_eq "" "$(active_step_of demo)" "D noop+updated: matching pointer cleared"

# D4: noop + skipped → false (bullet already marked, active_step names another
#     step — proves `changed` keys off `updated` ONLY, not "work considered").
setup_e2e marked step-01
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out")" "D noop+skipped: marked_shipped noop"
assert_eq "skipped" "$(jq -r '.active_step_cleared' <<<"$out")" "D noop+skipped: active_step_cleared skipped"
assert_eq "false" "$(jq -r '.changed' <<<"$out")" "D noop+skipped: changed false"
assert_eq "step-01" "$(active_step_of demo)" "D noop+skipped: non-matching pointer left in place"

# ---------------------------------------------------------------------------
# Case E — forge stand-down (ADR-0018). When a forge owns the transition
#   (features.forge.enabled), ship reports `transition: stood-down` — BUT its
#   disk writes are UNCHANGED (the bullet is still marked, active_step still
#   cleared). The stand-down is intent-only, never a gate.
# ---------------------------------------------------------------------------
setup_e2e
WIP_ROOT="$tmp" yq -i '.features.forge.enabled = true' "$manifest"
out="$(run demo step-02)"
assert_eq "stood-down" "$(jq -r '.transition' <<<"$out")" "E: transition stood-down (forge owns it)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "E: disk write unchanged — marked_shipped updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "E: disk write unchanged — active_step cleared"
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "E: roadmap bullet still marked despite stand-down"
assert_eq "" "$(active_step_of demo)" "E: active_step still cleared despite stand-down"

test_summary
