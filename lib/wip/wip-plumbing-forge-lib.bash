# Forge transport seam (ADR-0018): wip OBSERVES a forge by wrapping the gh/glab
# CLIs — it never owns the push. This lib is the shared Round 2 prereq both lanes
# build on: Lane A (status --probe-forge liveness) and Lane B (the forge verb's
# push/merge observation). It provides detection + command resolution + a single
# shell-out runner, all behind env seams so tests never touch a network or a real
# forge. Mirrors the solo-CLI probe shape (status.bash, ADR-0014).
# shellcheck shell=bash

# _wip_forge_detect [configured_cli] — which forge CLI is usable.
# Echoes "gh" | "glab" | "". Resolution order is env → config → probe:
#   1. WIP_FORGE_CLI env forces the answer (test seam / explicit process pin);
#      a *set-but-empty* WIP_FORGE_CLI forces "none" and short-circuits.
#   2. configured_cli (arg 1 — the committed .features.forge.backend pin) is
#      returned verbatim when non-empty. Authoritative as a *value*: it is NOT
#      re-validated against PATH, so a repo that pins `glab` gets glab even when
#      only gh is installed (the pin is a deliberate statement of which remote
#      this repo targets; reachability stays `status --probe-forge`'s concern).
#   3. binary probe: gh wins when both are present, else glab, else "".
# Load-bearing asymmetry vs. the env layer: an empty/absent arg 1 means "not
# configured" and falls through to the probe (reproducing today's zero-config
# gh-wins default), whereas a *set-but-empty* WIP_FORGE_CLI forces "none".
_wip_forge_detect() {
  if [[ -n "${WIP_FORGE_CLI+x}" ]]; then
    printf '%s' "${WIP_FORGE_CLI}"
    return 0
  fi
  local configured="${1:-}"
  if [[ -n "$configured" ]]; then
    printf '%s' "$configured"
    return 0
  fi
  if command -v gh >/dev/null 2>&1; then
    printf 'gh'
  elif command -v glab >/dev/null 2>&1; then
    printf 'glab'
  fi
}

# _wip_forge_status_cmd <cli> — the liveness probe command for <cli>, or "".
# WIP_FORGE_STATUS_CMD overrides for any cli (test seam); its presence makes the
# transport usable even when no real CLI is installed (mirrors status.bash:138).
_wip_forge_status_cmd() {
  local cli="${1:-}"
  if [[ -n "${WIP_FORGE_STATUS_CMD:-}" ]]; then
    printf '%s' "$WIP_FORGE_STATUS_CMD"
    return 0
  fi
  case "$cli" in
    gh) printf 'gh auth status' ;;
    glab) printf 'glab auth status' ;;
    *) ;;
  esac
}

# _wip_forge_observe_cmd <cli> <branch> — the PR/MR-state command for <cli>, or
# "". WIP_FORGE_OBSERVE_CMD overrides (test seam). Emits the minimal field set
# Lane B needs to map observed state -> transition intent: PR state + merge fact.
_wip_forge_observe_cmd() {
  local cli="${1:-}" branch="${2:-}"
  if [[ -n "${WIP_FORGE_OBSERVE_CMD:-}" ]]; then
    printf '%s' "$WIP_FORGE_OBSERVE_CMD"
    return 0
  fi
  case "$cli" in
    gh) printf 'gh pr view %q --json state,mergedAt,url' "$branch" ;;
    glab) printf 'glab mr view %q --output json' "$branch" ;;
    *) ;;
  esac
}

# _wip_forge_run <cmd> — execute a resolved forge command, echo its stdout. The
# single shell-out path shared by both lanes; stderr is swallowed (a missing CLI
# / auth failure is a normal, non-fatal "the forge didn't answer"). Returns the
# command's own exit status, or 2 when handed an empty command.
_wip_forge_run() {
  local cmd="${1:-}"
  [[ -n "$cmd" ]] || return 2
  bash -c "$cmd" 2>/dev/null
}
