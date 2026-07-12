# next — ranked candidates for "what to do next". Ranking, not choosing.
# Sources: manifest active_step → roadmap order → roadmap backlog → repo backlog.
# shellcheck shell=bash

# _wip_next_tracker_closed <root> <tracker-id> — return 0 (true) only when the
# tracker's cached state says the issue is CLOSED. Same closed vocabulary as
# doctor's backlog-tracker-closed check: done / canceled / cancelled,
# case-insensitively.
#
# Decision 4 (fail open) is the whole point of this helper: a tracker with NO
# `issue:<ID>` cache entry is UNKNOWN, and unknown means keep showing it. Having
# a tracker at all is never itself a reason to drop a candidate — nothing
# populates the `issue:*` keyspace yet, so a "any tracker ⇒ filter" reading would
# silently empty both backlog sources.
_wip_next_tracker_closed() {
  local root="$1" tracker="$2" entry state
  [[ -n "$tracker" && "$tracker" != "null" ]] || return 1

  entry="$(_wip_tracker_cache_get "$root" "issue:$tracker")"
  [[ -n "$entry" && "$entry" != "null" ]] || return 1

  state="$(jq -r '.state // ""' <<<"$entry" | tr '[:upper:]' '[:lower:]')"
  case "$state" in
    done | canceled | cancelled) return 0 ;;
    *) return 1 ;;
  esac
}

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

  # 1. Manifest active_step if unshipped and not already archived. An archived
  #    but unmarked active step is a half-done closeout; status/doctor surface
  #    that drift, while next should continue to the next actionable step.
  if [[ -n "$active_step_id" ]]; then
    local step
    step="$(wip_roadmap_step "$doc" "$active_step_id")"
    if [[ -n "$step" && "$step" != "null" ]] &&
      [[ "$(jq -r '.shipped' <<<"$step")" == "false" ]] &&
      ! _wip_archived_workplan_exists "$root/.wip/initiatives/$slug/archive" "$active_step_id"; then
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

  # Walk every unshipped step in declared order; skip the manifest active step
  # when it was already surfaced above, or when it is a half-done closeout.
  local unshipped
  unshipped="$(wip_roadmap_unshipped_after "$doc" "")"
  unshipped="$(jq -c --arg active "$active_step_id" '
    map(select(.id != $active))
  ' <<<"$unshipped")"

  # When there is no manifest active step, the first unshipped step is the
  # actionable one; its round + lane anchor the lane-aware ranking below.
  if [[ "$active_is_manifest" == "0" ]]; then
    local first
    first="$(jq -c '.[0] // null' <<<"$unshipped")"
    if [[ "$first" != "null" ]]; then
      active_lane="$(jq -r '.lane // ""' <<<"$first")"
      active_round="$(jq -r '.round_n // ""' <<<"$first")"
    fi
  fi

  # Pre-lane foreshadow (BDS-16): when the active step is main-lane (no
  # active_lane) and the active round has 2+ lanes with unshipped work, the
  # upcoming lane steps are parallelizable. We surface that one step early — from
  # the prereq itself — by marking those lane-step candidates concurrent, rather
  # than waiting until active_step is already inside a lane (ADR-0010 §7).
  local foreshadow_steps="[]"
  if [[ -n "$active_round" ]]; then
    foreshadow_steps="$(jq -c --argjson n "$active_round" '
      (.rounds[] | select(.n == $n)) as $r
      | [ $r.lanes[] as $ln
          | ($r.steps | map(select(.shipped == false and .lane == $ln)) | (.[0].id // empty)) ]
    ' <<<"$doc")"
  fi
  local foreshadow=0
  [[ -z "$active_lane" && "$(jq -r 'length' <<<"$foreshadow_steps")" -ge 2 ]] && foreshadow=1

  # 2/3/4. Rank remaining unshipped roadmap steps.
  local first_seen=0

  local count
  count="$(jq -r 'length' <<<"$unshipped")"
  if [[ "$count" == "0" && "$rank" == "1" ]]; then
    # No actionable step and no manifest active step. Distinguish an UNAUTHORED
    # roadmap (a fresh brief whose roadmap is still the empty skeleton — zero
    # steps anywhere) from a COMPLETE one (every authored step shipped). The
    # former needs authoring, not a "start the next round / close" nudge — this
    # is the Brief → Roadmap gap a fresh `init` / `intake --kind brief` lands in.
    local total_steps
    total_steps="$(jq -r '[.rounds[].steps[]] | length' <<<"$doc")"
    if [[ "$total_steps" == "0" ]]; then
      candidates="$(jq -c --argjson rank "$rank" --arg path "$roadmap_path" '
        . + [{rank:$rank, source:"scaffold", id:null, title:"author the roadmap",
              reason:"brief exists; roadmap has no steps yet", path:$path}]' <<<"$candidates")"
    else
      candidates="$(jq -c --argjson rank "$rank" '
        . + [{rank:$rank, source:"roadmap", id:null, title:"roadmap complete",
              reason:"start next round / close initiative"}]' <<<"$candidates")"
    fi
    rank=$((rank + 1))
  else
    local i
    for ((i = 0; i < count; i++)); do
      local entry reason rn lane eid concurrent=0
      entry="$(jq -c --argjson i "$i" '.[$i]' <<<"$unshipped")"
      rn="$(jq -r '.round_n' <<<"$entry")"
      lane="$(jq -r '.lane // ""' <<<"$entry")"
      eid="$(jq -r '.id' <<<"$entry")"
      if [[ "$rn" == "$active_round" && "$foreshadow" == "1" && -n "$lane" ]] &&
        [[ "$(jq -r --arg id "$eid" 'index($id) != null' <<<"$foreshadow_steps")" == "true" ]]; then
        # Pre-lane vantage with 2+ in-flight lanes: foreshadow that the upcoming
        # first step in each lane runs concurrently, from the main-lane prereq
        # itself (BDS-16). Later same-lane steps remain sequential.
        reason="concurrent lane $lane"
        concurrent=1
      elif [[ "$first_seen" == "0" && -z "$active_lane" ]]; then
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

  # 5. Roadmap backlog entries. A closed tracker is skipped BEFORE the append, so
  #    `rank` stays contiguous by construction (no renumbering pass).
  local backlog_count
  backlog_count="$(jq -r '.backlog | length' <<<"$doc")"
  local j
  for ((j = 0; j < backlog_count; j++)); do
    local b btracker
    b="$(jq -c --argjson j "$j" '.backlog[$j]' <<<"$doc")"
    btracker="$(jq -r '.tracker // ""' <<<"$b")"
    if _wip_next_tracker_closed "$root" "$btracker"; then
      continue
    fi
    candidates="$(jq -c \
      --argjson rank "$rank" --argjson b "$b" '
      . + [{rank:$rank, source:"backlog", id:$b.id, title:$b.title,
            reason:"roadmap backlog"}]' <<<"$candidates")"
    rank=$((rank + 1))
  done

  # 6. Repo backlog (.wip/backlog.md) if present. Its entries are multi-paragraph
  #    prose blocks with the tracker on a trailing line — a DIFFERENT grammar from
  #    the roadmap's one-line `## Backlog` bullets, so it gets its own parser.
  #    `wip_roadmap_parse` never matched this file at all (its backlog mode arms on
  #    a `## Backlog` H2, which the real file does not have), so the path that
  #    actually ran here was a bare-slugify fallback that read no tracker of either
  #    form — which is exactly why a shipped item kept being re-nominated.
  local repo_backlog="$root/.wip/backlog.md"
  if [[ -f "$repo_backlog" ]]; then
    local repo_entries rb_count k
    repo_entries="$(_wip_repo_backlog_parse "$repo_backlog")"
    rb_count="$(jq -r 'length' <<<"$repo_entries")"
    for ((k = 0; k < rb_count; k++)); do
      local b btracker
      b="$(jq -c --argjson k "$k" '.[$k]' <<<"$repo_entries")"
      btracker="$(jq -r '.tracker // ""' <<<"$b")"
      if _wip_next_tracker_closed "$root" "$btracker"; then
        continue
      fi
      candidates="$(jq -c \
        --argjson rank "$rank" --argjson b "$b" '
        . + [{rank:$rank, source:"backlog", id:$b.id, title:$b.title,
              reason:"repo backlog"}]' <<<"$candidates")"
      rank=$((rank + 1))
    done
  fi

  # Deferred items (## Deferred in the roadmap) — a clearly NOT-actionable
  # bucket, emitted separately from candidates so they are never ranked or
  # nominated as the next step (BDS-17). Pass through {id,title} verbatim.
  local deferred
  deferred="$(jq -c '.deferred' <<<"$doc")"

  jq -nc --arg slug "$slug" --argjson candidates "$candidates" \
    --argjson deferred "$deferred" '
    { ok: true, initiative: $slug, candidates: $candidates, deferred: $deferred }'
}
