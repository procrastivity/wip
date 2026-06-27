# wip-plumbing-ship-roadmap-lib.bash — the `ship` verb's roadmap marker writer.
# Sourced by bin/wip-plumbing. STEP-01 SEAM: this is an inert stub; the
# step-02 (roadmap-writer) lane fills the body. Pairs with the manifest seam in
# wip-plumbing-ship-manifest-lib.bash. Contract: ADR-0016.
# shellcheck shell=bash

# _wip_ship_mark_roadmap_shipped <roadmap-path> <step-id> <date>
#
# Insert/normalize <step-id>'s `✅ shipped <date>` bullet marker in the roadmap.
# Per the seam contract it PRINTS a status word to stdout and returns 0, or
# returns 1 on internal error:
#   updated — the marker was written (or corrected to <date>).
#   noop    — the bullet already carries the correct `✅ shipped <date>`.
#
# STEP-01 STUB: inert seam. Performs NO read and NO write; returns `noop` by
# default and honors $WIP_SHIP_FAKE_ROADMAP_STATUS so the harness can exercise
# every status branch (updated/noop) before the real writer lands.
#
# STEP-02 CONTRACT: replace this body WITHOUT changing the signature or the
# printed-status contract — reuse `_wip_roadmap_extract_shipped` (grammar) to
# read the bullet's current shipped state and `wip_amend_apply_replace` (in-place
# block rewrite) to insert/normalize the marker. Honor $WIP_DRY_RUN (no write).
_wip_ship_mark_roadmap_shipped() {
  local roadmap="$1" step_id="$2" date="$3"
  : "$roadmap" "$step_id" "$date" # step-01 seam: inert; step-02 consumes these.
  printf '%s' "${WIP_SHIP_FAKE_ROADMAP_STATUS:-noop}"
  return 0
}
