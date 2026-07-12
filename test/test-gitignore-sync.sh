#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="gitignore-sync"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the `always_commit` gitignore generator (_wip_gitignore_sync_always_commit)
# driven directly as a unit — the `gitignore sync` verb does not exist yet (that
# is step-05 Chunk 2). Mirrors test-closeout-manifest-writer.sh's direct-seam
# idiom: the seam takes explicit <manifest> + <gitignore> paths, so there is no
# WIP_ROOT to root and the fixture paths are passed in directly.
#
# HARD BOUNDARY (Orchestrator-signed-off): every fixture here is a disposable
# tmp dir. This suite NEVER runs the generator against the live repo's real
# .gitignore, and never `git add`s the live .wip/backlog.md or .wip/GLOSSARY.md.
# The full-real-.gitignore case below RECONSTRUCTS the live file's content inside
# a throwaway `git init` repo; it does not touch the original.
#
# Contract: workplan step-05 (.wip/initiatives/closeout-write-ladder/workplans/
# step-05-enforce-the-always-commit-gitignore-policy.md), Chunk 1.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1

# shellcheck source=lib/wip/wip-plumbing-gitignore-lib.bash
source lib/wip/wip-plumbing-gitignore-lib.bash

BEGIN_LINE='# --- wip: gitignore.always_commit exceptions (generated; do not hand-edit) ---'
END_LINE='# --- end wip: gitignore.always_commit exceptions ---'

# setup [--no-anchor] [--no-key] [entry...]
#
# Write a fresh fixture into a new tmp dir; set the globals `manifest`,
# `gitignore`, and `before` (a byte-for-byte snapshot taken before the seam runs,
# so every no-write claim can be proven with `cmp` rather than inspection).
#
#   --no-anchor  omit the bare `.wip/` blanket-ignore line (the anchor-missing case)
#   --no-key     omit `gitignore.always_commit` entirely (vs. an explicit `[]`)
#   entry...     the declared always_commit paths (default: none → empty list)
setup() {
  local no_anchor=0 no_key=0
  local -a entries=()
  while (($#)); do
    case "$1" in
      --no-anchor)
        no_anchor=1
        shift
        ;;
      --no-key)
        no_key=1
        shift
        ;;
      *)
        entries+=("$1")
        shift
        ;;
    esac
  done

  local tmp
  tmp="$(wip_mktemp)"
  manifest="$tmp/.wip.yaml"
  gitignore="$tmp/.gitignore"

  {
    printf 'version: 1\n'
    printf 'gitignore:\n'
    printf '  commit: false\n'
    if ((no_key == 0)); then
      if ((${#entries[@]} == 0)); then
        printf '  always_commit: []\n'
      else
        printf '  always_commit:\n'
        local e
        for e in "${entries[@]}"; do printf '    - %s\n' "$e"; done
      fi
    fi
  } >"$manifest"

  {
    printf '# noise\n'
    printf '.DS_Store\n'
    printf '\n'
    printf '.wip.yaml\n'
    if ((no_anchor == 0)); then printf '.wip/\n'; fi
    printf '\n'
    printf '# tooling\n'
    printf '.direnv/\n'
  } >"$gitignore"

  before="$tmp/.gitignore.before"
  cp "$gitignore" "$before"
}

# expected_gitignore <path> <entry...> — write the EXPECTED post-sync file, built
# by hand (not by the generator), so the fresh-insertion assertion is a real
# byte-for-byte diff against an independently-authored fixture rather than a
# tautology comparing the generator against itself.
expected_gitignore() {
  local dest="$1"
  shift
  {
    printf '# noise\n'
    printf '.DS_Store\n'
    printf '\n'
    printf '.wip.yaml\n'
    printf '.wip/\n'
    printf '%s\n' "$BEGIN_LINE"
    printf '!.wip/\n'
    printf '.wip/*\n'
    local e
    for e in "$@"; do printf '!%s\n' "$e"; done
    printf '%s\n' "$END_LINE"
    printf '\n'
    printf '# tooling\n'
    printf '.direnv/\n'
  } >"$dest"
}

# ---------------------------------------------------------------------------
# 1. Fresh insertion: no block, anchor present, 2 declared entries → `updated`,
#    and the resulting file is byte-for-byte the expected fixture. The full-file
#    diff (not a grep) is the point: wrong sort order, a missing marker line, a
#    stray blank, or a misplaced block all fail here and only here.
# ---------------------------------------------------------------------------
setup .wip/GLOSSARY.md .wip/backlog.md
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "updated" "$st" "fresh: status updated"
exp="$(wip_mktemp)/expected"
# LC_ALL=C sort puts GLOSSARY.md (uppercase G, 0x47) before backlog.md (0x62).
expected_gitignore "$exp" .wip/GLOSSARY.md .wip/backlog.md
assert_cmp "$exp" "$gitignore" "fresh: file is byte-for-byte the expected block"

# ---------------------------------------------------------------------------
# 2. Idempotence: re-running against the just-written file is `noop` and does not
#    touch a byte. An implementation that always inserts (never looks for an
#    existing block) duplicates the block here and fails both assertions.
# ---------------------------------------------------------------------------
snap="$(wip_mktemp)/snap"
cp "$gitignore" "$snap"
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "noop" "$st" "re-run: status noop"
assert_cmp "$snap" "$gitignore" "re-run: file BYTE-IDENTICAL (no write)"

# ---------------------------------------------------------------------------
# 3. Grow: a 3rd declared entry → `updated`, the block carries all three, sorted,
#    and the block is REPLACED in place (not appended alongside the old one).
# ---------------------------------------------------------------------------
setup .wip/GLOSSARY.md .wip/backlog.md
_wip_gitignore_sync_always_commit "$manifest" "$gitignore" >/dev/null
# Re-declare with a third entry, keeping the same on-disk .gitignore (which now
# holds the 2-entry block) — this is the "the manifest changed" path.
{
  printf 'version: 1\n'
  printf 'gitignore:\n'
  printf '  commit: false\n'
  printf '  always_commit:\n'
  printf '    - .wip/backlog.md\n'
  printf '    - .wip/NOTES.md\n'
  printf '    - .wip/GLOSSARY.md\n'
} >"$manifest"
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "updated" "$st" "grow: status updated"
expected_gitignore "$exp" .wip/GLOSSARY.md .wip/NOTES.md .wip/backlog.md
assert_cmp "$exp" "$gitignore" "grow: block holds all 3, sorted, replaced in place"
assert_eq "1" "$(grep -c -- "^${END_LINE}\$" "$gitignore")" "grow: exactly one block (no duplicate)"

# ---------------------------------------------------------------------------
# 4. Shrink: back down to a single declared entry → `updated`, only the survivor
#    remains in the block.
# ---------------------------------------------------------------------------
{
  printf 'version: 1\n'
  printf 'gitignore:\n'
  printf '  commit: false\n'
  printf '  always_commit:\n'
  printf '    - .wip/backlog.md\n'
} >"$manifest"
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "updated" "$st" "shrink: status updated"
expected_gitignore "$exp" .wip/backlog.md
assert_cmp "$exp" "$gitignore" "shrink: block holds only the survivor"
assert_not_grep "GLOSSARY" "$gitignore" "shrink: dropped entry is gone from the file"

# ---------------------------------------------------------------------------
# 5. Emptied list with a stale block present → `updated`, the block is removed
#    ENTIRELY (both markers gone), and the rest of the file is untouched — i.e.
#    the file returns exactly to its pre-block state.
# ---------------------------------------------------------------------------
setup .wip/GLOSSARY.md .wip/backlog.md
pristine="$(wip_mktemp)/pristine"
cp "$gitignore" "$pristine" # the fixture BEFORE any block was ever inserted
_wip_gitignore_sync_always_commit "$manifest" "$gitignore" >/dev/null
assert_grep "^${BEGIN_LINE}\$" "$gitignore" "empty: (precondition) block was inserted"
{
  printf 'version: 1\n'
  printf 'gitignore:\n'
  printf '  commit: false\n'
  printf '  always_commit: []\n'
} >"$manifest"
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "updated" "$st" "empty: status updated (stale block removed)"
assert_not_grep "always_commit exceptions" "$gitignore" "empty: both marker lines gone"
assert_cmp "$pristine" "$gitignore" "empty: file back to its exact pre-block content"

# ---------------------------------------------------------------------------
# 6. Empty list, no block ever existed → `noop`, byte-identical. Covers BOTH
#    spellings of "empty": an explicit `always_commit: []` and the key omitted
#    entirely (a manifest that never declared the policy at all).
# ---------------------------------------------------------------------------
setup
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "noop" "$st" "empty+noblock: status noop (explicit [])"
assert_cmp "$before" "$gitignore" "empty+noblock: file BYTE-IDENTICAL (no write)"

setup --no-key
st="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "noop" "$st" "empty+noblock: status noop (key absent entirely)"
assert_cmp "$before" "$gitignore" "empty+noblock: file BYTE-IDENTICAL (key absent)"

# ---------------------------------------------------------------------------
# 7. Anchor missing (no bare `.wip/` line) with a non-empty list → return 1, and
#    a message on stderr. Asserted via `$?` + a captured stderr, not "it didn't
#    crash": a seam that silently returned 0 having written nothing would leave
#    the declared policy unenforced with no signal at all.
# ---------------------------------------------------------------------------
setup --no-anchor .wip/GLOSSARY.md
rc=0
anchor_err="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore" 2>&1 >/dev/null)" || rc=$?
assert_eq "1" "$rc" "anchor-missing: returns 1"
assert_eq "0" "$([[ -n "$anchor_err" ]] && echo 0 || echo 1)" "anchor-missing: stderr message present"
assert_cmp "$before" "$gitignore" "anchor-missing: file untouched"

# ---------------------------------------------------------------------------
# 8. Nested declared path → return 1, with a message that is a DIFFERENT STRING
#    from the anchor-missing one. Asserting the two messages actually differ (not
#    merely that both are non-empty) is what catches a Builder collapsing both
#    refusals into one generic error — the two have different causes and different
#    fixes, and a caller must be able to tell them apart.
# ---------------------------------------------------------------------------
setup .wip/initiatives/x/NOTES.md
rc=0
nested_err="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore" 2>&1 >/dev/null)" || rc=$?
assert_eq "1" "$rc" "nested: returns 1"
assert_eq "0" "$([[ -n "$nested_err" ]] && echo 0 || echo 1)" "nested: stderr message present"
assert_eq "0" "$([[ "$nested_err" != "$anchor_err" ]] && echo 0 || echo 1)" \
  "nested: message DIFFERS from the anchor-missing message"
assert_cmp "$before" "$gitignore" "nested: file untouched"

# A declared entry outside `.wip/` is its own refusal (the manifest is asserting a
# policy this generator's block cannot express), also distinct from the two above.
setup README.md
rc=0
outside_err="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore" 2>&1 >/dev/null)" || rc=$?
assert_eq "1" "$rc" "outside-.wip: returns 1"
assert_eq "0" "$([[ "$outside_err" != "$nested_err" ]] && echo 0 || echo 1)" \
  "outside-.wip: message DIFFERS from the nested-path message"

# ---------------------------------------------------------------------------
# 9. WIP_DRY_RUN=1 on the fresh-insertion case → the status word is still computed
#    and printed, but the file on disk is byte-identical to the pre-run fixture.
# ---------------------------------------------------------------------------
setup .wip/GLOSSARY.md .wip/backlog.md
st="$(WIP_DRY_RUN=1 _wip_gitignore_sync_always_commit "$manifest" "$gitignore")"
assert_eq "updated" "$st" "dry-run: reports updated"
assert_cmp "$before" "$gitignore" "dry-run: file BYTE-IDENTICAL (no write)"

# A dry-run must not paper over an error it would have hit for real: the
# anchor-missing refusal still fires (and still writes nothing) under --dry-run.
setup --no-anchor .wip/GLOSSARY.md
rc=0
WIP_DRY_RUN=1 _wip_gitignore_sync_always_commit "$manifest" "$gitignore" >/dev/null 2>&1 || rc=$?
assert_eq "1" "$rc" "dry-run: anchor-missing still returns 1 (no false 'updated')"

# ---------------------------------------------------------------------------
# 10. The end-to-end proof, and the only case that needs a REAL git repo: rebuild
#     the live repo's ACTUAL .gitignore content verbatim inside a throwaway
#     `git init` dir, sync it, and let `git check-ignore` — git's own resolver,
#     not our reading of it — decide what is ignored.
#
#     This is what catches the non-obvious failure the Decisions section warned
#     about: a generator emitting only `!.wip/GLOSSARY.md`, with no `!.wip/` +
#     `.wip/*` pair, produces a file that LOOKS right and still leaves the file
#     ignored, because git never descends into an ignored directory. Pure content
#     assertions cannot see that; check-ignore can.
#
#     NB: this reconstructs the live .gitignore's content — it never reads, runs
#     against, or modifies the real file.
# ---------------------------------------------------------------------------
repo="$(wip_mktemp)"
git -C "$repo" init -q

cat >"$repo/.gitignore" <<'GITIGNORE'
# Study slices — copies of other projects collected for distillation, not part
# of the wip system itself. Ignored so this repo's history is just wip.
bizapps-symfony-bot/
bug-free-happiness/
changelog-portable-stub/
direnv-session-loader/
hypomnema/
layered-documentation-system/
obsidian-sync-to-git/
playbook/
prtend/
workflow-portable-stub/
xcind/

.wip.yaml
.wip/

# Environment / tooling noise
.direnv/
result
result-*
.DS_Store

# Task-backend live orchestration state. Roadmaps/workplans remain the durable
# tracked plan; these files are an ephemeral execution mirror.
.wip/initiatives/*/orchestration/
GITIGNORE

cat >"$repo/.wip.yaml" <<'YAML'
version: 1
gitignore:
  commit: false
  always_commit:
    - .wip/GLOSSARY.md
    - .wip/backlog.md
YAML

# A realistic .wip/ tree: the two declared files, an undeclared sibling, an
# undeclared nested workplan, and an orchestration-mirror file (which the
# pre-existing line-26 rule ALSO covers — it must stay ignored either way).
mkdir -p "$repo/.wip/initiatives/demo/workplans" \
  "$repo/.wip/initiatives/demo/orchestration" \
  "$repo/hypomnema"
: >"$repo/.wip/GLOSSARY.md"
: >"$repo/.wip/backlog.md"
: >"$repo/.wip/tracker-cache.json"
: >"$repo/.wip/initiatives/demo/workplans/step-01-x.md"
: >"$repo/.wip/initiatives/demo/orchestration/state.json"
: >"$repo/hypomnema/README.md"

st="$(_wip_gitignore_sync_always_commit "$repo/.wip.yaml" "$repo/.gitignore")"
assert_eq "updated" "$st" "real-gitignore: status updated"

# ignore_state <path> — ask git itself. `check-ignore -q` exits 0 when the path is
# matched by an ignore rule, 1 when it is not.
ignore_state() {
  if git -C "$repo" check-ignore -q -- "$1"; then
    printf 'ignored'
  else
    printf 'not-ignored'
  fi
}

# The two declared files — and ONLY these — must come back un-ignored.
assert_eq "not-ignored" "$(ignore_state .wip/GLOSSARY.md)" "real-gitignore: GLOSSARY.md un-ignored"
assert_eq "not-ignored" "$(ignore_state .wip/backlog.md)" "real-gitignore: backlog.md un-ignored"

# Everything else under .wip/ stays ignored — the `.wip/*` re-ignore line is what
# keeps `!.wip/` from accidentally opening the whole directory.
assert_eq "ignored" "$(ignore_state .wip/tracker-cache.json)" \
  "real-gitignore: undeclared sibling still ignored"
assert_eq "ignored" "$(ignore_state .wip/initiatives/demo/workplans/step-01-x.md)" \
  "real-gitignore: undeclared nested workplan still ignored"
assert_eq "ignored" "$(ignore_state .wip/initiatives/demo/orchestration/state.json)" \
  "real-gitignore: orchestration mirror still ignored"

# The rest of the file's rules must survive the splice untouched.
assert_eq "ignored" "$(ignore_state hypomnema/README.md)" \
  "real-gitignore: study-slice rule still ignored"
assert_eq "ignored" "$(ignore_state .wip.yaml)" \
  "real-gitignore: .wip.yaml rule still ignored"
assert_eq "ignored" "$(ignore_state .DS_Store)" \
  "real-gitignore: tooling-noise rule still ignored"

# And the sync is idempotent against the real file's shape too.
st="$(_wip_gitignore_sync_always_commit "$repo/.wip.yaml" "$repo/.gitignore")"
assert_eq "noop" "$st" "real-gitignore: re-run is noop"

test_summary
