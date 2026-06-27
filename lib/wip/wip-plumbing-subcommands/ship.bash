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

  # Locked flat JSON ledger (mirrors `workplan init`'s emit style). `dry_run`
  # key present only under --dry-run.
  jq -nc \
    --arg slug "$slug" --arg step "$step_id" --arg date "$shipped_date" \
    --arg ms "$marked_shipped" --arg asc "$active_step_cleared" \
    --argjson changed "$changed" --arg dry "$dry_run" '
    { ok: true, slug: $slug, step: $step, shipped_date: $date,
      marked_shipped: $ms, active_step_cleared: $asc, changed: $changed }
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}
