#!/usr/bin/env bash
# `next` must stop nominating a backlog item whose tracker is already closed —
# in BOTH candidate sources (the roadmap's own `## Backlog` section AND the
# repo-level `.wip/backlog.md`). Workplan step-06 chunk 6.
#
# The load-bearing rule is Decision 4 (fail open): a tracker with NO
# `issue:<ID>` cache entry is UNKNOWN, and unknown means KEEP SHOWING IT.
# "Has a tracker at all" is never itself a reason to filter — nothing populates
# the `issue:*` keyspace yet, so that misreading would empty both sources.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="next-skips-closed-backlog"
# shellcheck source=test/helpers.sh
source test/helpers.sh

export WIP_NO_REGISTRY=1

# A fixture repo with a tracked + an untracked entry in EACH source.
#   roadmap `## Backlog`  : BDS-80 (tracked), "Roadmap chore untracked"
#   .wip/backlog.md       : BDS-81 (tracked), BDS-82 (tracked), untracked
# BDS-82 is the Decision-4 probe: it always has a tracker and NEVER a cache
# entry, so it must appear in every single fixture below.
make_fixture() {
  local dir="$1"
  wip_fixture_init "$dir"
  cat >"$dir/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.

## Backlog

- **Roadmap chore** [tracker: BDS-80] — sweep stragglers.
- **Roadmap chore untracked** — no tracker at all.
MD
  # Live-shaped: multi-paragraph prose, tracker as a markdown LINK on the
  # entry's trailing line (the form every tracked entry in the real
  # .wip/backlog.md uses).
  cat >"$dir/.wip/backlog.md" <<'MD'
# Backlog — cross-cutting

## Nice-to-have

- **Repo tracked thing** — a wider concern.

  A second paragraph, because real entries have them.

  ([BDS-81](https://linear.app/x/issue/BDS-81))

- **Repo uncached thing** — tracked, but nothing has ever cached its state.

  ([BDS-82](https://linear.app/x/issue/BDS-82))

- **Repo untracked thing** — carries no tracker in either spelling.
MD
}

# seed_cache <dir> <key> <state> [<key> <state> ...] — write `.wip/tracker-cache.json`
# directly, the idiom the workplan's Test strategy prescribes (no live tracker
# read-through ships in this step, so there is no writer to drive).
seed_cache() {
  local dir="$1"
  shift
  local cache='{}'
  while (($#)); do
    cache="$(jq -c --arg k "issue:$1" --arg s "$2" \
      '.[$k] = {state: $s, reason: "fixture", updated: "2026-07-12"}' <<<"$cache")"
    shift 2
  done
  printf '%s\n' "$cache" >"$dir/.wip/tracker-cache.json"
}

ids() { jq -r '[.candidates[].id] | join(",")' <<<"$1"; }
has() { jq --arg id "$2" '[.candidates[] | select(.id == $id)] | length' <<<"$1"; }
rank_of() { jq -r --arg id "$2" '[.candidates[] | select(.id == $id)][0].rank // "absent"' <<<"$1"; }
# Ranks must stay 1..N contiguous: filtering happens BEFORE the append, so a
# skipped candidate never burns a rank number.
ranks() { jq -r '[.candidates[].rank] | join(",")' <<<"$1"; }

# ---- 1. Baseline: no cache file at all. Nothing is filtered, both sources rank.
tmpA="$(wip_mktemp)"
make_fixture "$tmpA"
outA="$(WIP_ROOT="$tmpA" bin/wip-plumbing next)"
assert_eq "step-02,roadmap-chore,roadmap-chore-untracked,repo-tracked-thing,repo-uncached-thing,repo-untracked-thing" \
  "$(ids "$outA")" "no cache: every candidate from both sources ranks"
assert_eq "1,2,3,4,5,6" "$(ranks "$outA")" "no cache: ranks contiguous"

# ---- 2. Closed in BOTH sources -> neither tracked id appears.
#      Mutation pin (a): a no-op filter that never filters fails right here.
tmpB="$(wip_mktemp)"
make_fixture "$tmpB"
seed_cache "$tmpB" BDS-80 "done" BDS-81 "done"
outB="$(WIP_ROOT="$tmpB" bin/wip-plumbing next)"
assert_eq "0" "$(has "$outB" roadmap-chore)" "closed: roadmap-backlog candidate filtered"
assert_eq "0" "$(has "$outB" repo-tracked-thing)" "closed: repo-backlog candidate filtered"
# Decision 4 — the uncached tracker is NOT collateral damage.
assert_eq "1" "$(has "$outB" repo-uncached-thing)" "closed: uncached tracker still ranks"
assert_eq "1" "$(has "$outB" roadmap-chore-untracked)" "closed: untracked roadmap entry still ranks"
assert_eq "1" "$(has "$outB" repo-untracked-thing)" "closed: untracked repo entry still ranks"
assert_eq "4" "$(jq -r '.candidates | length' <<<"$outB")" "closed: 6 - 2 filtered = 4 candidates"
assert_eq "1,2,3,4" "$(ranks "$outB")" "closed: ranks stay contiguous (skip before append)"
assert_eq "step-02" "$(jq -r '.candidates[0].id' <<<"$outB")" "closed: step ranking untouched"

# ---- 3. Open in BOTH sources -> both still appear, at their baseline ranks.
#      Mutation pin (b): an over-eager filter that drops everything from these
#      two sources fails here.
tmpC="$(wip_mktemp)"
make_fixture "$tmpC"
seed_cache "$tmpC" BDS-80 in-progress BDS-81 in-progress
outC="$(WIP_ROOT="$tmpC" bin/wip-plumbing next)"
assert_eq "1" "$(has "$outC" roadmap-chore)" "open: roadmap-backlog candidate kept"
assert_eq "2" "$(rank_of "$outC" roadmap-chore)" "open: roadmap-backlog candidate at rank 2"
assert_eq "1" "$(has "$outC" repo-tracked-thing)" "open: repo-backlog candidate kept"
assert_eq "4" "$(rank_of "$outC" repo-tracked-thing)" "open: repo-backlog candidate at rank 4"
assert_eq "6" "$(jq -r '.candidates | length' <<<"$outC")" "open: all 6 candidates kept"
assert_eq "1,2,3,4,5,6" "$(ranks "$outC")" "open: ranks contiguous"

# ---- 4. Tracker present, cache entry ABSENT -> keeps ranking (Decision 4).
#      Mutation pin (c): a stub that treats "has a tracker" as sufficient reason
#      to filter fails here — a cache holding SOMEONE ELSE's key must not drag
#      BDS-82 (or the untracked entries) down with it.
tmpD="$(wip_mktemp)"
make_fixture "$tmpD"
seed_cache "$tmpD" BDS-99 "done"
outD="$(WIP_ROOT="$tmpD" bin/wip-plumbing next)"
assert_eq "step-02,roadmap-chore,roadmap-chore-untracked,repo-tracked-thing,repo-uncached-thing,repo-untracked-thing" \
  "$(ids "$outD")" "unknown tracker state: nothing filtered, both sources intact"
assert_eq "1" "$(has "$outD" repo-uncached-thing)" "unknown: tracker with no cache entry still ranks"

# ---- 5. The closed vocabulary: done / canceled / cancelled, case-insensitive
#      (same words doctor's backlog-tracker-closed check uses).
for state in Done DONE canceled CANCELED cancelled Cancelled; do
  tmpV="$(wip_mktemp)"
  make_fixture "$tmpV"
  seed_cache "$tmpV" BDS-80 "$state" BDS-81 "$state"
  outV="$(WIP_ROOT="$tmpV" bin/wip-plumbing next)"
  assert_eq "0" "$(has "$outV" roadmap-chore)" "closed state '$state': roadmap candidate filtered"
  assert_eq "0" "$(has "$outV" repo-tracked-thing)" "closed state '$state': repo candidate filtered"
  assert_eq "1" "$(has "$outV" repo-uncached-thing)" "closed state '$state': uncached still ranks"
done

# ---- 6. An OPEN-ish state that merely contains a closed word is not closed.
#      (`_wip_next_tracker_closed` matches the whole state, not a substring.)
tmpW="$(wip_mktemp)"
make_fixture "$tmpW"
seed_cache "$tmpW" BDS-80 not-done BDS-81 in-review
outW="$(WIP_ROOT="$tmpW" bin/wip-plumbing next)"
assert_eq "1" "$(has "$outW" roadmap-chore)" "state 'not-done' is not closed"
assert_eq "1" "$(has "$outW" repo-tracked-thing)" "state 'in-review' is not closed"

# ---- 7. Repo backlog read through the corrected parser: the tracker-carrying
#      entries are found via the LIVE markdown-link spelling, not a bracket
#      marker. If the parse regressed to bare slugify (no tracker at all), the
#      closed fixture above could not have filtered anything — this asserts the
#      seam directly, so a future refactor back to `wip_roadmap_parse` (which
#      returns an empty `.backlog[]` for this file shape) is caught here too.
entries="$(bash -c '
  source lib/wip/wip-plumbing-roadmap-lib.bash
  source lib/wip/wip-plumbing-repo-backlog-lib.bash
  _wip_repo_backlog_parse "$1"' _ "$tmpA/.wip/backlog.md")"
assert_eq "BDS-81" \
  "$(jq -r '[.[] | select(.title == "Repo tracked thing")][0].tracker' <<<"$entries")" \
  "repo backlog entry resolves its markdown-link tracker"
assert_eq "null" \
  "$(jq -r '[.[] | select(.title == "Repo untracked thing")][0].tracker' <<<"$entries")" \
  "untracked repo backlog entry has no tracker"

test_summary
