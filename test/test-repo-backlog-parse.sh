#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="repo-backlog-parse"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: `_wip_repo_backlog_parse` (workplan step-06, Chunk 1) driven directly as
# a unit — the seam takes an explicit <path>, so there is no WIP_ROOT to root and
# fixtures are passed in directly (test-gitignore-sync.sh's idiom).
#
# DEFECT CONTEXT (the reason this parser exists at all). `.wip/backlog.md` is
# invisible to the existing roadmap parser, on two independent counts:
#
#   1. POSITIONAL — the file's H2 is `## Nice-to-have`, not `## Backlog`, so
#      `wip_roadmap_parse`'s backlog mode never arms. Reproduced live: today
#      `bin/wip-plumbing roadmap parse .wip/backlog.md` returns an empty
#      `.backlog[]`. Its entries are also multi-paragraph, with the tracker on a
#      TRAILING line rather than the `- **Title**` line.
#   2. GRAMMATICAL — the tracker is spelled as a markdown LINK,
#      `([BDS-14](https://…))`. `_wip_roadmap_extract_tracker` only ever matched
#      the literal `[tracker: ID]` bracket form. All 5 of the live file's tracked
#      entries use the link form; ZERO use the bracket form. So even joining the
#      body (which fixes defect 1) still resolves `.tracker == null` on every
#      real entry — fixing the positional half alone is necessary but NOT
#      sufficient. Mutation pin 1 below reproduces exactly that.
#
# HARD BOUNDARY: every fixture here is a disposable tmp file. This suite never
# reads, writes, or parses the live `.wip/backlog.md`.
#
# Contract: workplan step-06, Chunk 1 + Test strategy bullet 1.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1

# shellcheck source=lib/wip/wip-plumbing-roadmap-lib.bash
source lib/wip/wip-plumbing-roadmap-lib.bash
# shellcheck source=lib/wip/wip-plumbing-repo-backlog-lib.bash
source lib/wip/wip-plumbing-repo-backlog-lib.bash

# The fixture reproduces every shape the live file actually contains:
#   (a) multi-paragraph entry, tracker as a markdown LINK on a trailing line
#   (b) multi-paragraph entry, tracker as a literal `[tracker: …]` bracket marker
#       AND a contradictory trailing link (the extraction-order pin)
#   (c) entry whose PROSE cites another issue's link mid-body, with no trailing
#       tracker of its own (the mis-attribution pin)
#   (d) single-paragraph entry with no tracker in either form
#   (e) a real entry immediately followed by an existing `- _(pruned …)_` line
fixture="$(wip_mktemp)/backlog.md"
cat >"$fixture" <<'EOF'
# Backlog — cross-cutting

Some preamble prose.

## Nice-to-have

- **Solo TODO lifecycle hygiene** (from closeout-write-completion). Running
  Round 1 left dangling **open** Solo todos for shipped work, so
  `todo_list(completed=false)` no longer reflected reality.

  A second paragraph, because these entries are multi-paragraph prose blocks.
  ([BDS-14](https://linear.app/beausimensen/issue/BDS-14))

- **Bracket-form entry wins over a trailing link** [tracker: BDS-20]. This entry
  carries BOTH forms, and they disagree on purpose: the bracket marker says
  BDS-20, the trailing link says BDS-21. The bracket form is authoritative.
  ([BDS-21](https://linear.app/beausimensen/issue/BDS-21))

- **Entry that merely cites another issue mid-prose**. Its body links
  ([BDS-99](https://linear.app/beausimensen/issue/BDS-99)) as a cross-reference,
  but that link is NOT on this entry's trailing line, so it must never be
  mistaken for this entry's own tracker. This closing line has no link at all.

- **Single-paragraph entry with no tracker of any kind**.

- **Last real entry, followed by pruned history**. Retiring this one must not
  disturb the pruned lines below it.

- _(pruned 2026-07-04 → filed as BDS-63: `wip ship` roadmap-marker writer mis-targets commented-out example bullets.)_

- _(pruned 2026-07-11 → filed as BDS-91: roadmap parse silently drops a step whose title contains `*`.)_
EOF

doc="$(_wip_repo_backlog_parse "$fixture")"

# --- entry detection --------------------------------------------------------
# 5 real entries. The two `- _(pruned …)_` lines are NOT entries: they open
# `- _(`, which the `^- \*\*` entry-open regex never matches.
assert_eq "5" "$(jq 'length' <<<"$doc")" \
  "parses exactly the 5 real entries (both pruned lines excluded)"
assert_eq "0" "$(jq '[.[] | select(.id | startswith("pruned"))] | length' <<<"$doc")" \
  'no "- _(pruned …)_" line is ever parsed as an entry'

# --- (a) markdown-link tracker on a trailing line ---------------------------
# The exact live-file shape, and the case the pre-existing parser cannot see.
assert_eq "BDS-14" "$(jq -r '.[0].tracker' <<<"$doc")" \
  "(a) resolves a markdown-link tracker on a TRAILING line — the live-file shape"
assert_eq "solo-todo-lifecycle-hygiene" "$(jq -r '.[0].id' <<<"$doc")" \
  "(a) id is slugify(title), via _wip_roadmap_slugify"

# --- (b) bracket form is authoritative over a contradictory link ------------
# Extraction order is load-bearing: _wip_roadmap_extract_tracker FIRST, link
# fallback SECOND, never the reverse. This entry carries both forms, disagreeing
# on purpose — a parser that ran the fallback first would answer BDS-21.
assert_eq "BDS-20" "$(jq -r '.[1].tracker' <<<"$doc")" \
  "(b) literal [tracker:] form WINS over a contradictory trailing link (order pin)"

# --- (c) a mid-prose citation is never mis-attributed -----------------------
# The link fallback is anchored to the entry's trailing line specifically. Entry
# bodies routinely cite OTHER issues; since retirement matches on this id, a
# "first link anywhere in the body" fallback would delete the wrong entry.
assert_eq "null" "$(jq -r '.[2].tracker' <<<"$doc")" \
  "(c) an issue link cited MID-PROSE is not mistaken for the entry's own tracker"

# --- (d) no tracker at all --------------------------------------------------
assert_eq "null" "$(jq -r '.[3].tracker' <<<"$doc")" \
  "(d) an entry with no tracker of either form resolves .tracker == null"

# --- (e) the corrected end boundary -----------------------------------------
# The whole point of the boundary correction. The last real entry must END at the
# first pruned line, NOT run to EOF. If it ran to EOF, the pruned lines would sit
# inside its [start_line, end_line) span — and chunk 2 splices that span out, so
# retiring this entry would DELETE the file's accumulated retirement history.
pruned_line="$(grep -n 'BDS-63' "$fixture" | cut -d: -f1)"
last_end="$(jq -r '.[4].end_line' <<<"$doc")"
assert_eq "$pruned_line" "$last_end" \
  "(e) last entry's end_line stops AT the first pruned line (not EOF) — pruned history is out of splice range"

# Spans are half-open and contiguous-by-construction; prove the ranges are sane
# rather than trusting them (a bad end_line silently over-splices).
assert_eq "true" "$(jq -r 'all(.[]; .start_line < .end_line)' <<<"$doc")" \
  "every entry's span is non-empty (start_line < end_line, end exclusive)"

# --- a wrapped title still parses -------------------------------------------
# The live file has a title that wraps across two physical lines, so title and
# tracker are read from the JOINED body, never the opening line alone.
wrapfix="$(wip_mktemp)/backlog.md"
cat >"$wrapfix" <<'EOF'
## Nice-to-have

- **A title that wraps across two physical lines and keeps going
  until its closing marker lands on line two** (body follows here).
  ([BDS-30](https://linear.app/beausimensen/issue/BDS-30))
EOF
wrapdoc="$(_wip_repo_backlog_parse "$wrapfix")"
assert_eq "1" "$(jq 'length' <<<"$wrapdoc")" "a title wrapped across lines still parses as one entry"
assert_eq "A title that wraps across two physical lines and keeps going until its closing marker lands on line two" \
  "$(jq -r '.[0].title' <<<"$wrapdoc")" "wrapped title is joined and whitespace-collapsed"
assert_eq "BDS-30" "$(jq -r '.[0].tracker' <<<"$wrapdoc")" "wrapped-title entry still resolves its trailing-line tracker"

# --- an indented sub-bullet stays inside the body ---------------------------
# Open Question 3: both the entry-open and the boundary regex are column-0
# anchored, so a nested `  - foo` terminates nothing.
subfix="$(wip_mktemp)/backlog.md"
cat >"$subfix" <<'EOF'
## Nice-to-have

- **Entry with nested sub-bullets**. Fix directions:
  - first sub-bullet
  - second sub-bullet
  ([BDS-40](https://linear.app/beausimensen/issue/BDS-40))

- **Following entry**. Separate.
EOF
subdoc="$(_wip_repo_backlog_parse "$subfix")"
assert_eq "2" "$(jq 'length' <<<"$subdoc")" \
  "an INDENTED sub-bullet does not open a new entry nor terminate the body"
assert_eq "BDS-40" "$(jq -r '.[0].tracker' <<<"$subdoc")" \
  "an entry whose body holds sub-bullets still resolves its trailing-line tracker"

# --- missing file -----------------------------------------------------------
assert_eq "[]" "$(_wip_repo_backlog_parse "/nonexistent/backlog.md")" \
  "a missing backlog file parses to [] (a repo need not have one)"

# ---------------------------------------------------------------------------
# MUTATION PIN 1 — the markdown-link fallback is load-bearing.
#
# Strip the fallback (stub it to return empty) and re-run against fixture (a),
# the live-shaped entry. It MUST revert to `.tracker == null` — reproducing the
# exact defect found live during this step's build, where the workplan's original
# "join the body and call _wip_roadmap_extract_tracker" spec resolved null for
# every one of the live file's 5 tracked entries.
#
# A pin that still passes with the fallback removed is not testing the fallback.
# This one demonstrates the dependency by executing the mutant, rather than
# asserting it in a comment.
# ---------------------------------------------------------------------------
_wip_repo_backlog_extract_link_tracker_real() { :; }
eval "$(declare -f _wip_repo_backlog_extract_link_tracker |
  sed '1s/^_wip_repo_backlog_extract_link_tracker/_wip_repo_backlog_extract_link_tracker_real/')"

# The mutant: the parser WITHOUT its link-form fallback (i.e. bracket form only,
# which is precisely the original, insufficient spec).
_wip_repo_backlog_extract_link_tracker() { :; }

mutdoc="$(_wip_repo_backlog_parse "$fixture")"
assert_eq "null" "$(jq -r '.[0].tracker' <<<"$mutdoc")" \
  "MUTATION PIN: with the link fallback stripped, the live-shaped entry reverts to .tracker == null (the real defect)"
assert_eq "BDS-20" "$(jq -r '.[1].tracker' <<<"$mutdoc")" \
  "MUTATION PIN: the bracket-form entry is UNAFFECTED by stripping the fallback (proves the two forms are independent paths)"

# Restore the real fallback and prove the pin was a true mutation, not a
# permanently-broken parser: the same fixture resolves again.
eval "$(declare -f _wip_repo_backlog_extract_link_tracker_real |
  sed '1s/^_wip_repo_backlog_extract_link_tracker_real/_wip_repo_backlog_extract_link_tracker/')"
assert_eq "BDS-14" "$(jq -r '.[0].tracker' <<<"$(_wip_repo_backlog_parse "$fixture")")" \
  "MUTATION PIN: restoring the fallback resolves BDS-14 again (the mutation was real and reversible)"

test_summary
