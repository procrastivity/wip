# closeout — close out a whole INITIATIVE: flip its manifest `status` to
# `shipped` (with a synthesized trailing comment), clear any stale `active_step`,
# and resolve the top-level `current_initiative` pointer. The initiative-level
# rung of the closeout ladder; ADR-0016, workplan step-04.
#
# Unlike `ship` — a pure un-gated state-writer — this verb IS gated: it REFUSES
# unless every non-empty round of the initiative's roadmap carries the round-level
# `✅ shipped` heading marker. That guard is the safety-critical surface of this
# verb and lives HERE, before any writer seam runs (mirroring where ship's
# step-existence guard sits), not in the manifest-writer lib.
#
# closeout only ever WRITES `.wip.yaml`. The roadmap is read (never written) —
# `wip_roadmap_parse` supplies the guard's input and the comment's round/step
# counts.
# shellcheck shell=bash

# wip_plumbing_cmd_closeout <slug> [--next <slug>] [--pr <ref>] [--dry-run]
#
# Error codes: missing <slug> / unknown flag -> exit 2 (usage); unknown
# initiative -> exit 3 (unknown-initiative, mirroring ship); not every round
# shipped -> exit 4 (not-all-shipped); a `--next` value that names no initiative
# -> exit 4 (unknown-next-initiative); a `--next` value that is not in flight ->
# exit 4 (next-not-in-flight). The two `--next` refusals share exit 4 — the
# "state precondition failed" family — because writing `current_initiative` to a
# dangling or already-shipped slug is precisely the drift this whole verb exists
# to eliminate; the verb that fixes the drift must not be able to introduce its
# mirror image through its own flag.
#
# The `--next`-unknown word is deliberately NOT `unknown-initiative`: that word is
# reserved for the PRIMARY slug's lookup, which exits 3 in true parity with
# `wip_plumbing_cmd_ship`. One word, one exit code — a caller can branch on the
# status word alone.
wip_plumbing_cmd_closeout() {
  local slug="" next="" pr="" dry_run="${WIP_DRY_RUN:-0}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)
        dry_run=1
        shift
        ;;
      --next)
        [[ $# -ge 2 ]] || wip_die 2 usage "closeout: --next requires an argument"
        next="$2"
        shift 2
        ;;
      --next=*)
        next="${1#--next=}"
        shift
        ;;
      --pr)
        [[ $# -ge 2 ]] || wip_die 2 usage "closeout: --pr requires an argument"
        pr="$2"
        shift 2
        ;;
      --pr=*)
        pr="${1#--pr=}"
        shift
        ;;
      -*) wip_die 2 usage "closeout: unknown flag: $1" ;;
      *)
        if [[ -z "$slug" ]]; then
          slug="$1"
        else
          wip_die 2 usage "closeout: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done

  [[ -n "$slug" ]] || wip_die 2 usage "closeout: missing <slug>"

  # Thread --dry-run to every writer seam via the env var they read.
  WIP_DRY_RUN="$dry_run"
  export WIP_DRY_RUN

  # Resolve the initiative + its roadmap (idiom copied from `ship`).
  local root mj init_record roadmap_path
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "closeout: initiative not in manifest: $slug"
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  # --next validation. The repoint seam deliberately does NOT validate its
  # <next-slug> (see that lib's header); refusing a bad one is this guard's job.
  if [[ -n "$next" ]]; then
    local next_status
    next_status="$(jq -r --arg s "$next" '
      [.initiatives[]? | select(.slug == $s)] | (.[0].status // "")
    ' <<<"$mj")"
    if [[ "$(jq -r --arg s "$next" '[.initiatives[]? | select(.slug == $s)] | length' <<<"$mj")" == "0" ]]; then
      wip_die 4 unknown-next-initiative "closeout: --next names no initiative in the manifest: $next"
    fi
    # The initiative being closed is, by the end of this run, itself shipped —
    # pointing `current_initiative` at it is the same drift as pointing at any
    # other shipped initiative, so it is refused under the same status word.
    if [[ "$next" == "$slug" ]]; then
      wip_die 4 next-not-in-flight \
        "closeout: --next names the initiative being closed: $next"
    fi
    if [[ "$next_status" != "in-flight" ]]; then
      wip_die 4 next-not-in-flight \
        "closeout: --next is not in flight (status: ${next_status:-unset}): $next"
    fi
  fi

  # --- The refuse-unless-all-shipped guard --------------------------------
  # Every NON-EMPTY round's heading marker must be `✅ shipped`. Reading
  # `rounds[].shipped` — the round-level marker step-03's `ship` writes — rather
  # than re-deriving "all steps shipped" from the bullets is deliberate: it makes
  # closeout depend on the round-marker rung having actually FIRED, so a round
  # whose last step shipped under an old binary that never wrote the heading
  # marker is correctly refused rather than silently treated as closed.
  #
  # `length > 0` is load-bearing: a roadmap with zero rounds — or only EMPTY
  # rounds — must not vacuously pass (`all` over an empty array is `true`). Same
  # trap doctor's §2j check already guards.
  local doc all_shipped
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"
  all_shipped="$(jq -r '
    [.rounds[] | select((.steps | length) > 0)] | length > 0 and all(.shipped)
  ' <<<"$doc")"
  if [[ "$all_shipped" != "true" ]]; then
    local unshipped n_unshipped
    unshipped="$(jq -r '
      [.rounds[] | select((.steps | length) > 0) | select(.shipped != true) | .n]
      | map(tostring) | join(", ")
    ' <<<"$doc")"
    if [[ -z "$unshipped" ]]; then
      # No non-empty rounds at all — the vacuous case. Refused under the same
      # status word (one word for "this initiative is not closeable yet"), with a
      # message that names the actual reason.
      wip_die 4 not-all-shipped \
        "closeout: roadmap has no rounds with steps — nothing to close" "$roadmap_path"
    fi
    n_unshipped="$(jq -r '
      [.rounds[] | select((.steps | length) > 0) | select(.shipped != true)] | length
    ' <<<"$doc")"
    wip_die 4 not-all-shipped \
      "closeout: $n_unshipped round(s) not yet shipped: $unshipped" "$roadmap_path"
  fi

  # --- Trailing-comment synthesis -----------------------------------------
  # Deterministic from disk + $WIP_NOW: round count, total step count, date. The
  # PR reference is NOT derivable (closeout runs independent of any PR merge), so
  # it arrives via --pr and its clause is simply omitted when absent. No parser
  # reads this comment back — only the `status: shipped` scalar is load-bearing —
  # so the prose matches the semicolon-separated style already in .wip.yaml.
  local closed_date n_rounds n_steps
  closed_date="$(wip_scaffold_now)"
  n_rounds="$(jq -r '.rounds | length' <<<"$doc")"
  n_steps="$(jq -r '[.rounds[].steps[]] | length' <<<"$doc")"

  local round_clause step_clause comment
  if [[ "$n_rounds" == "1" ]]; then
    round_clause="its only round"
  else
    round_clause="$n_rounds rounds shipped"
  fi
  if [[ "$n_steps" == "1" ]]; then
    step_clause="1 step shipped"
  else
    step_clause="$n_steps steps shipped"
  fi
  comment="Round $n_rounds closed $closed_date ($round_clause; $step_clause)"
  [[ -n "$pr" ]] && comment="$comment; PR $pr"

  # --- The three writer seams ---------------------------------------------
  local manifest="$root/.wip.yaml"
  local status_set active_step_cleared current_action
  status_set="$(_wip_closeout_mark_shipped "$manifest" "$slug" "$comment")" ||
    wip_die 1 internal "closeout: status writer failed"
  active_step_cleared="$(_wip_closeout_clear_active_step "$manifest" "$slug")" ||
    wip_die 1 internal "closeout: active_step clear failed"

  # The pointer's PRE-run value and the in-flight candidate list are both read
  # before the repoint seam runs, because the ledger has to report the pointer's
  # resolved value even under --dry-run, where re-reading the file afterward would
  # report the un-written pre-state and silently contradict the action word.
  # `_wip_closeout_inflight_candidates` is the SAME helper the repoint seam counts
  # with internally (its status word and this ledger cannot drift apart); the seam
  # cannot hand the list back on its own stdout, since callers capture that status
  # word through a command substitution — a subshell.
  local current_before
  current_before="$(yq -r '.current_initiative // ""' "$manifest" 2>/dev/null)"
  [[ "$current_before" == "null" ]] && current_before=""
  local -a candidates=()
  local line
  while IFS= read -r line; do
    [[ -n "$line" ]] && candidates+=("$line")
  done < <(_wip_closeout_inflight_candidates "$manifest" "$slug")

  current_action="$(_wip_closeout_repoint_current_initiative "$manifest" "$slug" "$next")" ||
    wip_die 1 internal "closeout: current_initiative repoint failed"

  # The pointer's post-run value, computed from the same inputs the seam resolved
  # on (never a re-read — see above).
  local current_value=""
  case "$current_action" in
    updated)
      if [[ -n "$next" ]]; then
        current_value="$next"
      elif ((${#candidates[@]} == 1)); then
        current_value="${candidates[0]}"
      else
        current_value="" # zero others in flight: the pointer was cleared
      fi
      ;;
    # noop (pointer already holds the requested value), skipped (it never named
    # <slug>), ambiguous (left UNCHANGED for a human to pick) all keep whatever
    # the pointer already held.
    *) current_value="$current_before" ;;
  esac

  # `changed` is true iff ANY writer seam reported `updated` — one aggregation
  # rule over all three, the same idiom `ship` uses.
  local changed=false
  if [[ "$status_set" == "updated" || "$active_step_cleared" == "updated" ||
    "$current_action" == "updated" ]]; then
    changed=true
  fi

  # Locked flat JSON ledger (mirrors ship's emit style). `candidates` is present
  # only on `ambiguous` — the case where a human has to pick; `dry_run` only
  # under --dry-run.
  local candidates_json
  candidates_json="$(printf '%s\n' ${candidates[@]+"${candidates[@]}"} |
    jq -Rsc 'split("\n") | map(select(length > 0))')"

  jq -nc \
    --arg slug "$slug" --arg date "$closed_date" \
    --arg ss "$status_set" --arg asc "$active_step_cleared" \
    --arg ca "$current_action" --arg cv "$current_value" \
    --argjson cands "$candidates_json" \
    --argjson changed "$changed" --arg comment "$comment" --arg dry "$dry_run" '
    { ok: true, slug: $slug, closed_date: $date,
      status_set: $ss, active_step_cleared: $asc,
      current_initiative: (
        { action: $ca, value: (if $cv == "" then null else $cv end) }
        + (if $ca == "ambiguous" then { candidates: $cands } else {} end)
      ),
      changed: $changed, comment: $comment }
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}
