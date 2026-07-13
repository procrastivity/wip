#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="gitignore-sync-end-to-end"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the COMPOSED `gitignore sync` verb driven END-TO-END through
# `bin/wip-plumbing gitignore` — arg surface, root resolution, the JSON ledger,
# and the dispatcher wiring that makes the verb reachable at all.
#
# The load-bearing assertion here is one the lib-level tests structurally cannot
# make: that the file the verb WRITES is actually consumable by git. Every case
# that claims a positive sync proves it by running `git add -A` inside a real
# `git init` fixture and reading back `git status --porcelain` — git's own index,
# not our reading of the file. Case A pins the staged set EXACTLY (not "the two
# declared files appear"), because the failure mode the `.wip/*` re-ignore line
# exists to prevent is the opposite of a missing file: `!.wip/` alone re-opens
# the WHOLE directory, and a test that only checked for the presence of the two
# declared paths would pass while every ignored file under `.wip/` silently
# became stageable.
#
# Case A0 is what keeps the rest of the file from being vacuous: it proves the
# fixture STARTS in the broken state (the declared files are un-stageable before
# the verb runs), so A's staged set is something the verb caused rather than
# something the fixture was born with.
#
# Seam-internal mechanics (block content byte-for-byte, sort order, the empty-list
# removal case, each refusal's distinct message) are owned by
# test-gitignore-sync.sh and NOT re-tested here.
# Contract: workplan step-05 chunk 2.
#
# NB — hard boundary: every fixture is a throwaway `git init` dir. This file never
# reads, runs against, or modifies the live repo's .gitignore, and never `git
# add`s the live .wip/backlog.md or .wip/GLOSSARY.md.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1

# setup_gi [variant] — a fresh REAL git repo in a new tmp root; sets the globals
# `tmp` and `gitignore`. (The manifest path is deliberately NOT a global: the verb
# resolves it from the root itself, and nothing in this file reads it back.)
#
#   variant : declared (default) `always_commit` names the two top-level files.
#             nested             `always_commit` names a NESTED path — the entry
#                                the one-level un-ignore shape cannot express, so
#                                the generator refuses it.
#
# The `.gitignore` reproduces the shape the real policy has to survive: a bare
# `.wip/` blanket-ignore (the anchor), plus a pre-existing deeper `.wip/` rule
# below it. The `.wip/` tree carries undeclared files at both the top level and
# nested, so "only the declared files became trackable" is an assertion with
# something to be wrong about.
setup_gi() {
  local variant="${1:-declared}"
  tmp="$(wip_mktemp)"
  git -C "$tmp" init -q

  cat >"$tmp/.gitignore" <<'GITIGNORE'
# Environment / tooling noise
.direnv/
.DS_Store

.wip.yaml
.wip/

# Task-backend live orchestration state.
.wip/initiatives/*/orchestration/
GITIGNORE

  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    printf 'gitignore:\n'
    printf '  commit: false\n'
    printf '  always_commit:\n'
    case "$variant" in
      declared)
        printf -- '    - .wip/GLOSSARY.md\n'
        printf -- '    - .wip/backlog.md\n'
        ;;
      nested)
        printf -- '    - .wip/initiatives/demo/NOTES.md\n'
        ;;
      *)
        printf 'setup_gi: unknown variant %q\n' "$variant" >&2
        return 2
        ;;
    esac
  } >"$tmp/.wip.yaml"

  mkdir -p "$tmp/.wip/initiatives/demo/workplans" \
    "$tmp/.wip/initiatives/demo/orchestration"
  : >"$tmp/.wip/GLOSSARY.md"
  : >"$tmp/.wip/backlog.md"
  : >"$tmp/.wip/tracker-cache.json"
  : >"$tmp/.wip/initiatives/demo/workplans/step-01-x.md"
  : >"$tmp/.wip/initiatives/demo/orchestration/state.json"
  : >"$tmp/README.md"

  gitignore="$tmp/.gitignore"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing gitignore "$@"; }

# staged — `git add -A`, then echo the resulting index as a comma-joined, sorted
# path list. This is git's own answer to "what would a commit contain", which is
# the only question this verb's output ultimately has to get right.
staged() {
  git -C "$tmp" add -A
  git -C "$tmp" status --porcelain | sed 's/^...//' | LC_ALL=C sort | paste -sd, -
}

# snapshot — copy the current .gitignore aside; echo the snapshot path.
snapshot() {
  local s
  s="$(wip_mktemp)/gitignore"
  cp "$gitignore" "$s"
  printf '%s\n' "$s"
}

# ---------------------------------------------------------------------------
# Case A0 — the anti-vacuity pin: BEFORE the verb runs, the two declared files
#   are ignored, so `git add -A` cannot stage them. Everything case A asserts is
#   only meaningful against this baseline.
# ---------------------------------------------------------------------------
setup_gi declared
assert_eq ".gitignore,README.md" "$(staged)" \
  "A0: before sync, the declared .wip/ files are UN-stageable (policy unenforced)"

# ---------------------------------------------------------------------------
# Case A — positive sync, proven through git's index. The staged set is pinned
#   EXACTLY: the two declared files become stageable and NOTHING else under
#   `.wip/` does (not the undeclared top-level sibling, not the nested workplan,
#   not the orchestration mirror). A generator that emitted `!.wip/` without the
#   `.wip/*` re-ignore would stage all of them and fail right here.
# ---------------------------------------------------------------------------
setup_gi declared
out="$(run sync)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "sync" "$(jq -r '.action' <<<"$out")" "A: action sync"
assert_eq "updated" "$(jq -r '.status' <<<"$out")" "A: status updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"
assert_eq "$gitignore" "$(jq -r '.gitignore' <<<"$out")" "A: ledger names the resolved .gitignore path"
assert_eq "null" "$(jq -r '.dry_run' <<<"$out")" "A: dry_run absent without the flag"
assert_grep '^!\.wip/$' "$gitignore" "A: the un-ignore line landed on disk"
assert_grep '^\.wip/\*$' "$gitignore" "A: the re-ignore line landed on disk"
assert_eq ".gitignore,.wip/GLOSSARY.md,.wip/backlog.md,README.md" "$(staged)" \
  "A: git stages EXACTLY the two declared files — nothing else under .wip/"

# ---------------------------------------------------------------------------
# Case B — idempotency: a clean re-run is noop/changed:false and writes nothing.
#   The BYTE-IDENTICAL check is the real assertion — "exit 0 twice" would pass
#   against a verb that appended a duplicate block on every run.
# ---------------------------------------------------------------------------
setup_gi declared
run sync >/dev/null # run 1 — mutates
before="$(snapshot)"
out2="$(run sync)" # run 2 — steady state
assert_eq "noop" "$(jq -r '.status' <<<"$out2")" "B: re-run status noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "B: re-run changed false"
assert_cmp "$before" "$gitignore" "B: .gitignore BYTE-IDENTICAL across the re-run"
out3="$(run sync)"
assert_eq "$out2" "$out3" "B: steady-state ledger stable (run2 == run3)"

# ---------------------------------------------------------------------------
# Case C — --dry-run: the full ledger is still computed (same status word, same
#   resolved path) but nothing is written, and git's index is unmoved — the
#   mirror image of case A, which is what makes "dry-run wrote nothing" a claim
#   about behavior rather than about file mtimes.
# ---------------------------------------------------------------------------
setup_gi declared
before="$(snapshot)"
out="$(run sync --dry-run)"
assert_eq "updated" "$(jq -r '.status' <<<"$out")" "C: dry-run reports the status it WOULD have written"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "C: dry-run changed true"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "C: dry_run true"
assert_cmp "$before" "$gitignore" "C: .gitignore unwritten under --dry-run"
assert_eq ".gitignore,README.md" "$(staged)" \
  "C: the declared files are still un-stageable — the dry-run really wrote nothing"

# C2 — the GLOBAL --dry-run flag (before the verb) reaches the seam identically.
setup_gi declared
before="$(snapshot)"
out="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run gitignore sync)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "C2: global --dry-run honored"
assert_cmp "$before" "$gitignore" "C2: .gitignore unwritten under the global flag"

# ---------------------------------------------------------------------------
# Case D — a generator refusal surfaces as the verb's OWN error envelope. The
#   nested-path entry is the refusal with a real chance of reaching a user (it is
#   a plausible manifest edit, not a corrupt file), so it is the one pinned here:
#   exit 1, the `internal` envelope on stdout, the generator's specific reason on
#   stderr, and — the actual point — NO bash stack trace and no half-written file.
# ---------------------------------------------------------------------------
setup_gi nested
before="$(snapshot)"
err="$(wip_mktemp)/err"
set +e
out="$(run sync 2>"$err")"
rc=$?
set -e
assert_eq "1" "$rc" "D: nested-path rejection exits 1"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "D: ok false"
assert_eq "internal" "$(jq -r '.error.kind' <<<"$out")" "D: kind internal"
assert_eq "gitignore: sync writer failed" "$(jq -r '.error.message' <<<"$out")" "D: the verb's own message"
assert_grep 'nested always_commit entry not supported' "$err" \
  "D: the generator's SPECIFIC reason still reaches stderr"
assert_not_grep 'line [0-9]*:' "$err" "D: no bash stack trace leaked to stderr"
assert_cmp "$before" "$gitignore" "D: .gitignore untouched by the refused run"

# ---------------------------------------------------------------------------
# Case E — the arg surface (mirrors closeout's skeleton pins).
# ---------------------------------------------------------------------------
setup_gi declared

set +e
out="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: missing subcommand exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "E: missing subcommand kind usage"

set +e
out="$(run bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unknown subcommand exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "E: unknown subcommand kind usage"

set +e
out="$(run sync --bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unknown flag exits 2"

set +e
out="$(run sync extra 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unexpected positional arg exits 2"

test_summary
