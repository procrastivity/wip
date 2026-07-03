# forge — observe a forge (push / PR / merge) and map it to a lifecycle
# transition INTENT (ADR-0018). wip OBSERVES; it never owns the push, and it
# never writes the Linear status (that consumer is BDS-20). Wraps gh/glab via the
# transport seam (wip-plumbing-forge-lib.bash). Deterministic given the seam.
# shellcheck shell=bash

# wip_plumbing_cmd_forge <subcommand> [args]
wip_plumbing_cmd_forge() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_die 2 usage "forge: missing subcommand (observe)"
  shift
  case "$sub" in
    observe) _wip_forge_cmd_observe "$@" ;;
    *) wip_die 2 usage "forge: unknown subcommand: $sub" ;;
  esac
}

# forge observe [--initiative <slug>] [--branch <branch>]
#
# Resolves the initiative (default current_initiative) and the branch (default
# the current git branch), asks the forge for that branch's PR/MR state via the
# transport seam, and maps it to a transition intent:
#   merged            -> "done"      (Tier-1 equivalent of `review complete`)
#   open              -> "in-review" (Tier-1 equivalent of `wip ship`)
#   closed-unmerged   -> "none"      (+ pr-closed-unmerged signal)
#   no PR / no answer -> "none"
# Emits a flat JSON envelope; the Linear write that consumes `intent` is BDS-20's.
_wip_forge_cmd_observe() {
  local slug="" branch=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "forge: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      --branch)
        [[ $# -ge 2 ]] || wip_die 2 usage "forge: --branch requires an argument"
        branch="$2"
        shift 2
        ;;
      --branch=*)
        branch="${1#--branch=}"
        shift
        ;;
      -*) wip_die 2 usage "forge: unknown flag: $1" ;;
      *) wip_die 2 usage "forge: unexpected arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative "forge: no current_initiative; pass --initiative <slug>"
  fi
  local init_record
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "forge: initiative not in manifest: $slug"

  local forge_available
  forge_available="$(jq -r '.features.forge.enabled // false' <<<"$mj")"

  # Default branch = current git branch (best-effort; empty outside a repo).
  if [[ -z "$branch" ]] && command -v git >/dev/null 2>&1; then
    branch="$(git -C "$root" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
  fi

  # Transport: detect CLI, resolve the observe command, run it. A non-zero exit
  # or empty output means "no PR / no answer" for observation purposes; liveness
  # belongs to `status --probe-forge`, so do not collapse that into unreachable.
  local cli observe_cmd raw="" reachable="null" forge_backend
  forge_backend="$(jq -r '.features.forge.backend // ""' <<<"$mj")"
  cli="$(_wip_forge_detect "$forge_backend")"
  observe_cmd="$(_wip_forge_observe_cmd "$cli" "$branch")"
  if [[ -n "$observe_cmd" ]]; then
    if raw="$(_wip_forge_run "$observe_cmd")" && [[ -n "$raw" ]]; then
      reachable="true"
    else
      raw=""
    fi
  fi

  # Normalize gh (state OPEN/MERGED/CLOSED + mergedAt) and glab (state
  # opened/merged/closed + merged_at) into one shape, then map to intent.
  local observed="null" intent="none" signals="[]"
  if [[ -n "$raw" ]]; then
    observed="$(jq -c '{
      state: (.state // null),
      merged_at: (.mergedAt // .merged_at // null),
      url: (.url // .web_url // null)
    }' <<<"$raw" 2>/dev/null || printf 'null')"
  fi
  if [[ "$observed" != "null" ]]; then
    local st merged
    st="$(jq -r '(.state // "") | ascii_downcase' <<<"$observed")"
    merged="$(jq -r 'if .merged_at != null then "true" else "false" end' <<<"$observed")"
    if [[ "$merged" == "true" || "$st" == "merged" ]]; then
      intent="done"
    elif [[ "$st" == "open" || "$st" == "opened" ]]; then
      intent="in-review"
    elif [[ "$st" == "closed" ]]; then
      intent="none"
      signals='["pr-closed-unmerged"]'
    fi
  fi

  jq -nc \
    --arg slug "$slug" --arg branch "$branch" --arg cli "$cli" \
    --argjson available "$forge_available" --argjson reachable "$reachable" \
    --argjson observed "$observed" --arg intent "$intent" \
    --argjson signals "$signals" '
    {
      ok: true,
      initiative: $slug,
      branch: (if $branch == "" then null else $branch end),
      forge: {
        cli: (if $cli == "" then null else $cli end),
        available: $available,
        reachable: $reachable
      },
      observed: $observed,
      intent: $intent,
      signals: $signals
    }'
}
