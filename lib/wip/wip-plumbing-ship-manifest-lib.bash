# wip-plumbing-ship-manifest-lib.bash — the `ship` verb's manifest pointer
# writer. Sourced by bin/wip-plumbing. Pairs with the roadmap seam in
# wip-plumbing-ship-roadmap-lib.bash. Contract: ADR-0016.
# shellcheck shell=bash

# _wip_ship_clear_active_step <manifest> <slug> <step-id>
#
# Clear initiatives[slug].active_step in .wip.yaml, but ONLY when it currently
# points at <step-id>. Per the seam contract it PRINTS a status word to stdout
# and returns 0, or returns 1 on internal error:
#   updated — active_step pointed at <step-id> and was cleared.
#   noop    — active_step is already unset.
#   skipped — active_step points at a DIFFERENT step; left untouched (silently).
# This updated/noop/skipped vocabulary is the exact one
# `_wip_workplan_set_active_step` already prints — reused, not invented.
#
# Mirrors `_wip_workplan_set_active_step`'s read idiom (same
# `select(.slug == strenv(SLUG))` targeting, so OTHER initiatives' pointers are
# never touched) but CLEARS the pointer via `yq -i del(...)` instead of setting
# it, gated on the current value `== <step-id>`. Honors $WIP_DRY_RUN (no write).
_wip_ship_clear_active_step() {
  local manifest="$1" slug="$2" step_id="$3"
  [[ -f "$manifest" ]] || {
    printf 'wip-plumbing: ship: manifest missing: %s\n' "$manifest" >&2
    return 1
  }
  local current
  current="$(SLUG="$slug" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""
  ' "$manifest" 2>/dev/null)" || current=""
  [[ "$current" == "null" ]] && current=""
  if [[ -z "$current" ]]; then
    printf 'noop'
    return 0
  fi
  if [[ "$current" != "$step_id" ]]; then
    printf 'skipped'
    return 0
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi
  SLUG="$slug" yq -i '
    del(.initiatives[] | select(.slug == strenv(SLUG)) | .active_step)
  ' "$manifest" || return 1
  printf 'updated'
  return 0
}
