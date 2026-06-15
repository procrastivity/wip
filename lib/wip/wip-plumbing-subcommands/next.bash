# next — ranked candidates for "what to do next". Ranking, not choosing.
# Sources: manifest active_step → roadmap order → roadmap backlog → repo backlog.
# shellcheck shell=bash

wip_plumbing_cmd_next() {
  local slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "next: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "next: unknown flag: $1" ;;
      *) wip_die 2 usage "next: unexpected arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative \
      "next: no current_initiative; pass --initiative <slug>"
  fi
  local init_record
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "next: initiative not in manifest: $slug"

  local active_step_id roadmap_path
  active_step_id="$(jq -r '.active_step // ""' <<<"$init_record")"
  roadmap_path="$(jq -r '.roadmap // ""' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  local doc
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"

  local candidates="[]" rank=1

  # Determine the active step (the one lane-awareness keys off): the manifest
  # active_step when set and unshipped, else the first unshipped step. From it we
  # derive the active round and active lane (ADR-0010).
  local active_is_manifest=0 active_round="" active_lane=""

  # 1. Manifest active_step if unshipped.
  if [[ -n "$active_step_id" ]]; then
    local step
    step="$(wip_roadmap_step "$doc" "$active_step_id")"
    if [[ -n "$step" && "$step" != "null" ]] &&
      [[ "$(jq -r '.shipped' <<<"$step")" == "false" ]]; then
      candidates="$(jq -c \
        --argjson rank "$rank" --argjson step "$step" '
        . + [{rank:$rank, source:"roadmap", id:$step.id, title:$step.title,
              reason:"manifest active step"}]' <<<"$candidates")"
      rank=$((rank + 1))
      active_is_manifest=1
      active_lane="$(jq -r '.lane // ""' <<<"$step")"
      local ar
      ar="$(wip_roadmap_active_round "$doc" "$active_step_id")"
      active_round="$(jq -r '.n // ""' <<<"$ar")"
    fi
  fi

  # When there is no manifest active step, the first unshipped step is the
  # actionable one; its round + lane anchor the lane-aware ranking below.
  if [[ "$active_is_manifest" == "0" ]]; then
    local first
    first="$(wip_roadmap_first_unshipped "$doc")"
    if [[ "$first" != "null" ]]; then
      active_lane="$(jq -r '.lane // ""' <<<"$first")"
      active_round="$(jq -r '.round_n // ""' <<<"$first")"
    fi
  fi

  # 2/3/4. Walk every unshipped step in declared order; skip the one already
  # surfaced as the manifest active step (if any).
  local first_seen=0
  local unshipped
  unshipped="$(wip_roadmap_unshipped_after "$doc" "")"
  # Filter out the manifest active step (which we already emitted).
  unshipped="$(jq -c --arg active "$active_step_id" '
    map(select(.id != $active))
  ' <<<"$unshipped")"

  local count
  count="$(jq -r 'length' <<<"$unshipped")"
  if [[ "$count" == "0" && "$rank" == "1" ]]; then
    # Nothing on the roadmap is unshipped (and no manifest active step).
    candidates="$(jq -c --argjson rank "$rank" '
      . + [{rank:$rank, source:"roadmap", id:null, title:"roadmap complete",
            reason:"start next round / close initiative"}]' <<<"$candidates")"
    rank=$((rank + 1))
  else
    local i
    for ((i = 0; i < count; i++)); do
      local entry reason rn lane concurrent=0
      entry="$(jq -c --argjson i "$i" '.[$i]' <<<"$unshipped")"
      rn="$(jq -r '.round_n' <<<"$entry")"
      lane="$(jq -r '.lane // ""' <<<"$entry")"
      if [[ "$first_seen" == "0" && -z "$active_lane" ]]; then
        # Linear roadmap (active step is main-lane): the first forward candidate
        # is the headline next step. Preserves the pre-lane contract.
        reason="first unshipped step in active round"
      elif [[ "$rn" == "$active_round" && -n "$active_lane" ]]; then
        # Active step lives in a lane; the active round's steps disambiguate by
        # lane (ADR-0010 §7).
        if [[ "$lane" == "$active_lane" ]]; then
          reason="next-in-lane"
        elif [[ -n "$lane" ]]; then
          reason="concurrent lane $lane"
          concurrent=1
        else
          reason="next sequential step"
        fi
      elif [[ "$rn" == "$active_round" ]]; then
        reason="next sequential step"
      else
        reason="upcoming round $rn"
      fi
      first_seen=1
      candidates="$(jq -c \
        --argjson rank "$rank" --argjson e "$entry" --arg reason "$reason" \
        --argjson concurrent "$concurrent" '
        . + [{rank:$rank, source:"roadmap", id:$e.id, title:$e.title,
              reason:$reason} + (if $concurrent == 1 then {concurrent:true} else {} end)]' \
        <<<"$candidates")"
      rank=$((rank + 1))
    done
  fi

  # 5. Roadmap backlog entries.
  local backlog_count
  backlog_count="$(jq -r '.backlog | length' <<<"$doc")"
  local j
  for ((j = 0; j < backlog_count; j++)); do
    local b
    b="$(jq -c --argjson j "$j" '.backlog[$j]' <<<"$doc")"
    candidates="$(jq -c \
      --argjson rank "$rank" --argjson b "$b" '
      . + [{rank:$rank, source:"backlog", id:$b.id, title:$b.title,
            reason:"roadmap backlog"}]' <<<"$candidates")"
    rank=$((rank + 1))
  done

  # 6. Repo backlog (.wip/backlog.md) if present — parse via the same grammar.
  local repo_backlog="$root/.wip/backlog.md"
  if [[ -f "$repo_backlog" ]]; then
    local repo_doc
    repo_doc="$(wip_roadmap_parse "$repo_backlog")"
    local rb_count
    rb_count="$(jq -r '.backlog | length' <<<"$repo_doc")"
    # If .wip/backlog.md has no ## Backlog header, parse entries from any
    # bullet directly under H1. Fall back to a permissive scan.
    if [[ "$rb_count" == "0" ]]; then
      while IFS= read -r line; do
        if [[ "$line" =~ ^-\ \*\*([^*]+)\*\* ]]; then
          local t="${BASH_REMATCH[1]}"
          local rid
          rid="$(printf '%s' "$t" | tr '[:upper:]' '[:lower:]' |
            sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//')"
          candidates="$(jq -c \
            --argjson rank "$rank" --arg id "$rid" --arg title "$t" '
            . + [{rank:$rank, source:"backlog", id:$id, title:$title,
                  reason:"repo backlog"}]' <<<"$candidates")"
          rank=$((rank + 1))
        fi
      done <"$repo_backlog"
    else
      local k
      for ((k = 0; k < rb_count; k++)); do
        local b
        b="$(jq -c --argjson k "$k" '.backlog[$k]' <<<"$repo_doc")"
        candidates="$(jq -c \
          --argjson rank "$rank" --argjson b "$b" '
          . + [{rank:$rank, source:"backlog", id:$b.id, title:$b.title,
                reason:"repo backlog"}]' <<<"$candidates")"
        rank=$((rank + 1))
      done
    fi
  fi

  jq -nc --arg slug "$slug" --argjson candidates "$candidates" '
    { ok: true, initiative: $slug, candidates: $candidates }'
}
