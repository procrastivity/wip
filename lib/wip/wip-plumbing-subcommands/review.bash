# review — the In-Review surface of the tracker lifecycle (ADR-0019 §A/§D, BRIEF
# §5). `review list` shows the nodes currently In Review (from the cache floor);
# `review complete <node>` is the manual Tier-0 Done gate — it emits a
# {to:done, reason:review-complete} intent. These operate on wip's lifecycle
# cache directly (an explicit user action, so not gated on issue-tracker the way
# the automatic ship/activate emission is). Deterministic; `complete` writes.
# shellcheck shell=bash

# wip_plumbing_cmd_review <subcommand> [args]
wip_plumbing_cmd_review() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_die 2 usage "review: missing subcommand (list | complete)"
  shift
  case "$sub" in
    list) _wip_review_cmd_list "$@" ;;
    complete) _wip_review_cmd_complete "$@" ;;
    *) wip_die 2 usage "review: unknown subcommand: $sub" ;;
  esac
}

# Resolve root + slug (default current_initiative). Echoes "<root>\037<slug>" on
# one line (mj is not returned — it is multi-line and would break a `read`; no
# review subcommand needs it past slug resolution). Dies on the usual envelopes.
_wip_review_resolve() {
  local slug="$1" root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative "review: no current_initiative; pass --initiative <slug>"
  fi
  [[ "$(jq -c --arg s "$slug" '[.initiatives[]? | select(.slug == $s)] | (.[0] // null)' <<<"$mj")" != "null" ]] ||
    wip_die 3 unknown-initiative "review: initiative not in manifest: $slug"
  printf '%s\037%s' "$root" "$slug"
}

# review list [--initiative <slug>] — nodes whose cached lifecycle state is
# in-review, scoped to the initiative. Emits {ok, initiative, in_review:[...]}.
_wip_review_cmd_list() {
  local slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "review list: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "review list: unknown flag: $1" ;;
      *) wip_die 2 usage "review list: unexpected arg: $1" ;;
    esac
  done

  local root resolved
  resolved="$(_wip_review_resolve "$slug")"
  IFS=$'\037' read -r root slug <<<"$resolved"

  local cache in_review
  cache="$(_wip_tracker_cache_read "$root")"
  in_review="$(jq -c --arg p "$slug/" '
    [ to_entries[]
      | select(.key | startswith($p))
      | select(.value.state == "in-review")
      | { node: .key, state: .value.state, reason: .value.reason, updated: .value.updated } ]
  ' <<<"$cache")"

  jq -nc --arg slug "$slug" --argjson ir "$in_review" \
    '{ ok: true, initiative: $slug, in_review: $ir }'
}

# review complete <step-id> [--initiative <slug>] — the manual Done gate. Emits a
# {to:done, reason:review-complete} intent into the cache floor. Reports whether
# the node was actually In Review first (a complete from another state still
# advances, but the flag surfaces an out-of-order completion).
_wip_review_cmd_complete() {
  local slug="" step_id=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "review complete: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "review complete: unknown flag: $1" ;;
      *)
        if [[ -z "$step_id" ]]; then step_id="$1"; else wip_die 2 usage "review complete: unexpected arg: $1"; fi
        shift
        ;;
    esac
  done
  [[ -n "$step_id" ]] || wip_die 2 usage "review complete: missing <node>"

  local root resolved
  resolved="$(_wip_review_resolve "$slug")"
  IFS=$'\037' read -r root slug <<<"$resolved"

  local prev was_in_review="false" intent
  prev="$(_wip_tracker_cache_get "$root" "$slug/$step_id")"
  [[ "$(jq -r '.state // ""' <<<"$prev")" == "in-review" ]] && was_in_review="true"

  if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
    intent="$(_wip_tracker_emit_intent "$root" "$slug" "$step_id" "done" "review-complete" "$(wip_scaffold_now)")"
  else
    intent="$(jq -nc --arg n "$slug/$step_id" '{node:$n, to:"done", reason:"review-complete"}')"
  fi

  jq -nc --arg slug "$slug" --argjson intent "$intent" --argjson wir "$was_in_review" \
    '{ ok: true, initiative: $slug, node: $intent.node, intent: $intent, was_in_review: $wir }'
}
