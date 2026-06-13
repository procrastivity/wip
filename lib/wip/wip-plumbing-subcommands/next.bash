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
    fi
  fi

  # 2/3/4. Walk every unshipped step in declared order; skip the one already
  # surfaced as the manifest active step (if any).
  local first_seen=0
  local seen_in_round_1=0
  local active_round_n
  active_round_n="$(jq -r '.[0].round_n // 0' <<<"$(wip_roadmap_unshipped_after "$doc" "")")"
  # Use unshipped_after with empty step to get everything.
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
      local entry reason rn
      entry="$(jq -c --argjson i "$i" '.[$i]' <<<"$unshipped")"
      rn="$(jq -r '.round_n' <<<"$entry")"
      if [[ "$first_seen" == "0" ]]; then
        reason="first unshipped step in active round"
        first_seen=1
        seen_in_round_1="$rn"
      elif [[ "$rn" == "$seen_in_round_1" ]]; then
        reason="next sequential step"
      else
        reason="upcoming round $rn"
      fi
      candidates="$(jq -c \
        --argjson rank "$rank" --argjson e "$entry" --arg reason "$reason" '
        . + [{rank:$rank, source:"roadmap", id:$e.id, title:$e.title,
              reason:$reason}]' <<<"$candidates")"
      rank=$((rank + 1))
    done
  fi
  # active_round_n is referenced solely to document intent (top-of-list rank);
  # the per-row rn comparison already drives the boundary detection.
  : "$active_round_n"

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
