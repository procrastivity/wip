# ship — close out a single roadmap step: write its `✅ shipped <date>` bullet
# marker and clear `active_step` when it points at that step. A pure
# deterministic state-writer — no gating (drift detection is doctor's job).
# Operates step-level ONLY; round-level closeout is out of scope. v1 contract is
# locked in ADR-0016 (engineering/decisions/0016-closeout-write-contract.md).
# shellcheck shell=bash

# wip_plumbing_cmd_ship <slug> <step-id> [--dry-run]
#
# Resolution mirrors `workplan init`: resolve the initiative from .wip.yaml
# (wip_find_root + wip_manifest_json), resolve its roadmap path, parse it
# (wip_roadmap_parse), and verify the step exists. Error codes mirror
# `workplan init`: missing <slug>/<step-id> -> exit 2 (usage); unknown
# initiative -> exit 3 (unknown-initiative); step not in roadmap -> exit 4
# (step-not-in-roadmap). Then calls both writer seams, aggregates their printed
# status, and emits the locked flat JSON ledger.
wip_plumbing_cmd_ship() {
  local slug="" step_id="" dry_run="${WIP_DRY_RUN:-0}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)
        dry_run=1
        shift
        ;;
      -*) wip_die 2 usage "ship: unknown flag: $1" ;;
      *)
        if [[ -z "$slug" ]]; then
          slug="$1"
        elif [[ -z "$step_id" ]]; then
          step_id="$1"
        else
          wip_die 2 usage "ship: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done

  [[ -n "$slug" ]] || wip_die 2 usage "ship: missing <slug>"
  [[ -n "$step_id" ]] || wip_die 2 usage "ship: missing <step-id>"

  # Thread --dry-run to both writer seams via the env var they read.
  WIP_DRY_RUN="$dry_run"
  export WIP_DRY_RUN

  # Resolve initiative + roadmap (idiom copied from `workplan init`).
  local root mj init_record roadmap_path
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "ship: initiative not in manifest: $slug"
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  # Verify the step exists in the roadmap.
  local doc step_record
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"
  step_record="$(wip_roadmap_step "$doc" "$step_id")"
  if [[ -z "$step_record" || "$step_record" == "null" ]]; then
    wip_die 4 step-not-in-roadmap "ship: step not in roadmap: $step_id" "$roadmap_path"
  fi

  # The shipped date is a seam param (no --date flag in v1). Honors $WIP_NOW.
  local shipped_date
  shipped_date="$(wip_scaffold_now)"

  # Call both writer seams; each prints a status word and returns 0 (1 on error).
  local marked_shipped active_step_cleared
  marked_shipped="$(_wip_ship_mark_roadmap_shipped "$root/$roadmap_path" "$step_id" "$shipped_date")" ||
    wip_die 1 internal "ship: roadmap marker writer failed"
  active_step_cleared="$(_wip_ship_clear_active_step "$root/.wip.yaml" "$slug" "$step_id")" ||
    wip_die 1 internal "ship: active_step clear failed"

  # `changed` is true iff either writer reported `updated`.
  local changed=false
  if [[ "$marked_shipped" == "updated" || "$active_step_cleared" == "updated" ]]; then
    changed=true
  fi

  # Tier-0/Tier-1 stand-down (ADR-0018). ship's DISK writes above are unchanged
  # and un-gated (ADR-0016). What stands down is only the *transition intent*:
  # when a reachable forge owns the transition (features.forge.enabled + a
  # successful liveness probe), forge observation is the single transition writer,
  # so ship reports `transition: stood-down` instead of its Tier-0 In-Review drive
  # — no double-fire. With no forge, ship carries the Tier-0 `in-review` intent
  # (the Linear write that consumes it is BDS-20's).
  local transition="in-review"
  if [[ "$(jq -r '.features.forge.enabled // false' <<<"$mj")" == "true" ]]; then
    local fcli fcmd forge_backend
    forge_backend="$(jq -r '.features.forge.backend // ""' <<<"$mj")"
    fcli="$(_wip_forge_detect "$forge_backend")"
    fcmd="$(_wip_forge_status_cmd "$fcli")"
    if [[ -n "$fcmd" ]] && _wip_forge_run "$fcmd" >/dev/null 2>&1; then
      transition="stood-down"
    fi
  fi

  # Tier-0 lifecycle (ADR-0019 §A): ship is the In-Review boundary. When
  # issue-tracker is enabled AND ship still owns the transition (Tier-0, i.e. not
  # stood down for a forge), emit an {to:in-review, reason:ship} intent into the
  # cache floor — generalizing the `transition` field this verb already carried.
  # Exactly one writer: under stand-down ship emits nothing (the forge owns it).
  # Skipped under --dry-run.
  local intent="null"
  if [[ "$transition" == "in-review" && "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    if [[ "$dry_run" != "1" ]]; then
      intent="$(_wip_tracker_emit_intent "$root" "$slug" "$step_id" "in-review" "ship" "$shipped_date")"
    else
      intent="$(jq -nc --arg n "$slug/$step_id" '{node:$n, to:"in-review", reason:"ship"}')"
    fi
  fi

  # Locked flat JSON ledger (mirrors `workplan init`'s emit style), extended with
  # `transition` (ADR-0018) and the lifecycle `intent` (ADR-0019). `dry_run` key
  # present only under --dry-run; `intent` only when emitted.
  jq -nc \
    --arg slug "$slug" --arg step "$step_id" --arg date "$shipped_date" \
    --arg ms "$marked_shipped" --arg asc "$active_step_cleared" \
    --arg transition "$transition" --argjson intent "$intent" \
    --argjson changed "$changed" --arg dry "$dry_run" '
    { ok: true, slug: $slug, step: $step, shipped_date: $date,
      marked_shipped: $ms, active_step_cleared: $asc, changed: $changed,
      transition: $transition }
    + (if $intent != null then { intent: $intent } else {} end)
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}
