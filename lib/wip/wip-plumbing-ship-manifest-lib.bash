# wip-plumbing-ship-manifest-lib.bash — the `ship` verb's manifest pointer
# writer. Sourced by bin/wip-plumbing. STEP-01 SEAM: this is an inert stub; the
# step-03 (manifest-writer) lane fills the body. Pairs with the roadmap seam in
# wip-plumbing-ship-roadmap-lib.bash. Contract: ADR-0016.
# shellcheck shell=bash

# _wip_ship_clear_active_step <manifest> <slug> <step-id>
#
# Clear initiatives[slug].active_step in .wip.yaml, but ONLY when it currently
# points at <step-id>. Per the seam contract it PRINTS a status word to stdout
# and returns 0, or returns 1 on internal error:
#   updated — active_step pointed at <step-id> and was cleared.
#   noop    — active_step is already unset.
#   skipped — active_step points at a DIFFERENT step; left untouched.
# This updated/noop vocabulary is the exact one `_wip_workplan_set_active_step`
# already prints — reused, not invented.
#
# STEP-01 STUB: inert seam. Performs NO read and NO write; returns `noop` by
# default and honors $WIP_SHIP_FAKE_MANIFEST_STATUS so the harness can exercise
# every status branch (updated/noop/skipped) before the real writer lands.
#
# STEP-03 CONTRACT: replace this body WITHOUT changing the signature or the
# printed-status contract — mirror `_wip_workplan_set_active_step`'s `yq -i`
# idiom but CLEAR the pointer, gated on the current value `== <step-id>` (never
# disturb another step's/initiative's pointer). Honor $WIP_DRY_RUN (no write).
_wip_ship_clear_active_step() {
  local manifest="$1" slug="$2" step_id="$3"
  : "$manifest" "$slug" "$step_id" # step-01 seam: inert; step-03 consumes these.
  printf '%s' "${WIP_SHIP_FAKE_MANIFEST_STATUS:-noop}"
  return 0
}
