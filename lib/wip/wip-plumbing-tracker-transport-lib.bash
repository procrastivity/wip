# wip-plumbing-tracker-transport-lib.bash — the issue-tracker transport adapter
# (ADR-0019 §4, BRIEF §4). Plumbing stays pure: it resolves a provider-agnostic
# lifecycle intent into a concrete **bind plan** (which issue, which provider
# state) and resolves the read/write shell-out seams — but it never makes the
# call. The agent/MCP path (default) executes the plan via the Linear MCP
# connector; a wrap-the-CLI path is deferred to BDS-23. Tests inject the
# WIP_LINEAR_{READ,WRITE}_CMD seams so the read/write paths never touch a network.
# shellcheck shell=bash

# _wip_tracker_provider_state <backend> <semantic> — map a semantic state
# (todo|in-progress|in-review|done|canceled, ADR-0019 §B) to the provider's
# concrete state name. Unknown backend → passthrough; unknown state → empty.
_wip_tracker_provider_state() {
  local backend="$1" sem="$2"
  case "$backend" in
    linear)
      case "$sem" in
        todo) printf 'Todo' ;;
        in-progress) printf 'In Progress' ;;
        in-review) printf 'In Review' ;;
        'done') printf 'Done' ;;
        canceled) printf 'Canceled' ;;
        *) ;;
      esac
      ;;
    *) printf '%s' "$sem" ;;
  esac
}

# _wip_tracker_semantic_rank <semantic> — the lifecycle order used to keep `sync`
# push-forward-only: todo(0) < in-progress(1) < in-review(2) < done(3). Unknown
# or canceled → -1 (never an automatic forward target).
_wip_tracker_semantic_rank() {
  case "$1" in
    todo) printf '0' ;;
    in-progress) printf '1' ;;
    in-review) printf '2' ;;
    'done') printf '3' ;;
    *) printf -- '-1' ;;
  esac
}

# _wip_tracker_provider_to_semantic <backend> <provider-state> — the reverse of
# _wip_tracker_provider_state: a provider's concrete state name → wip's semantic
# vocabulary, so `sync` can rank the tracker's current state. Unknown → empty.
_wip_tracker_provider_to_semantic() {
  local backend="$1" ps="$2"
  case "$backend" in
    linear)
      case "$ps" in
        Todo) printf 'todo' ;;
        "In Progress") printf 'in-progress' ;;
        "In Review") printf 'in-review' ;;
        Done) printf 'done' ;;
        Canceled) printf 'canceled' ;;
        *) ;;
      esac
      ;;
    *) printf '%s' "$ps" ;;
  esac
}

# _wip_tracker_bind_plan <root> <mj> <slug> [<node>] — echo a JSON array of bind
# plans, one per mapped node (or just <node> when given). Each:
#   {node, issue, semantic_state, target_state}
# issue comes from the `.wip.yaml` tracker_map mirror; semantic_state from the
# cache floor (null when no cache entry); target_state from the provider mapping.
# Nodes without a tracker mapping are skipped.
_wip_tracker_bind_plan() {
  local root="$1" mj="$2" slug="$3" only="${4:-}"
  local backend mirror cache
  backend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"
  mirror="$(_wip_tracker_map_from_manifest "$mj" "$slug")"
  cache="$(_wip_tracker_cache_read "$root")"

  local out="[]" node issue sem target entry
  while IFS=$'\t' read -r node issue; do
    [[ -n "$node" ]] || continue
    [[ -n "$only" && "$node" != "$only" ]] && continue
    sem="$(jq -r --arg k "$slug/$node" '.[$k].state // ""' <<<"$cache")"
    target="$(_wip_tracker_provider_state "$backend" "$sem")"
    entry="$(jq -nc \
      --arg node "$slug/$node" --arg issue "$issue" \
      --arg sem "$sem" --arg target "$target" '
      { node: $node, issue: $issue,
        semantic_state: (if $sem == "" then null else $sem end),
        target_state: (if $target == "" then null else $target end) }')"
    out="$(jq -nc --argjson a "$out" --argjson e "$entry" '$a + [$e]')"
  done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"$mirror")
  printf '%s' "$out"
}

# _wip_tracker_transport_read_cmd <backend> — the live read shell-out for a
# backend, or "" when none (the agent/MCP path). WIP_LINEAR_READ_CMD overrides
# (test seam). No default CLI: the bare-CLI adapter is deferred to BDS-23.
_wip_tracker_transport_read_cmd() {
  case "$1" in
    linear) printf '%s' "${WIP_LINEAR_READ_CMD:-}" ;;
    *) ;;
  esac
}

# _wip_tracker_transport_write_cmd <backend> — the live write shell-out for a
# backend, or "" when none. WIP_LINEAR_WRITE_CMD overrides (test seam).
_wip_tracker_transport_write_cmd() {
  case "$1" in
    linear) printf '%s' "${WIP_LINEAR_WRITE_CMD:-}" ;;
    *) ;;
  esac
}
