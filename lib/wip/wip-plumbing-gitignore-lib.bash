# wip-plumbing-gitignore-lib.bash — the `gitignore sync` verb's writer: makes
# the manifest's `gitignore.always_commit` policy real in `.gitignore`.
# Sourced by bin/wip-plumbing. Contract: workplan step-05 (closeout-write-ladder).
#
# The policy this enforces is a standing repo-hygiene invariant, NOT a rung of
# the closeout ladder (ADR-0016) — `.wip.yaml` DECLARES which files under an
# otherwise-ignored `.wip/` stay tracked, and until now nothing on disk made that
# declaration true. This lib is the "nothing" that was missing.
#
# Why a marker block rather than free-floating `!` lines: the exceptions are
# GENERATED from the manifest, so they must be findable again to be rewritten
# when the declared list changes. A delimited block is what makes the write
# idempotent (re-run = byte-identical file) and reversible (list emptied = block
# removed) without ever disturbing a hand-authored line.
#
# The three-line shape is load-bearing and non-obvious. git does not descend into
# a directory that matches an ignore pattern, so a bare `!.wip/GLOSSARY.md` under
# a blanket `.wip/` ignore has NO EFFECT — the directory is never opened, so the
# finer pattern is never evaluated. The block therefore emits, in order:
#   `!.wip/`   — re-open `.wip/` for descent (last-match-wins over `.wip/`)
#   `.wip/*`   — immediately re-ignore every DIRECT child, so nothing else under
#                `.wip/` silently becomes trackable as a side effect
#   `!<path>`  — punch the specific declared files back through
# and is spliced immediately AFTER the bare `.wip/` blanket-ignore line, so git's
# last-match-wins rule resolves it in that order.
#
# Scope limit (deliberate, see the workplan's scope-limitation decision): the
# un-ignore/re-ignore pair re-opens exactly ONE level of descent, so only
# top-level `.wip/<file>` entries can be expressed. A nested entry
# (`.wip/initiatives/x/NOTES.md`) would need one such pair per ancestor
# directory, which this generator does not implement — so it REFUSES such an
# entry loudly (return 1) rather than emitting an exception line that would
# silently not work.
# shellcheck shell=bash

# The block delimiters. The begin line is matched by PREFIX (not equality) so a
# future cosmetic edit to its parenthetical can still find and replace an older
# block rather than orphaning it; the end line is matched exactly.
_WIP_GITIGNORE_BEGIN='# --- wip: gitignore.always_commit exceptions (generated; do not hand-edit) ---'
_WIP_GITIGNORE_END='# --- end wip: gitignore.always_commit exceptions ---'
_WIP_GITIGNORE_BEGIN_RE='^# --- wip: gitignore\.always_commit exceptions'
_WIP_GITIGNORE_END_RE='^# --- end wip: gitignore\.always_commit exceptions ---$'

# The anchor the block is inserted after: the bare `.wip/` blanket-ignore line.
# Matched exactly — `.wip/*` or `.wip/initiatives/...` must never be mistaken for
# it, since inserting the block after those would put the un-ignore in the wrong
# place in git's last-match-wins order.
_WIP_GITIGNORE_ANCHOR_RE='^\.wip/$'

# _wip_gitignore_sync_always_commit <manifest> <gitignore-path>
#
# Generate/repair the `always_commit` exception block in <gitignore-path> from
# <manifest>'s `gitignore.always_commit` list. Prints a bare status word to
# stdout and returns 0; returns 1 with a message on stderr on internal error
# (unreadable manifest/gitignore, an unsupported entry, or a missing anchor).
#   updated — the block was inserted, replaced (its content differed), or removed
#             (the declared list is now empty and a stale block was present).
#   noop    — the block already matches what `always_commit` demands, byte for
#             byte (including the degenerate "empty list, no block" case); no
#             write is performed, so the file stays byte-identical.
# Never `skipped`: like `_wip_closeout_mark_shipped`, this seam has no "belongs to
# someone else" case — the file is either in the declared state or it is not.
#
# Honors $WIP_DRY_RUN=1: the status word is still computed and printed, but no
# file write happens.
_wip_gitignore_sync_always_commit() {
  local manifest="$1" gitignore="$2"

  [[ -f "$manifest" ]] || {
    printf 'wip-plumbing: gitignore: manifest missing: %s\n' "$manifest" >&2
    return 1
  }
  [[ -f "$gitignore" ]] || {
    printf 'wip-plumbing: gitignore: gitignore missing: %s\n' "$gitignore" >&2
    return 1
  }

  # --- Read + validate the declared list ------------------------------------
  # A missing `gitignore.always_commit` key is not an error — it is the empty
  # list, which canonically means "no block", i.e. the removal case.
  local raw
  raw="$(yq -r '(.gitignore.always_commit // [])[]' "$manifest" 2>/dev/null)" || {
    printf 'wip-plumbing: gitignore: cannot read gitignore.always_commit from %s\n' \
      "$manifest" >&2
    return 1
  }

  local -a declared=()
  local entry
  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    # Two distinct refusals, deliberately worded differently — a caller (and a
    # test) must be able to tell "you declared something outside .wip/" from
    # "you declared a nested path this generator cannot express".
    if [[ "$entry" != .wip/* ]]; then
      printf 'wip-plumbing: gitignore: always_commit entry is not under .wip/: %s\n' \
        "$entry" >&2
      return 1
    fi
    if [[ "$entry" =~ ^\.wip/.*/ ]]; then
      printf 'wip-plumbing: gitignore: nested always_commit entry not supported (top-level .wip/<file> only): %s\n' \
        "$entry" >&2
      return 1
    fi
    declared+=("$entry")
  done <<<"$raw"

  # Sort for deterministic output: the block's content is compared byte-for-byte
  # to decide noop-vs-updated, so a stable order is what keeps a re-run quiet
  # when the manifest merely lists the same paths in a different order.
  if ((${#declared[@]} > 0)); then
    local -a sorted=()
    while IFS= read -r entry; do
      [[ -n "$entry" ]] && sorted+=("$entry")
    done < <(printf '%s\n' "${declared[@]}" | LC_ALL=C sort)
    declared=("${sorted[@]}")
  fi

  # --- Build the canonical block --------------------------------------------
  # An empty declared list yields an EMPTY block (no lines at all) — that is what
  # makes "list emptied → stale block removed" fall out of the same comparison.
  local -a block=()
  if ((${#declared[@]} > 0)); then
    block=("$_WIP_GITIGNORE_BEGIN" '!.wip/' '.wip/*')
    for entry in "${declared[@]}"; do
      block+=("!${entry}")
    done
    block+=("$_WIP_GITIGNORE_END")
  fi

  # --- Read the current file -------------------------------------------------
  local -a lines=()
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    lines+=("$line")
  done <"$gitignore"

  # --- Locate an existing block ----------------------------------------------
  local begin=-1 end=-1 i n=${#lines[@]}
  for ((i = 0; i < n; i++)); do
    if ((begin < 0)) && [[ "${lines[i]}" =~ $_WIP_GITIGNORE_BEGIN_RE ]]; then
      begin=$i
      continue
    fi
    if ((begin >= 0)) && [[ "${lines[i]}" =~ $_WIP_GITIGNORE_END_RE ]]; then
      end=$i
      break
    fi
  done

  # A begin with no end is a corrupted block: splicing against it would eat the
  # rest of the file. Refuse loudly rather than guess where it was meant to stop.
  if ((begin >= 0 && end < 0)); then
    printf 'wip-plumbing: gitignore: unterminated generated block in %s (found the begin marker, no end marker)\n' \
      "$gitignore" >&2
    return 1
  fi

  # --- Decide the status -----------------------------------------------------
  local status="updated"
  if ((begin >= 0)); then
    # Block present: `noop` iff its current content already equals the canonical
    # block exactly. (A canonical-empty block can never equal a present one, so
    # the "list emptied, stale block" case correctly lands on `updated`.)
    local -a current=()
    for ((i = begin; i <= end; i++)); do current+=("${lines[i]}"); done
    if ((${#current[@]} == ${#block[@]})); then
      status="noop"
      for ((i = 0; i < ${#current[@]}; i++)); do
        if [[ "${current[i]}" != "${block[i]}" ]]; then
          status="updated"
          break
        fi
      done
    fi
  else
    # No block present: nothing to do iff the canonical block is also empty.
    ((${#block[@]} == 0)) && status="noop"
  fi

  # --- Resolve the insertion anchor ------------------------------------------
  # Done BEFORE the dry-run short-circuit, not inside the splice: a missing anchor
  # is an internal error the caller must hear about under `--dry-run` too. A
  # dry-run that cheerfully reports `updated` for a write that could never have
  # landed is exactly the lie dry-run exists to prevent.
  local anchor=-1
  if ((begin < 0 && ${#block[@]} > 0)); then
    for ((i = 0; i < n; i++)); do
      if [[ "${lines[i]}" =~ $_WIP_GITIGNORE_ANCHOR_RE ]]; then
        anchor=$i
        break
      fi
    done
    if ((anchor < 0)); then
      printf 'wip-plumbing: gitignore: no bare .wip/ blanket-ignore line found in %s — the always_commit policy assumes one exists to un-ignore against\n' \
        "$gitignore" >&2
      return 1
    fi
  fi

  if [[ "$status" == "noop" || "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf '%s' "$status"
    return 0
  fi

  # --- Splice ----------------------------------------------------------------
  local -a out=()
  if ((begin >= 0)); then
    # Replace [begin, end] with the canonical block (which may be empty — that is
    # the removal case, and it drops the marker lines entirely).
    for ((i = 0; i < begin; i++)); do out+=("${lines[i]}"); done
    ((${#block[@]} > 0)) && out+=("${block[@]}")
    for ((i = end + 1; i < n; i++)); do out+=("${lines[i]}"); done
  else
    # Fresh insertion: the block lands immediately after the bare `.wip/`
    # blanket-ignore line, so git resolves `!.wip/` as the LATER match.
    for ((i = 0; i <= anchor; i++)); do out+=("${lines[i]}"); done
    out+=("${block[@]}")
    for ((i = anchor + 1; i < n; i++)); do out+=("${lines[i]}"); done
  fi

  if ((${#out[@]} > 0)); then
    printf '%s\n' "${out[@]}" >"$gitignore"
  else
    : >"$gitignore"
  fi

  printf '%s' "$status"
  return 0
}
