# tracker — the wip ⇄ issue-tracker node mapping mirror (ADR-0019 §C). The
# roadmap authors `[tracker: <ID>]` per node (source of truth); this verb derives
# the map and, with --write, mirrors it into `.wip.yaml`; `doctor` checks they
# agree. Deterministic; pure read unless --write.
# shellcheck shell=bash

# wip_plumbing_cmd_tracker <subcommand> [args]
wip_plumbing_cmd_tracker() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_die 2 usage "tracker: missing subcommand (map)"
  shift
  case "$sub" in
    map) _wip_tracker_cmd_map "$@" ;;
    bind) _wip_tracker_cmd_bind "$@" ;;
    *) wip_die 2 usage "tracker: unknown subcommand: $sub" ;;
  esac
}

# tracker bind [--initiative <slug>] [--node <step>] — the transport bind plan
# (ADR-0019 §4): for each mapped node, the resolved {issue, semantic_state,
# target_state} the transport needs to push. Plumbing resolves; it never calls
# the provider (the agent/MCP path executes the plan; bare-CLI deferred to
# BDS-23). Pure read.
_wip_tracker_cmd_bind() {
  local slug="" node=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "tracker bind: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      --node)
        [[ $# -ge 2 ]] || wip_die 2 usage "tracker bind: --node requires an argument"
        node="$2"
        shift 2
        ;;
      --node=*)
        node="${1#--node=}"
        shift
        ;;
      -*) wip_die 2 usage "tracker bind: unknown flag: $1" ;;
      *) wip_die 2 usage "tracker bind: unexpected arg: $1" ;;
    esac
  done

  local root mj init_record backend bindings
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative "tracker bind: no current_initiative; pass --initiative <slug>"
  fi
  init_record="$(jq -c --arg s "$slug" '[.initiatives[]? | select(.slug == $s)] | (.[0] // null)' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "tracker bind: initiative not in manifest: $slug"

  backend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"
  bindings="$(_wip_tracker_bind_plan "$root" "$mj" "$slug" "$node")"

  # transport: the agent/MCP path is the default executor; "cli" only when a
  # WIP_LINEAR_*_CMD seam is wired (BDS-23 territory). Plumbing emits the plan
  # either way — it never makes the call.
  local transport="mcp"
  [[ -n "$(_wip_tracker_transport_write_cmd "$backend")" ]] && transport="cli"

  jq -nc \
    --arg slug "$slug" --arg backend "$backend" --arg transport "$transport" \
    --argjson bindings "$bindings" '
    { ok: true, initiative: $slug,
      backend: (if $backend == "" then null else $backend end),
      transport: $transport, bindings: $bindings }'
}

# tracker map [--initiative <slug>] [--write]
#
# Derives the node→issue map from the roadmap's `[tracker: ID]` keys and compares
# it to the `.wip.yaml` mirror. Prints {ok, initiative, tracker_map, mirror,
# agrees, wrote}. With --write, regenerates the mirror from the roadmap when they
# disagree (the roadmap always wins — ADR-0019 §C).
_wip_tracker_cmd_map() {
  local slug="" write=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "tracker: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      --write)
        write=1
        shift
        ;;
      -*) wip_die 2 usage "tracker: unknown flag: $1" ;;
      *) wip_die 2 usage "tracker: unexpected arg: $1" ;;
    esac
  done

  local root mj init_record roadmap_path
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative "tracker: no current_initiative; pass --initiative <slug>"
  fi
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "tracker: initiative not in manifest: $slug"
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  local doc rmap mmap agrees="true" wrote="false"
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"
  rmap="$(_wip_tracker_map_from_roadmap "$doc")"
  mmap="$(_wip_tracker_map_from_manifest "$mj" "$slug")"
  jq -ne --argjson a "$rmap" --argjson b "$mmap" '$a == $b' >/dev/null || agrees="false"

  if [[ "$write" == "1" && "$agrees" == "false" ]]; then
    # Roadmap wins: regenerate the mirror. yq sets the matched initiative's
    # tracker_map to the roadmap-derived object (parsed from JSON).
    SLUG="$slug" RMAP="$rmap" yq -i '
      (.initiatives[] | select(.slug == strenv(SLUG)) | .tracker_map) = (strenv(RMAP) | from_json)
    ' "$root/.wip.yaml"
    mmap="$rmap"
    agrees="true"
    wrote="true"
  fi

  jq -nc \
    --arg slug "$slug" --argjson rmap "$rmap" --argjson mmap "$mmap" \
    --argjson agrees "$agrees" --argjson wrote "$wrote" '
    { ok: true, initiative: $slug, tracker_map: $rmap, mirror: $mmap,
      agrees: $agrees, wrote: $wrote }'
}
