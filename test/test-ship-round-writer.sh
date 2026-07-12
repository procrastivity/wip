#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-round-writer"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the ROUND-level `✅ shipped` marker writer (_wip_ship_mark_round_shipped),
# driven directly as a unit — `ship` does not call it yet (that wiring is
# step-03 Chunk 3). Mirrors test-ship-roadmap-writer.sh's fixture idiom, one
# level down: no CLI, no ledger, just the writer's contract —
# `updated`/`noop` on stdout + return 0, return 1 on internal error, return 2
# when the round heading is only found inside an HTML comment span.
# Contract: ADR-0016; workplan step-03 (round-level closeout writer).
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
DATE="2026-06-27"

# shellcheck source=lib/wip/wip-plumbing-roadmap-lib.bash
source lib/wip/wip-plumbing-roadmap-lib.bash
# shellcheck source=lib/wip/wip-plumbing-amend-lib.bash
source lib/wip/wip-plumbing-amend-lib.bash
# shellcheck source=lib/wip/wip-plumbing-ship-roadmap-lib.bash
source lib/wip/wip-plumbing-ship-roadmap-lib.bash

tmp="$(wip_mktemp)"

# setup_roadmap <content> — write a fresh roadmap fixture, set `roadmap` (path)
# and `before` (a byte-for-byte snapshot taken before the writer runs).
setup_roadmap() {
  roadmap="$tmp/roadmap.md"
  before="$tmp/roadmap.before.md"
  printf '%s\n' "$1" >"$roadmap"
  cp "$roadmap" "$before"
}

# mark <round-n> — run the writer, capturing status + return code into
# `status` / `rc` without tripping `set -e`.
mark() {
  status=""
  rc=0
  status="$(_wip_ship_mark_round_shipped "$roadmap" "$1" "$DATE")" || rc=$?
}

# round1_line — echo the roadmap's `## Round 1` heading line.
round1_line() { grep -F '## Round 1 —' "$roadmap"; }

# ---------------------------------------------------------------------------
# 1. Marker insertion: an unmarked `## Round N — Title` heading gains a
#    trailing ` ✅ shipped <date>`, writer reports `updated`.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 — done.'
mark 1
assert_eq "0" "$rc" "insert: return 0"
assert_eq "updated" "$status" "insert: status updated"
assert_eq '## Round 1 — Build ✅ shipped 2026-06-27' "$(round1_line)" \
  "insert: marker appended at the heading tail"
assert_eq "1" "$(round1_line | grep -o '✅' | wc -l | tr -d ' ')" "insert: exactly one marker"

# ---------------------------------------------------------------------------
# 2. Tracker key preserved: a round heading carrying `[tracker: ID]` keeps the
#    key and the marker still lands at the TRUE tail (the parser reads tracker
#    and marker order-independently; the rebuild is title, tracker, marker).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build [tracker: BDS-93]

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 1
assert_eq "updated" "$status" "tracker: status updated"
assert_eq '## Round 1 — Build [tracker: BDS-93] ✅ shipped 2026-06-27' "$(round1_line)" \
  "tracker: key preserved, marker at the true tail"
assert_eq "BDS-93" "$(jq -r '.rounds[0].tracker' <<<"$(wip_roadmap_parse "$roadmap")")" \
  "tracker: parser reads the rebuilt tracker key back"
assert_eq "Build" "$(jq -r '.rounds[0].title' <<<"$(wip_roadmap_parse "$roadmap")")" \
  "tracker: parser reads the rebuilt title back clean"

# ---------------------------------------------------------------------------
# 3. Regression pin 3 — idempotency is BYTE-IDENTICAL, not merely a `noop` word.
#    An already-marked-with-the-exact-date round reports `noop` AND the file is
#    unchanged on disk (a writer that rewrote the line unchanged would still be
#    a defect: it churns mtime/trailing whitespace and defeats the no-write
#    guarantee `ship` relies on).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build ✅ shipped 2026-06-27

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 1
assert_eq "0" "$rc" "noop: return 0"
assert_eq "noop" "$status" "noop: status noop"
assert_cmp "$before" "$roadmap" "noop: roadmap byte-identical (no write)"

# The same guarantee through a tracker-bearing heading already in canonical form.
setup_roadmap '# Roadmap

## Round 1 — Build [tracker: BDS-93] ✅ shipped 2026-06-27

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 1
assert_eq "noop" "$status" "noop+tracker: status noop"
assert_cmp "$before" "$roadmap" "noop+tracker: roadmap byte-identical (no write)"

# ---------------------------------------------------------------------------
# 4. Date normalization — a wrong date is corrected, with exactly one marker
#    glyph left behind (no duplicate marker run).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build ✅ shipped 2026-01-01

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 1
assert_eq "updated" "$status" "wrong-date: status updated"
assert_eq '## Round 1 — Build ✅ shipped 2026-06-27' "$(round1_line)" \
  "wrong-date: corrected to the target date"
assert_eq "1" "$(round1_line | grep -o '✅' | wc -l | tr -d ' ')" "wrong-date: exactly one marker"

# A bare `✅` with no date at all is the same case: complete it, don't duplicate.
setup_roadmap '# Roadmap

## Round 1 — Build ✅

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 1
assert_eq "updated" "$status" "missing-date: status updated"
assert_eq '## Round 1 — Build ✅ shipped 2026-06-27' "$(round1_line)" \
  "missing-date: marker completed with the target date"
assert_eq "1" "$(round1_line | grep -o '✅' | wc -l | tr -d ' ')" "missing-date: exactly one marker"

# ---------------------------------------------------------------------------
# 5. --dry-run ($WIP_DRY_RUN=1): the status is still computed and reported, but
#    the roadmap file is not written.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
WIP_DRY_RUN=1 mark 1
assert_eq "0" "$rc" "dry-run: return 0"
assert_eq "updated" "$status" "dry-run: status still computed as updated"
assert_cmp "$before" "$roadmap" "dry-run: roadmap unchanged (no write)"

# ---------------------------------------------------------------------------
# 6. Regression pin 4 — a round heading that exists ONLY inside an HTML comment
#    span is inert scaffolding (`init`'s template, a hand-authored example),
#    never a write anchor: return 2, no write. Round 1 here is real; Round 2 is
#    the commented example.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

<!--
## Round 2 — Commented example
- **step-12 — Commented prereq** — inert.
-->

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.'
mark 2
assert_eq "2" "$rc" "comment-shadowed: return 2"
assert_eq "" "$status" "comment-shadowed: no status word emitted"
assert_cmp "$before" "$roadmap" "comment-shadowed: roadmap byte-identical (no write)"

# A round that simply is not in the roadmap at all is `absent` (1), distinct
# from `shadowed` (2) — the caller maps them to different errors.
mark 9
assert_eq "1" "$rc" "absent: return 1"
assert_cmp "$before" "$roadmap" "absent: roadmap byte-identical (no write)"

test_summary
