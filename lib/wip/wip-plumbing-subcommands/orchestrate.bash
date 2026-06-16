# orchestrate — deterministic prep for booting orchestration (ADR-0012).
#
# `orchestrate prep` resolves an initiative's active step, gates orchestration
# readiness, and emits a "what to orchestrate" brief that /wip:orchestrate
# consumes before it adopts the Orchestrator role and spawns a Coordinator.
#
# This verb NEVER spawns and NEVER names a backend tool (no mcp__solo__*,
# no agent_tool_id) — spawning is a plugin/MCP concern. It emits the FACTS
# about the work (initiative / step / workplan), not the STAFFING of it
# (Tier, process names) which lives in the Roles + backend binding (ADR-0007).
# Pure function of .wip.yaml + roadmap + disk.
# shellcheck shell=bash

wip_plumbing_cmd_orchestrate() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    prep) _wip_orchestrate_cmd_prep "$@" ;;
    "") wip_die 2 usage "orchestrate: missing subcommand (prep)" ;;
    *) wip_die 2 usage "orchestrate: unknown subcommand: $sub" ;;
  esac
}

_wip_orchestrate_cmd_prep() {
  local slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "orchestrate prep: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "orchestrate prep: unknown flag: $1" ;;
      *) wip_die 2 usage "orchestrate prep: unexpected arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  # Gate 1: the orchestration capability must be enabled (ADR-0007). Feature
  # not enabled => exit 3 (per the prtend exit-code contract).
  local orch_enabled backend
  orch_enabled="$(jq -r '.features.orchestration.enabled // false' <<<"$mj")"
  backend="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"
  if [[ "$orch_enabled" != "true" ]]; then
    wip_die 3 orchestration-not-enabled \
      "orchestrate prep: features.orchestration.enabled is not true — run \`wip-plumbing setup agents\` or enable it in .wip.yaml"
  fi

  # Resolve the initiative (default current; --initiative overrides). Mirrors
  # status' resolution so the two agree on "which initiative".
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative \
      "orchestrate prep: no current_initiative; pass --initiative <slug>"
  fi
  local init_record
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "orchestrate prep: initiative not in manifest: $slug"

  local active_step_id roadmap_path
  active_step_id="$(jq -r '.active_step // ""' <<<"$init_record")"
  roadmap_path="$(jq -r '.roadmap // ""' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  # Gate 2: an active step must be set. Orchestration boots the ACTIVE step;
  # without one there is nothing to orchestrate => exit 4 (data prevents
  # safe action; a human runs /wip:start first).
  [[ -n "$active_step_id" ]] || wip_die 4 no-active-step \
    "orchestrate prep: no active_step for '$slug' — run \`/wip:start <step-id>\` first" "$roadmap_path"

  local doc
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"

  # Gate 3: the active step must exist in the roadmap => else exit 4.
  local step_record
  step_record="$(wip_roadmap_step "$doc" "$active_step_id")"
  if [[ -z "$step_record" || "$step_record" == "null" ]]; then
    wip_die 4 step-not-in-roadmap \
      "orchestrate prep: active_step not in roadmap: $active_step_id" "$roadmap_path"
  fi

  local step_title step_shipped step_lane round
  step_title="$(jq -r '.title // ""' <<<"$step_record")"
  step_shipped="$(jq -r '.shipped // false' <<<"$step_record")"
  step_lane="$(jq -c '.lane // null' <<<"$step_record")"
  round="$(wip_roadmap_active_round "$doc" "$active_step_id")"
  [[ -n "$round" ]] || round="null"

  # Locate the step's workplan. Glob <step-id>-*.md under workplans/; the `-`
  # delimiter after the step id keeps step-01 from matching step-01.5. A
  # MISSING workplan is NOT an error: the Coordinator's Researcher produces it
  # in Phase 1 (Roles). We still emit a canonical path (derived from the step
  # title, mirroring `workplan init`) so the Orchestrator has a stable target.
  local wp_dir_rel=".wip/initiatives/$slug/workplans"
  local wp_path="" wp_exists="false"
  local match
  match="$(find "$root/$wp_dir_rel" -maxdepth 1 -type f -name "$active_step_id-*.md" 2>/dev/null | LC_ALL=C sort | head -1)"
  if [[ -n "$match" ]]; then
    wp_path="$wp_dir_rel/$(basename "$match")"
    wp_exists="true"
  else
    # Derive the canonical slug the same way `workplan init` does.
    local derived
    derived="$(printf '%s' "$step_title" | tr '[:upper:]' '[:lower:]' |
      sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//')"
    [[ -n "$derived" ]] || derived="$active_step_id"
    wp_path="$wp_dir_rel/$active_step_id-$derived.md"
  fi

  # Advisory signals (non-fatal). Mirrors status' divergence reporting: an
  # already-shipped active step is surfaced, not refused.
  local signals="[]"
  if [[ "$step_shipped" == "true" ]]; then
    signals="$(jq -nc '["active-step-shipped"]')"
  fi

  jq -nc \
    --arg slug "$slug" \
    --arg backend "$backend" \
    --argjson round "$round" \
    --arg sid "$active_step_id" --arg stitle "$step_title" \
    --argjson sshipped "$step_shipped" --argjson slane "$step_lane" \
    --arg wppath "$wp_path" --argjson wpexists "$wp_exists" \
    --arg roadmap "$roadmap_path" \
    --argjson signals "$signals" '
    {
      ok: true,
      initiative: $slug,
      orchestration: { enabled: true, backend: $backend },
      round: $round,
      active_step: { id: $sid, title: $stitle, shipped: $sshipped, lane: $slane },
      workplan: { path: $wppath, exists: $wpexists },
      roadmap: $roadmap,
      signals: $signals
    }'
}
