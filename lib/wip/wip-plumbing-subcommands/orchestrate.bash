# orchestrate — deterministic prep + backend selection for orchestration.
#
# `orchestrate prep` (ADR-0012) resolves an initiative's active step, gates
# orchestration readiness, and emits a "what to orchestrate" brief that
# /wip:orchestrate consumes before it adopts the Orchestrator role and spawns a
# Coordinator.
#
# `orchestrate backend [<name>]` (ADR-0013) shows or switches the active
# orchestration backend by regenerating the generated pointer
# roles/backends/active.md from roles/backends/<name>.md and flipping
# features.orchestration.backend.
#
# Neither verb spawns or names a backend *tool* (no mcp__solo__*, no
# agent_tool_id) — spawning is a plugin/MCP concern. prep emits the FACTS about
# the work (initiative / step / workplan), not the STAFFING of it (Tier,
# process names) which lives in the Roles + backend binding (ADR-0007); backend
# selects the *binding*, not a tool. Pure function of .wip.yaml + roadmap + disk.
# shellcheck shell=bash

wip_plumbing_cmd_orchestrate() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    prep) _wip_orchestrate_cmd_prep "$@" ;;
    backend) _wip_orchestrate_cmd_backend "$@" ;;
    "") wip_die 2 usage "orchestrate: missing subcommand (prep|backend)" ;;
    *) wip_die 2 usage "orchestrate: unknown subcommand: $sub" ;;
  esac
}

# orchestrate backend [<name>] — show or switch the active orchestration
# backend. With no argument, reports the configured backend + whether the
# generated pointer `roles/backends/active.md` is in sync with it. With a
# <name>, sets features.orchestration.backend and regenerates active.md from
# roles/backends/<name>.md (idempotent). This selects a backend *binding*; it
# names no backend tool, so the ADR-0007 seam stays intact. It's the verb the
# /wip:status fallback offer calls when Solo is unreachable (ADR-0014).
_wip_orchestrate_cmd_backend() {
  local name=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -*) wip_die 2 usage "orchestrate backend: unknown flag: $1" ;;
      *)
        [[ -z "$name" ]] || wip_die 2 usage "orchestrate backend: unexpected arg: $1"
        name="$1"
        shift
        ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  # Resolve the roles/ dir. The generated active.md lives next to the authored
  # backend bindings. In the dev/vendored layout roles/ sits at the repo root;
  # for a shared plugin install it lives under CLAUDE_PLUGIN_ROOT.
  local roles_dir=""
  if [[ -d "$root/roles/backends" ]]; then
    roles_dir="$root/roles"
  elif [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -d "$CLAUDE_PLUGIN_ROOT/roles/backends" ]]; then
    roles_dir="$CLAUDE_PLUGIN_ROOT/roles"
  else
    wip_die 4 no-roles-dir \
      "orchestrate backend: roles/backends/ not found (looked in \$root and \$CLAUDE_PLUGIN_ROOT); a \`source: plugin\` install switches the plugin's active.md, not a per-project copy"
  fi
  local backends_dir="$roles_dir/backends"

  # Available backends = authored *.md under backends/, excluding the
  # generated active.md pointer.
  local available
  available="$(find "$backends_dir" -maxdepth 1 -type f -name '*.md' ! -name 'active.md' \
    -exec basename {} .md \; 2>/dev/null | LC_ALL=C sort | jq -R . | jq -sc .)"
  [[ -n "$available" ]] || available="[]"

  local current
  current="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"

  # No name → report current + sync state, do not mutate.
  if [[ -z "$name" ]]; then
    local in_sync="false" src="$backends_dir/$current.md" dst="$backends_dir/active.md"
    if [[ -n "$current" && -f "$src" && -f "$dst" ]] && cmp -s "$src" "$dst"; then
      in_sync="true"
    fi
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg b "$current" --argjson avail "$available" --argjson sync "$in_sync" '
        {ok:true, verb:"orchestrate backend", backend:$b, available:$avail, active_in_sync:$sync}'
    fi
    return 0
  fi

  # Switch. Validate the requested backend exists (active is reserved).
  [[ "$name" != "active" ]] ||
    wip_die 2 usage "orchestrate backend: 'active' is the generated pointer, not a backend name"
  local src="$backends_dir/$name.md" dst="$backends_dir/active.md"
  [[ -f "$src" ]] ||
    wip_die 4 unknown-backend \
      "orchestrate backend: no such backend '$name' (available: $(jq -r 'join(", ")' <<<"$available"))"

  # Regenerate the pointer iff it differs (idempotent). Honor --dry-run env.
  local active_regenerated=false
  if [[ ! -f "$dst" ]] || ! cmp -s "$src" "$dst"; then
    if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
      cp "$src" "$dst" || wip_die 1 internal "orchestrate backend: failed to regenerate active.md"
    fi
    active_regenerated=true
  fi

  # Flip features.orchestration.backend (idempotent; honors WIP_DRY_RUN).
  # Surgical scalar set — NOT a whole-node rewrite — so block style and the
  # manifest's inline comments survive a switch (this verb runs repeatedly,
  # incl. from the /wip:status fallback offer).
  local manifest="$root/.wip.yaml" manifest_updated_json="null"
  if [[ "$current" != "$name" ]]; then
    if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
      NAME="$name" yq -i '.features.orchestration.backend = strenv(NAME)' "$manifest" ||
        wip_die 1 internal "orchestrate backend: manifest update failed"
    fi
    manifest_updated_json='".wip.yaml"'
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc \
      --arg b "$name" --argjson avail "$available" \
      --argjson regen "$active_regenerated" \
      --argjson manifest_updated "$manifest_updated_json" '
      {ok:true, verb:"orchestrate backend", backend:$b, available:$avail,
       active_regenerated:$regen, manifest_updated:$manifest_updated}'
  fi
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
