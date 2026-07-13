#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="backlog-retire"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the retirement writers (workplan step-06, Chunk 2) driven directly as
# units — `_wip_backlog_retire_entry` (repo `.wip/backlog.md`, multi-paragraph)
# and `_wip_roadmap_backlog_retire_entry` (a roadmap's own one-line `## Backlog`
# section). The `backlog retire` verb that fronts them is chunk 7, tested
# separately; these seams take explicit paths, so there is no WIP_ROOT to root.
#
# HARD BOUNDARY: every fixture is a disposable tmp file. This suite NEVER writes
# to the live `.wip/backlog.md` — retiring a live entry is the Orchestrator's
# call alone (workplan Open Question 1).
#
# Contract: workplan step-06, Chunk 2 + Test strategy bullet 2.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1

# shellcheck source=lib/wip/wip-plumbing-roadmap-lib.bash
source lib/wip/wip-plumbing-roadmap-lib.bash
# shellcheck source=lib/wip/wip-plumbing-repo-backlog-lib.bash
source lib/wip/wip-plumbing-repo-backlog-lib.bash

# two_entry_fixture — a backlog with exactly two tracked entries (BDS-50, BDS-51),
# each multi-paragraph and each using the live-shaped markdown-link tracker form.
# Echoes the new file's path.
two_entry_fixture() {
  local f
  f="$(wip_mktemp)/backlog.md"
  cat >"$f" <<'EOF'
# Backlog — cross-cutting

## Nice-to-have

- **First entry, the one being retired**. Multi-paragraph prose block, exactly
  like the live file's entries.

  A second paragraph, to prove the whole block is spliced and not just the
  opening line.
  ([BDS-50](https://linear.app/beausimensen/issue/BDS-50))

- **Second entry, which must be left completely alone**. This block is the
  mutation pin: a writer that prunes "whatever entry it finds first" (or "the
  last one") rather than matching on the tracker id will disturb it.

  Its own second paragraph.
  ([BDS-51](https://linear.app/beausimensen/issue/BDS-51))
EOF
  printf '%s' "$f"
}

# --- retire BDS-50 ----------------------------------------------------------
f="$(two_entry_fixture)"
before="$(wip_mktemp)/before.md"
cp "$f" "$before"

# Snapshot BDS-51's block byte-for-byte BEFORE the write, so "untouched" can be
# proven with cmp rather than eyeballed.
bds51_before="$(wip_mktemp)/bds51-before.txt"
sed -n '/^- \*\*Second entry/,$p' "$f" >"$bds51_before"

status="$(_wip_backlog_retire_entry "$f" "BDS-50" "2026-07-12" "shipped as step-06")"
assert_eq "retired" "$status" "retiring a present tracker reports 'retired'"

# The whole multi-paragraph block is gone — not just its opening line.
assert_not_grep "First entry, the one being retired" "$f" "BDS-50's title line is gone"
assert_not_grep "second paragraph, to prove the whole block is spliced" "$f" \
  "BDS-50's SECOND PARAGRAPH is gone too (the whole block was spliced, not just the bullet line)"
assert_not_grep "issue/BDS-50" "$f" "BDS-50's trailing tracker link is gone"

# The pruned marker is appended, in the live file's exact convention.
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-50: shipped as step-06\.)_$' "$f" \
  "a canonical '- _(pruned <date> → filed as <id>: <reason>.)_' marker is appended"

# ---------------------------------------------------------------------------
# MUTATION PIN 1 — match by TRACKER, not by position.
#
# The plausible-wrong implementation prunes "whatever backlog entry it finds
# first" (or "the last one") instead of the one carrying the requested tracker.
# Against this two-entry fixture, retiring BDS-50 is the FIRST entry — so a
# prune-the-first stub would PASS every assertion above. What it cannot survive
# is BDS-51 being byte-identical: a prune-the-last stub deletes BDS-51's block
# (cmp fails), and any stub that splices by position rather than by the parsed
# `.tracker` match disturbs it. The pin is the untouched-ness of the entry that
# was NOT named, which is exactly the property "prune by tracker" has and
# "prune by position" does not.
# ---------------------------------------------------------------------------
bds51_after="$(wip_mktemp)/bds51-after.txt"
sed -n '/^- \*\*Second entry/,/issue\/BDS-51/p' "$f" >"$bds51_after"
bds51_expected="$(wip_mktemp)/bds51-expected.txt"
sed -n '/^- \*\*Second entry/,/issue\/BDS-51/p' "$before" >"$bds51_expected"
assert_cmp "$bds51_expected" "$bds51_after" \
  "MUTATION PIN: BDS-51's block is BYTE-IDENTICAL after retiring BDS-50 (kills a prune-first/prune-last stub)"
assert_grep "issue/BDS-51" "$f" "MUTATION PIN: BDS-51's tracker link survives untouched"
assert_eq "BDS-51" "$(_wip_repo_backlog_parse "$f" | jq -r '.[0].tracker')" \
  "MUTATION PIN: BDS-51 is still a parseable, retirable entry afterwards"

# Retire the SECOND entry, in a fresh fixture, and assert the FIRST is untouched.
# This is the other half of the pin, and it is not redundant: retiring BDS-50
# above happens to name the FIRST entry, so a "prune whatever is first" stub still
# looks correct there (it is only caught later, by the idempotency re-run, where
# it eats BDS-51). Naming the SECOND entry kills that stub outright and directly —
# a prune-first stub deletes BDS-50's block, which is asserted byte-identical here.
s2="$(two_entry_fixture)"
s2_before="$(wip_mktemp)/s2-before.md"
cp "$s2" "$s2_before"
bds50_expected="$(wip_mktemp)/bds50-expected.txt"
sed -n '/^- \*\*First entry/,/issue\/BDS-50/p' "$s2_before" >"$bds50_expected"

assert_eq "retired" "$(_wip_backlog_retire_entry "$s2" "BDS-51" "2026-07-12" "shipped")" \
  "retiring the SECOND entry by tracker reports 'retired'"
assert_not_grep "issue/BDS-51" "$s2" "BDS-51's block is gone when BDS-51 is the one named"

bds50_actual="$(wip_mktemp)/bds50-actual.txt"
sed -n '/^- \*\*First entry/,/issue\/BDS-50/p' "$s2" >"$bds50_actual"
assert_cmp "$bds50_expected" "$bds50_actual" \
  "MUTATION PIN: retiring BDS-51 leaves BDS-50 BYTE-IDENTICAL (kills a prune-FIRST stub directly)"

# --- idempotency ------------------------------------------------------------
# Re-running against an already-retired tracker is a quiet no-op, never an error:
# this is what makes ship/closeout/`backlog retire` safe to re-run.
after_first="$(wip_mktemp)/after-first.md"
cp "$f" "$after_first"
status2="$(_wip_backlog_retire_entry "$f" "BDS-50" "2026-07-13" "shipped as step-06")"
assert_eq "noop" "$status2" "re-retiring an already-pruned tracker reports 'noop' (not an error)"
assert_cmp "$after_first" "$f" "the second run leaves the file BYTE-IDENTICAL (no duplicate pruned marker)"

# A tracker that was never present is also `noop` on the FIRST call — most
# shipped steps simply have no matching backlog item.
status3="$(_wip_backlog_retire_entry "$f" "BDS-999" "2026-07-12" "never present")"
assert_eq "noop" "$status3" "a never-present tracker reports 'noop' on the first call (never an error)"
assert_cmp "$after_first" "$f" "a noop never writes"

# A missing file is a noop, not a crash.
assert_eq "noop" "$(_wip_backlog_retire_entry "/nonexistent/backlog.md" "BDS-50" "2026-07-12" "x")" \
  "a missing backlog file is a noop"

# --- $WIP_DRY_RUN -----------------------------------------------------------
# The status word is still computed and reported; the file is not written.
g="$(two_entry_fixture)"
g_before="$(wip_mktemp)/g-before.md"
cp "$g" "$g_before"
dry="$(WIP_DRY_RUN=1 _wip_backlog_retire_entry "$g" "BDS-50" "2026-07-12" "shipped")"
assert_eq "retired" "$dry" "\$WIP_DRY_RUN reports the status it WOULD have written"
assert_cmp "$g_before" "$g" "\$WIP_DRY_RUN leaves the file byte-identical (no write)"

# ---------------------------------------------------------------------------
# MUTATION PIN 2 — retiring the LAST entry must preserve pre-existing pruned
# history byte-for-byte.
#
# This is the pin for the end-boundary correction. Under the original
# "entry runs until the next `- **` bullet" rule, a trailing `- _(pruned …)_`
# line (prior retirement history) is NOT recognized as a terminator — it opens
# `- _(`, not `- **` — so it lands INSIDE the last entry's [start,end) span and
# the splice DELETES it. A retirement writer that destroys retirement history is
# the worst possible version of this bug, and it is invisible unless the fixture
# has pre-existing pruned lines AND the retired entry is the last one.
#
# The corrected boundary (next column-0 `- ` bullet of ANY kind) makes the pruned
# line terminate the entry above it, so it is never in splice range.
# ---------------------------------------------------------------------------
h="$(wip_mktemp)/backlog.md"
cat >"$h" <<'EOF'
# Backlog — cross-cutting

## Nice-to-have

- **The last real entry, which is the one being retired**. Multi-paragraph, and
  immediately followed by prior retirement history.

  Its second paragraph.
  ([BDS-52](https://linear.app/beausimensen/issue/BDS-52))

- _(pruned 2026-07-04 → filed as BDS-63: `wip ship` roadmap-marker writer mis-targets commented-out example bullets.)_

- _(pruned 2026-07-11 → filed as BDS-91: roadmap parse silently drops a step whose title contains `*`.)_
EOF

# Snapshot the pre-existing pruned history byte-for-byte.
history_before="$(wip_mktemp)/history-before.txt"
grep '^- _(pruned' "$h" >"$history_before"
assert_eq "2" "$(wc -l <"$history_before" | tr -d ' ')" "fixture starts with 2 pre-existing pruned lines"

status4="$(_wip_backlog_retire_entry "$h" "BDS-52" "2026-07-12" "shipped as step-06")"
assert_eq "retired" "$status4" "the last real entry retires"
assert_not_grep "issue/BDS-52" "$h" "the last real entry's block is gone"

history_after="$(wip_mktemp)/history-after.txt"
grep '^- _(pruned' "$h" | grep -v 'BDS-52' >"$history_after"
assert_cmp "$history_before" "$history_after" \
  "MUTATION PIN: pre-existing pruned lines SURVIVE byte-for-byte (kills the '^- \*\*'-only boundary, which splices them away)"
assert_grep 'BDS-63' "$h" "MUTATION PIN: the BDS-63 pruned line still exists"
assert_grep 'BDS-91' "$h" "MUTATION PIN: the BDS-91 pruned line still exists"

# The NEW marker is appended after the existing history, not interleaved into it.
assert_eq "- _(pruned 2026-07-12 → filed as BDS-52: shipped as step-06.)_" \
  "$(grep '^- _(pruned' "$h" | tail -1)" \
  "the new pruned marker is appended AFTER the pre-existing history"

# --- reason punctuation -----------------------------------------------------
# A caller that already punctuated its reason must not produce `..`.
p="$(two_entry_fixture)"
_wip_backlog_retire_entry "$p" "BDS-50" "2026-07-12" "already punctuated." >/dev/null
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-50: already punctuated\.)_$' "$p" \
  "a reason ending in '.' does not produce a doubled period"

# ---------------------------------------------------------------------------
# The roadmap front-end: a roadmap's OWN `## Backlog` section — a different
# grammar (terse one-line bullets, inline `[tracker: …]`) that already
# round-trips trackers today, so it needs no new parser.
# ---------------------------------------------------------------------------
r="$(wip_mktemp)/roadmap.md"
cat >"$r" <<'EOF'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.

## Backlog (cross-cutting)

- **Retire me** [tracker: BDS-50] — matched by tracker.
- **Leave me alone** [tracker: BDS-51] — must be untouched.

## Deferred (decided-not-now)

- **Something postponed** — revisit later.
EOF

r_before="$(wip_mktemp)/r-before.md"
cp "$r" "$r_before"

rstatus="$(_wip_roadmap_backlog_retire_entry "$r" "BDS-50" "2026-07-12" "shipped as step-06")"
assert_eq "retired" "$rstatus" "roadmap front-end retires a matching one-line backlog entry"
assert_not_grep "Retire me" "$r" "the matched roadmap backlog line is spliced out"
assert_grep "Leave me alone" "$r" \
  "MUTATION PIN: the roadmap's OTHER backlog entry is untouched (match by tracker, not position)"

# The pruned marker lands at the end of the `## Backlog` SECTION — never under a
# later `## Deferred`, which is where a naive append-at-EOF would put it.
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-50: shipped as step-06\.)_$' "$r" \
  "roadmap front-end appends the canonical pruned marker"
backlog_section="$(sed -n '/^## Backlog/,/^## Deferred/p' "$r")"
assert_eq "0" "$(grep -c 'pruned' <<<"$(sed -n '/^## Deferred/,$p' "$r")" || true)" \
  "the pruned marker is NOT appended under '## Deferred' (append-at-EOF would land it there)"
assert_eq "1" "$(grep -c 'pruned 2026-07-12' <<<"$backlog_section")" \
  "the pruned marker lands INSIDE the '## Backlog' section it belongs to"

# The roadmap's step bullets are untouched — retirement never touches rounds.
assert_grep '^- \*\*step-01 — First\*\* ✅ shipped 2026-05-01 — done\.$' "$r" \
  "roadmap step bullets are untouched by backlog retirement"

# Idempotency + dry-run, same contract as the repo front-end.
r_after="$(wip_mktemp)/r-after.md"
cp "$r" "$r_after"
assert_eq "noop" "$(_wip_roadmap_backlog_retire_entry "$r" "BDS-50" "2026-07-13" "again")" \
  "roadmap front-end is idempotent (already-retired tracker reports 'noop')"
assert_cmp "$r_after" "$r" "roadmap front-end's noop leaves the file byte-identical"
assert_eq "noop" "$(_wip_roadmap_backlog_retire_entry "$r" "BDS-777" "2026-07-12" "absent")" \
  "roadmap front-end reports 'noop' for a never-present tracker"

r2="$(wip_mktemp)/roadmap2.md"
cp "$r_before" "$r2"
r2_snapshot="$(wip_mktemp)/r2-snapshot.md"
cp "$r2" "$r2_snapshot"
assert_eq "retired" "$(WIP_DRY_RUN=1 _wip_roadmap_backlog_retire_entry "$r2" "BDS-50" "2026-07-12" "x")" \
  "roadmap front-end honors \$WIP_DRY_RUN (reports the status it would write)"
assert_cmp "$r2_snapshot" "$r2" "roadmap front-end's \$WIP_DRY_RUN performs no write"

test_summary
