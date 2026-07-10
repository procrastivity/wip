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
# issue comes from the `.wip.yaml` tracker_map mirror (steps + rounds, ADR-0024)
# UNIONED with the intake-anchored `initiative` node (see below); semantic_state
# from the cache floor (null when no cache entry); target_state from the provider
# mapping. Nodes without a tracker mapping are skipped.
_wip_tracker_bind_plan() {
  local root="$1" mj="$2" slug="$3" only="${4:-}"
  local backend mirror cache anchor
  backend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"
  mirror="$(_wip_tracker_map_from_manifest "$mj" "$slug")"
  # Union the intake-anchored initiative node (ADR-0024 §D3). `tracker_anchor` is a
  # top-level initiative field — a SIBLING of tracker_map, NOT part of the
  # roadmap-derived mirror — with intake as its source of truth, so it is
  # deliberately absent from the `rmap == mmap` mirror-drift equality gate (in
  # sync / tracker map). We fold it in HERE, downstream of that gate, as node
  # `initiative` so sync / tracker bind surface and push-forward all three levels
  # (step / round / initiative). The anchor wins the `initiative` key on the
  # (structurally impossible) collision — the roadmap harvest only ever yields
  # `step-NN` / `round-N` keys.
  anchor="$(jq -r --arg s "$slug" \
    '[.initiatives[]? | select(.slug == $s)] | (.[0].tracker_anchor // "")' <<<"$mj")"
  if [[ -n "$anchor" ]]; then
    mirror="$(jq -c --arg a "$anchor" '. + { initiative: $a }' <<<"$mirror")"
  fi
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
# backend, or "" when none (the agent/MCP path). Resolution precedence
# (ADR-0026 §Decision 2), highest to lowest:
#   1. WIP_TRACKER_READ_CMD — generic, any-backend test/process seam.
#   2. the per-backend adapter fn `_wip_tracker_<backend>_read_cmd` (loaded from
#      lib/wip/tracker-backends/ by the loader below), which honors its own
#      WIP_<BACKEND>_READ_CMD before emitting its default CLI string.
#   3. the inline linear arm — WIP_LINEAR_READ_CMD, else "" (the MCP path;
#      Linear's own bare-CLI wrapper stays deferred to BDS-23).
# A non-linear backend with no adapter loaded falls through to rung 3's default
# (""). Linear stays inline deliberately (ADR-0026): minimal churn, unchanged
# empty-by-default MCP behavior.
_wip_tracker_transport_read_cmd() {
  local backend="$1"
  if [[ -n "${WIP_TRACKER_READ_CMD:-}" ]]; then
    printf '%s' "$WIP_TRACKER_READ_CMD"
    return 0
  fi
  local fn="_wip_tracker_${backend//-/_}_read_cmd"
  if declare -F "$fn" >/dev/null 2>&1; then
    "$fn"
    return 0
  fi
  case "$backend" in
    linear) printf '%s' "${WIP_LINEAR_READ_CMD:-}" ;;
    *) ;;
  esac
}

# _wip_tracker_transport_write_cmd <backend> — the live write shell-out for a
# backend, or "" when none. Same precedence as the read dispatcher above:
# WIP_TRACKER_WRITE_CMD → `_wip_tracker_<backend>_write_cmd` → inline linear
# (WIP_LINEAR_WRITE_CMD, else "").
_wip_tracker_transport_write_cmd() {
  local backend="$1"
  if [[ -n "${WIP_TRACKER_WRITE_CMD:-}" ]]; then
    printf '%s' "$WIP_TRACKER_WRITE_CMD"
    return 0
  fi
  local fn="_wip_tracker_${backend//-/_}_write_cmd"
  if declare -F "$fn" >/dev/null 2>&1; then
    "$fn"
    return 0
  fi
  case "$backend" in
    linear) printf '%s' "${WIP_LINEAR_WRITE_CMD:-}" ;;
    *) ;;
  esac
}

# --- Per-backend adapter loader (ADR-0026 §Decision 2) -----------------------
# Each lib/wip/tracker-backends/<name>.bash defines
# `_wip_tracker_<name>_{read,write}_cmd`, dispatched by the two functions above.
# Glob-sourced from this lib so adapters auto-load wherever the transport lib is
# sourced (the bin AND the direct-source tests) — no bin/wip-plumbing edit. The
# dir may be absent or contain no *.bash (e.g. in this substrate, before any
# backend adapter ships); the -d guard and the no-match -e check tolerate both.
# The load path is exercised for real by the backend lanes' own tests
# (test-tracker-github.sh / test-tracker-gitlab.sh).
_wip_tracker_backends_dir="${BASH_SOURCE[0]%/*}/tracker-backends"
if [[ -d "$_wip_tracker_backends_dir" ]]; then
  for _wip_tb in "$_wip_tracker_backends_dir"/*.bash; do
    [[ -e "$_wip_tb" ]] || continue # tolerate the no-match glob
    # shellcheck disable=SC1090
    source "$_wip_tb"
  done
  unset _wip_tb
fi
unset _wip_tracker_backends_dir
