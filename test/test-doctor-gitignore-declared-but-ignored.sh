#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-gitignore-declared-but-ignored"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# §2m (kind:"gitignore-declared", status:"gitignore-declared-but-ignored"): a path
# the manifest declares in `gitignore.always_commit` that git STILL ignores — the
# policy is declared but not made true on disk. The oracle is `git check-ignore`
# itself, never our own reading of `.gitignore`'s content, because the failure this
# check exists to catch is precisely one that content-inspection cannot see (git
# does not descend into an ignored directory, so a bare `!.wip/GLOSSARY.md` line
# with no `!.wip/` + `.wip/*` scaffolding LOOKS like an exception and is not one —
# case D pins exactly that).
#
# Unlike every prior doctor test file these fixtures are REAL git repos (`git init`
# in a `wip_mktemp` dir, a real `.gitignore`), since both new checks shell out to
# git. The healthy `.gitignore` is produced by calling the chunk-1 generator once at
# fixture setup rather than hand-writing the block — a hand-written fixture could
# silently drift from what `gitignore sync` actually emits and turn the healthy
# baseline into a lie.
#
# HARD BOUNDARY (Orchestrator-signed-off): every fixture is a disposable tmp git
# repo. Nothing here reads, runs against, or modifies the live repo's `.gitignore`,
# `.wip.yaml`, or `.wip/` tree.
#
# Contract: workplan step-05 (.wip/initiatives/closeout-write-ladder/workplans/
# step-05-enforce-the-always-commit-gitignore-policy.md), Chunk 3.

export WIP_NO_REGISTRY=1

# shellcheck source=lib/wip/wip-plumbing-gitignore-lib.bash
source lib/wip/wip-plumbing-gitignore-lib.bash

# run_doctor <tmp> — run doctor under WIP_ROOT; set globals OUT (json) and RC.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" bin/wip-plumbing doctor)"
  RC=$?
  set -e
}

# n_declared / n_tracked [selector] — count entries of each gitignore kind. Both
# live in every case, including the ones where one of them must be exactly 0: the
# cross-direction count is what proves the two checks are independently wired and
# not one over-eager predicate answering for both.
n_declared() { jq "[.checks[] | select(.kind==\"gitignore-declared\") | select(${1:-true})] | length" <<<"$OUT"; }
n_tracked() { jq '[.checks[] | select(.kind=="gitignore-tracked")] | length' <<<"$OUT"; }

# mkrepo <tmp> — a real git repo holding a wip fixture that is quiet for every
# OTHER doctor check (no shipped steps → §2b/§2j/§2l silent; in-flight
# current_initiative → §2k silent), so gitignore drift is the only drift in play.
# The two declared files exist on disk but are NOT git-added: the index stays
# empty, so §2n has nothing to say and §2m's counts stand alone.
mkrepo() {
  local tmp="$1"
  wip_fixture_init "$tmp" --no-active-step
  cat >>"$tmp/.wip.yaml" <<'YAML'
gitignore:
  always_commit:
    - .wip/GLOSSARY.md
    - .wip/backlog.md
YAML
  cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — current.
MD
  printf 'glossary\n' >"$tmp/.wip/GLOSSARY.md"
  printf 'backlog\n' >"$tmp/.wip/backlog.md"
  cat >"$tmp/.gitignore" <<'GI'
.direnv/
.DS_Store

.wip.yaml
.wip/
GI
  git -C "$tmp" init -q
}

# ── Case A: healthy/quiet baseline ───────────────────────────────────────────
# `.gitignore` synced by the real generator; nothing tracked. Both directions must
# be silent. This is also the fixture the vacuous-always-true guard is demonstrated
# against (see the commit message): an implementation that fires regardless of state
# shows up HERE, where the correct answer is zero.
tmpA="$(wip_mktemp)"
mkrepo "$tmpA"
assert_eq "updated" "$(_wip_gitignore_sync_always_commit "$tmpA/.wip.yaml" "$tmpA/.gitignore")" \
  "healthy: fixture's .gitignore comes from the real generator, not by hand"
run_doctor "$tmpA"
assert_eq "0" "$RC" "healthy: exit 0"
assert_eq "0" "$(n_declared)" "healthy: no gitignore-declared-but-ignored entry"
assert_eq "0" "$(n_tracked)" "healthy: no gitignore-tracked-but-undeclared entry either"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "healthy: zero drift overall"
assert_eq "true" "$(jq -r '.ok' <<<"$OUT")" "healthy: ok:true"

# ...and git agrees the declared files really are un-ignored — the baseline is
# healthy because the state is healthy, not because the check is asleep.
for p in .wip/GLOSSARY.md .wip/backlog.md; do
  set +e
  git -C "$tmpA" check-ignore -q -- "$p"
  gi_rc=$?
  set -e
  assert_eq "1" "$gi_rc" "healthy: git itself reports $p is NOT ignored"
done

# ── Case B: §2m-only drift, concrete count ───────────────────────────────────
# The generated block is deleted from the SAME fixture — "the generator never ran".
# Both declared files fall back under the blanket `.wip/` ignore. Nothing is in the
# index, so §2n has nothing to flag: exactly 2 and exactly 0.
grep -v -e '^# --- wip: gitignore\.always_commit' -e '^# --- end wip: gitignore\.always_commit' \
  -e '^!\.wip/' -e '^\.wip/\*$' "$tmpA/.gitignore" >"$tmpA/.gitignore.tmp"
mv "$tmpA/.gitignore.tmp" "$tmpA/.gitignore"
assert_not_grep '!' "$tmpA/.gitignore" "§2m-only: the fixture's block really is gone"
run_doctor "$tmpA"
assert_eq "4" "$RC" "§2m-only: exit 4"
assert_eq "2" "$(n_declared)" "§2m-only: EXACTLY 2 entries — one per declared file"
assert_eq "0" "$(n_tracked)" "§2m-only: EXACTLY 0 tracked-but-undeclared entries (nothing is tracked)"
assert_eq "2" "$(jq -r '.drift_count' <<<"$OUT")" "§2m-only: the 2 §2m entries are the ONLY drift"
assert_eq "1" "$(n_declared '.path==".wip/GLOSSARY.md"')" "§2m-only: GLOSSARY.md flagged once"
assert_eq "1" "$(n_declared '.path==".wip/backlog.md"')" "§2m-only: backlog.md flagged once"
assert_eq "2" "$(n_declared '.status=="gitignore-declared-but-ignored"')" \
  "§2m-only: status is gitignore-declared-but-ignored"
assert_eq "run wip-plumbing gitignore sync" \
  "$(jq -r '[.checks[]|select(.kind=="gitignore-declared").fix]|unique|.[0]' <<<"$OUT")" \
  "§2m-only: fix names the verb that repairs it"

# ── Case C: re-syncing the same fixture goes quiet again ─────────────────────
# The drift is a function of STATE, not of the check having run: repair the state
# and the entries disappear. (An always-fire check survives case A only if it also
# survives here — this is the same assertion from the other direction, on a fixture
# that has already been seen to be dirty.)
assert_eq "updated" "$(_wip_gitignore_sync_always_commit "$tmpA/.wip.yaml" "$tmpA/.gitignore")" \
  "re-sync: generator repairs the block"
run_doctor "$tmpA"
assert_eq "0" "$RC" "re-sync: exit 0 again"
assert_eq "0" "$(n_declared)" "re-sync: §2m silent again"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "re-sync: zero drift again"

# ── Case D: the plausible-but-non-functional gitignore ───────────────────────
# THE mutation pin for §2m. A `.gitignore` carrying bare `!.wip/GLOSSARY.md` /
# `!.wip/backlog.md` lines with NO `!.wip/` + `.wip/*` scaffolding reads, to a human
# and to any content-matching check, as "the exceptions are declared". git disagrees:
# it never descends into the ignored `.wip/`, so the files stay ignored. §2m must
# still flag both — a check that greps `.gitignore` for `!<path>` instead of asking
# `check-ignore` would go quiet here and be wrong.
tmpD="$(wip_mktemp)"
mkrepo "$tmpD"
cat >"$tmpD/.gitignore" <<'GI'
.direnv/
.DS_Store

.wip.yaml
.wip/
!.wip/GLOSSARY.md
!.wip/backlog.md
GI
run_doctor "$tmpD"
assert_eq "4" "$RC" "bare-! gitignore: exit 4 — still broken"
assert_eq "2" "$(n_declared)" "bare-! gitignore: BOTH declared files still flagged"
assert_eq "0" "$(n_tracked)" "bare-! gitignore: and §2n stays out of it"

# ── Case E: git-unavailable fallback ─────────────────────────────────────────
# A wip root that is not a git worktree at all. Both checks degrade to ONE combined
# informational note — never a crash, never a false positive, never exit 4.
tmpE="$(wip_mktemp)"
mkrepo "$tmpE"
rm -rf "$tmpE/.git"
run_doctor "$tmpE"
assert_eq "0" "$RC" "no git repo: exit 0 (a missing git never fails doctor)"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "no git repo: zero drift"
assert_eq "1" "$(jq '[.checks[]|select(.kind=="gitignore")]|length' <<<"$OUT")" \
  "no git repo: ONE combined note, not one per check"
assert_eq "ok" "$(jq -r '.checks[]|select(.kind=="gitignore").status' <<<"$OUT")" \
  "no git repo: informational status ok"
assert_eq "unavailable" "$(jq -r '.checks[]|select(.kind=="gitignore").probe' <<<"$OUT")" \
  "no git repo: probe unavailable"
assert_eq "0" "$(n_declared)" "no git repo: no §2m entries"
assert_eq "0" "$(n_tracked)" "no git repo: no §2n entries"

# ── Case F: no declared paths → §2m has nothing to check ─────────────────────
# An empty/absent `always_commit` list is not drift: there is no promise to break.
# The check iterates the declared list, so an empty list must produce an empty
# result — not a default-fire.
tmpF="$(wip_mktemp)"
mkrepo "$tmpF"
wip_fixture_init "$tmpF" --no-active-step # rewrite the manifest WITHOUT the gitignore key
run_doctor "$tmpF"
assert_eq "0" "$RC" "no always_commit key: exit 0"
assert_eq "0" "$(n_declared)" "no always_commit key: nothing declared, nothing flagged"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "no always_commit key: zero drift"

test_summary
