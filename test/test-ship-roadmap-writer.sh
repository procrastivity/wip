#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-roadmap-writer"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the roadmap `✅ shipped` marker writer
# (_wip_ship_mark_roadmap_shipped) driven END-TO-END through `bin/wip-plumbing
# ship`. Assertions are ROADMAP-SCOPED only — the marked bullet and the ledger's
# `marked_shipped` — never `active_step_cleared` / `changed`, whose values
# depend on step-03's manifest writer (still the inert stub here). Contract:
# ADR-0016 (engineering/decisions/0016-closeout-write-contract.md).
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-27

ROADMAP_REL=".wip/initiatives/demo/roadmap.md"
declare -a TMP_DIRS=()
cleanup() {
  local d
  for d in "${TMP_DIRS[@]}"; do rm -rf "$d"; done
}
trap cleanup EXIT

# setup_roadmap <roadmap-content> — write a fresh demo fixture into a new tmp
# root and set the globals `tmp` (root) and `roadmap` (absolute roadmap path).
setup_roadmap() {
  tmp="$(mktemp -d)"
  TMP_DIRS+=("$tmp")
  mkdir -p "$tmp/.wip/initiatives/demo"
  cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-02
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
  printf '%s\n' "$1" >"$tmp/$ROADMAP_REL"
  roadmap="$tmp/$ROADMAP_REL"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# step02_line — echo the roadmap's step-02 bullet first line.
step02_line() { grep -F '**step-02 —' "$roadmap"; }

# ---------------------------------------------------------------------------
# 1. Marker insertion: an unmarked bullet gains ` ✅ shipped <date>` right after
#    the closing `**`, the ` (tier) — description` tail is preserved, and the
#    writer reports `updated`.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** (small) — current.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "insert: marked_shipped updated"
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "insert: marker after ** with tail preserved"

# ---------------------------------------------------------------------------
# 2. Wrapped-bullet preservation: a multi-line bullet keeps its continuation
#    lines byte-for-byte after marking (guards against block-replace data loss).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Refresh tokens** (small) — current,
  with a wrapped continuation line,
  and another one.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "wrapped: marked_shipped updated"
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current,' \
  "$(step02_line)" "wrapped: first line rebuilt"
assert_grep '  with a wrapped continuation line,' "$roadmap" "wrapped: continuation line 1 kept"
assert_grep '  and another one.' "$roadmap" "wrapped: continuation line 2 kept"

# ---------------------------------------------------------------------------
# 3. Date normalization — wrong date corrected, no duplicate marker, `updated`.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Refresh tokens** ✅ shipped 2026-01-01 (small) — current.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "wrong-date: marked_shipped updated"
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "wrong-date: corrected to target date"
assert_eq "1" "$(step02_line | grep -o '✅' | wc -l | tr -d ' ')" "wrong-date: exactly one marker"

# ---------------------------------------------------------------------------
# 4. Date normalization — missing date corrected (`✅` without a date), `updated`.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Refresh tokens** ✅ (small) — current.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "missing-date: marked_shipped updated"
assert_eq '- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "missing-date: marker completed with target date"
assert_eq "1" "$(step02_line | grep -o '✅' | wc -l | tr -d ' ')" "missing-date: exactly one marker"

# ---------------------------------------------------------------------------
# 5. Already-shipped no-op: correct marker already present → `noop` AND the
#    roadmap file is byte-identical before/after (proves the no-write path).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Refresh tokens** ✅ shipped 2026-06-27 (small) — current.'
before="$(mktemp)"
TMP_DIRS+=("$before")
cp "$roadmap" "$before"
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out")" "noop: marked_shipped noop"
assert_cmp "$before" "$roadmap" "noop: roadmap byte-identical (no write)"

# ---------------------------------------------------------------------------
# 6. step-02-self gotcha: a step whose TITLE contains a backtick-wrapped
#    `✅ shipped` must not be read as shipped — the marker is inserted after
#    `**` (yielding both the in-title ✅ and a real marker), `updated`; a re-run
#    is `noop`. Directly exercises the srest-only read (D2).
# The roadmap content and expected line carry LITERAL backticks (the title is
# `✅ shipped` verbatim); single quotes are intentional, so silence SC2016.
# ---------------------------------------------------------------------------
# shellcheck disable=SC2016
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — `✅ shipped` marker writer** (small) — Implement the writer.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "self-gotcha: in-title ✅ not read as shipped"
# shellcheck disable=SC2016
assert_eq '- **step-02 — `✅ shipped` marker writer** ✅ shipped 2026-06-27 (small) — Implement the writer.' \
  "$(step02_line)" "self-gotcha: marker inserted after ** alongside in-title ✅"
out2="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out2")" "self-gotcha: re-run is noop"

# ---------------------------------------------------------------------------
# 7. Step-01 closeout-write-ladder writer parity pin: a title containing a
#    literal `*` must use the same closing-`**` split as the parser, so `ship`
#    inserts the marker immediately after the title-closing `**` instead of
#    failing to resolve/rewrite the bullet.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Use * wildcard** (small) — current.'
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "special-title: marked_shipped updated"
assert_eq '- **step-02 — Use * wildcard** ✅ shipped 2026-06-27 (small) — current.' \
  "$(step02_line)" "special-title: marker inserted after closing **"

# ---------------------------------------------------------------------------
# 8. --dry-run: reports `updated` and `dry_run: true`, but the roadmap file is
#    unchanged (no write).
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

## Round 1 — Build

- **step-02 — Refresh tokens** (small) — current.'
before_dry="$(mktemp)"
TMP_DIRS+=("$before_dry")
cp "$roadmap" "$before_dry"
out="$(run demo step-02 --dry-run)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "dry-run: marked_shipped updated"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "dry-run: dry_run true in ledger"
assert_cmp "$before_dry" "$roadmap" "dry-run: roadmap unchanged (no write)"

# ---------------------------------------------------------------------------
# 9. Step-02 regression pin 3: a step-id that exists only inside an HTML
#    comment span reports a specific shadowed-anchor error instead of updating
#    an inert scaffold bullet or returning a successful marked_shipped ledger.
# ---------------------------------------------------------------------------
setup_roadmap '# Roadmap

<!--
## Round 0 — Example
- **step-02 — Commented example** — inert.
-->

## Round 1 — Build
- **step-01 — Real** — work.'
set +e
out_shadow="$(run demo step-02 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "comment-shadowed: exit 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out_shadow")" "comment-shadowed: envelope present"
assert_eq "step-shadowed-in-comment" "$(jq -r '.error.kind' <<<"$out_shadow")" "comment-shadowed: error kind"
assert_eq "null" "$(jq -r '.marked_shipped // null' <<<"$out_shadow")" "comment-shadowed: no marked_shipped updated ledger"
assert_not_grep "✅ shipped 2026-06-27" "$roadmap" "comment-shadowed: roadmap unchanged"

test_summary
