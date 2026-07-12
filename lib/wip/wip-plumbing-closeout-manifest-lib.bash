# wip-plumbing-closeout-manifest-lib.bash — the `closeout` verb's manifest
# writers: the initiative-level rung of the closeout ladder. Sourced by
# bin/wip-plumbing. Contract: ADR-0016; workplan step-04.
#
# Deliberately NOT folded into wip-plumbing-ship-manifest-lib.bash: that file's
# one scope is per-STEP manifest writes (clear `active_step` when it names the
# step just shipped). These writers gate on different predicates (a status
# transition; "is there exactly one other in-flight initiative") and write
# different keys (`status`, `current_initiative`) — initiative-level lifecycle
# state, not step state.
#
# Every seam here follows the same contract `_wip_ship_clear_active_step`
# established: PRINT a bare status word to stdout and return 0; return 1 on
# internal error. All targeting goes through `select(.slug == strenv(SLUG))`, so
# a write on <slug> can never touch another initiative's fields. Every seam
# honors $WIP_DRY_RUN=1 by computing and printing its status word while skipping
# the actual `yq -i` write.
#
# Validation of the <slug> and <next-slug> arguments (does the initiative exist;
# is it in-flight) is the CALLER's guard, not these seams' job — the same split
# `ship` uses, where `wip_plumbing_cmd_ship` refuses an unknown initiative before
# any writer runs. The seams do assert the initiative exists, but only as a
# backstop that turns a silently-no-op write into a loud internal error (1).
# shellcheck shell=bash

# _wip_closeout_initiative_exists <manifest> <slug> — 0 if <slug> names an
# initiative in the manifest, 1 otherwise. The backstop behind each seam's
# "would this write silently match nothing?" check.
_wip_closeout_initiative_exists() {
  local manifest="$1" slug="$2" n
  n="$(SLUG="$slug" yq -r '
    [.initiatives[] | select(.slug == strenv(SLUG))] | length
  ' "$manifest" 2>/dev/null)" || return 1
  [[ "$n" == "0" || -z "$n" ]] && return 1
  return 0
}

# _wip_closeout_guard <manifest> <slug> <verb> — shared precondition check for
# the writer seams: the manifest is readable and <slug> names a real initiative.
# Returns 1 (with a message on stderr) when either fails.
_wip_closeout_guard() {
  local manifest="$1" slug="$2" verb="$3"
  [[ -f "$manifest" ]] || {
    printf 'wip-plumbing: closeout: manifest missing: %s\n' "$manifest" >&2
    return 1
  }
  _wip_closeout_initiative_exists "$manifest" "$slug" || {
    printf 'wip-plumbing: closeout: %s: no such initiative: %s\n' \
      "$verb" "$slug" >&2
    return 1
  }
  return 0
}

# _wip_closeout_inflight_candidates <manifest> <slug>
#
# Print the slugs (one per line, manifest order) of every OTHER initiative whose
# status is `in-flight` — i.e. the candidates `current_initiative` could be
# repointed at once <slug> is closed. Pure read, no write.
#
# Exposed (not private to the repoint seam) because the `closeout` verb needs the
# same list verbatim for its JSON ledger's `candidates` array in the `ambiguous`
# case; recomputing it there with a second, independently-written yq expression
# is exactly how the ledger and the write drift apart.
_wip_closeout_inflight_candidates() {
  local manifest="$1" slug="$2"
  SLUG="$slug" yq -r '
    [.initiatives[]
      | select(.slug != strenv(SLUG))
      | select(.status == "in-flight")
      | .slug] | .[]
  ' "$manifest" 2>/dev/null || return 1
  return 0
}

# _wip_closeout_mark_shipped <manifest> <slug> <comment>
#
# Set initiatives[slug].status to `shipped`, carrying <comment> as the YAML
# trailing line comment (pass the BARE comment text — no leading `# ` — which is
# what yq's `line_comment` reads and writes). Status words:
#   updated — status changed (from anything other than `shipped`), or the
#             comment text differs from what's already there.
#   noop    — status is already exactly `shipped` AND the comment is identical
#             (byte-for-byte re-run stability: a clean re-run must not churn the
#             file).
# Never `skipped`: this seam has no "belongs to someone else" case. The gate that
# could refuse this write (refuse-unless-all-shipped) is the verb's guard.
_wip_closeout_mark_shipped() {
  local manifest="$1" slug="$2" comment="$3"
  _wip_closeout_guard "$manifest" "$slug" "mark-shipped" || return 1

  local current_status current_comment
  current_status="$(SLUG="$slug" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status) // ""
  ' "$manifest" 2>/dev/null)" || return 1
  current_comment="$(SLUG="$slug" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status | line_comment) // ""
  ' "$manifest" 2>/dev/null)" || return 1
  [[ "$current_status" == "null" ]] && current_status=""

  if [[ "$current_status" == "shipped" && "$current_comment" == "$comment" ]]; then
    printf 'noop'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi

  SLUG="$slug" COMMENT="$comment" yq -i '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status) = "shipped" |
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status) line_comment = strenv(COMMENT)
  ' "$manifest" || return 1
  printf 'updated'
  return 0
}

# _wip_closeout_clear_active_step <manifest> <slug>
#
# Remove initiatives[slug].active_step UNCONDITIONALLY — unlike ship's version,
# no step-id match is required, because at closeout ANY leftover `active_step` on
# the initiative being closed is stale by definition. Status words:
#   updated — a key was present and has been removed.
#   noop    — already absent.
_wip_closeout_clear_active_step() {
  local manifest="$1" slug="$2"
  _wip_closeout_guard "$manifest" "$slug" "clear-active-step" || return 1

  local current
  current="$(SLUG="$slug" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""
  ' "$manifest" 2>/dev/null)" || return 1
  [[ "$current" == "null" ]] && current=""

  if [[ -z "$current" ]]; then
    printf 'noop'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi

  SLUG="$slug" yq -i '
    del(.initiatives[] | select(.slug == strenv(SLUG)) | .active_step)
  ' "$manifest" || return 1
  printf 'updated'
  return 0
}

# _wip_closeout_repoint_current_initiative <manifest> <slug> [<next-slug>]
#
# Resolve the top-level `current_initiative` pointer now that <slug> is closed.
# This seam carries a 4th status word beyond ship's 3-valued vocabulary, because
# "leave it alone and report the candidates" is a genuinely distinct outcome from
# "this pointer was never ours to touch":
#   updated   — the pointer was repointed, or cleared entirely.
#   noop      — the pointer already holds the requested value.
#   skipped   — `current_initiative` does not name <slug>; left untouched (the
#               same meaning ship's `skipped` carries: not ours to touch).
#   ambiguous — more than one other in-flight initiative; the pointer is left
#               UNCHANGED and the caller reports the candidates so a human picks.
#
# Resolution order (first match wins):
#   1. current_initiative != <slug>  → skipped. Never touch a pointer that isn't
#      already aimed at what we're closing. This outranks <next-slug>.
#   2. <next-slug> given             → repoint to it (updated), or noop if the
#      pointer already equals it (degenerate, but consistent).
#   3. Otherwise, count the OTHER in-flight initiatives:
#        exactly one → repoint to it (updated)
#        zero        → `del` the key entirely (updated) — a cleared pointer is
#                      itself meaningful "between initiatives" state, mirroring
#                      active_step's clear-on-finish convention
#        more than one → leave unchanged, ambiguous
#
# On `ambiguous` the caller gets the candidate slugs for its ledger by calling
# `_wip_closeout_inflight_candidates` with the same arguments — that helper is
# the single source of truth this seam itself counts with, so the two can't
# drift. (The candidates deliberately do NOT ride on this seam's stdout: callers
# capture the status word via command substitution, which runs the seam in a
# subshell, so neither a second output line nor a global would survive intact.)
#
# Validating <next-slug> (it must exist, and must not itself be shipped/archived)
# is the VERB's guard, not this seam's — see the file header.
_wip_closeout_repoint_current_initiative() {
  local manifest="$1" slug="$2" next="${3:-}"
  _wip_closeout_guard "$manifest" "$slug" "repoint" || return 1

  local current
  current="$(yq -r '.current_initiative // ""' "$manifest" 2>/dev/null)" || return 1
  [[ "$current" == "null" ]] && current=""

  # 1. The pointer isn't aimed at what we're closing — not ours to touch.
  if [[ "$current" != "$slug" ]]; then
    printf 'skipped'
    return 0
  fi

  # 2. An explicit --next wins over auto-resolution.
  if [[ -n "$next" ]]; then
    if [[ "$current" == "$next" ]]; then
      printf 'noop'
      return 0
    fi
    if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
      printf 'updated'
      return 0
    fi
    NEXT="$next" yq -i '.current_initiative = strenv(NEXT)' "$manifest" || return 1
    printf 'updated'
    return 0
  fi

  # 3. Auto-resolve from the other in-flight initiatives.
  local -a candidates=()
  local line
  while IFS= read -r line; do
    [[ -n "$line" ]] && candidates+=("$line")
  done < <(_wip_closeout_inflight_candidates "$manifest" "$slug")

  if ((${#candidates[@]} > 1)); then
    printf 'ambiguous'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi

  if ((${#candidates[@]} == 1)); then
    NEXT="${candidates[0]}" yq -i '.current_initiative = strenv(NEXT)' "$manifest" || return 1
  else
    # Zero others in flight: clear the pointer entirely. Absence is the state.
    yq -i 'del(.current_initiative)' "$manifest" || return 1
  fi
  printf 'updated'
  return 0
}
