#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="closeout-retires-backlog"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: `closeout`'s backlog-retirement seam (workplan step-06, Chunk 4), driven
# END-TO-END through `bin/wip-plumbing closeout`.
#
# `closeout` is the COMPREHENSIVE backstop, not an incremental writer: it has no
# single "node being shipped" the way `ship` does (it clears `active_step`
# unconditionally, so it does not even carry one), and the thing it closes is the
# whole roadmap. So it retires EVERY non-null step tracker across EVERY round, in
# ONE run — which is exactly what the fixture here is built to pin.
#
# THE LOAD-BEARING PIN: the fixture puts BDS-60 in round 1 and BDS-61 in round 2,
# each with a matching entry in BOTH backlog shapes, and demands all four are
# retired by a SINGLE closeout. The plausible-wrong implementation retires one
# tracker (whatever `active_step` names, or the last round's, or the first
# match) — it passes any single-tracker fixture and dies here.
#
# HARD BOUNDARY: every fixture is a disposable tmp root. This suite NEVER touches
# the live `.wip/backlog.md` — pruning live entries is the Orchestrator's call
# alone (workplan Open Question 1).
#
# The retirement MECHANICS (splice ranges, pruned-marker convention, preserving
# prior pruned history, the markdown-link tracker form) are owned by
# test-backlog-retire.sh / test-repo-backlog-parse.sh and are NOT re-tested here.
# What is tested here is the WIRING: which trackers closeout collects, that it
# collects them across all rounds, what it reports, and how that folds into
# `changed`.
#
# Contract: workplan step-06, Decision 1 + Chunk 4 + Test strategy bullet 3.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-07-12

# setup_cb [backlog] — fresh fixture root; sets globals `tmp`, `roadmap`,
# `backlog`.
#
# The roadmap is fully shipped (every round heading ✅ marked, every step bullet
# ✅ marked) so the all-shipped guard passes and the retirement seam is actually
# reached. Its steps:
#
#   round 1: step-01 [tracker: BDS-60]   <- a tracked step in the FIRST round
#            step-02 (no tracker)        <- contributes NO status word at all
#   round 2: step-03 [tracker: BDS-61]   <- a tracked step in a LATER round
#
# `backlog`:
#   present  (default) `.wip/backlog.md` carries BDS-60, BDS-61 and an untouchable
#            BDS-62 (the match-by-tracker pin).
#   absent   no `.wip/backlog.md` at all — a repo need not keep one.
setup_cb() {
  local bl="${1:-present}"
  tmp="$(wip_mktemp)"
  mkdir -p "$tmp/.wip/initiatives/demo"

  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    printf 'current_initiative: demo\n'
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    printf '    active_step: step-03\n'
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
  } >"$tmp/.wip.yaml"

  # The roadmap carries BOTH the step bullets (whose trackers closeout collects)
  # and its own `## Backlog` section (one of the two shapes it retires from).
  {
    printf '# Roadmap — demo\n\n'
    printf '## Round 1 — Build ✅ shipped 2026-05-02\n\n'
    printf -- '- **step-01 — Auth** [tracker: BDS-60] ✅ shipped 2026-05-01 — done.\n'
    printf -- '- **step-02 — Untracked** ✅ shipped 2026-05-02 — done.\n\n'
    printf '## Round 2 — Polish ✅ shipped 2026-06-01\n\n'
    printf -- '- **step-03 — Rotation** [tracker: BDS-61] ✅ shipped 2026-06-01 — done.\n\n'
    printf '## Backlog (cross-cutting)\n\n'
    printf -- '- **Retire with round 1** [tracker: BDS-60] — matched by step-01.\n'
    printf -- '- **Retire with round 2** [tracker: BDS-61] — matched by step-03.\n'
    printf -- '- **Leave me alone** [tracker: BDS-62] — no step carries BDS-62.\n\n'
    printf '## Deferred (decided-not-now)\n\n'
    printf -- '- **Something postponed** — revisit later.\n'
  } >"$tmp/.wip/initiatives/demo/roadmap.md"

  # The repo backlog: multi-paragraph prose entries in the live file's shape —
  # tracker as a markdown LINK on the entry's trailing line, under a
  # `## Nice-to-have` heading (there is no `## Backlog` H2 in the real file).
  if [[ "$bl" == "present" ]]; then
    cat >"$tmp/.wip/backlog.md" <<'EOF'
# Backlog — cross-cutting

## Nice-to-have

- **First round's item**. Multi-paragraph prose, exactly like the live file's
  entries.

  Its second paragraph, to prove the whole block is spliced.
  ([BDS-60](https://linear.app/beausimensen/issue/BDS-60))

- **Second round's item**. This one is only reachable if closeout looks past the
  round the initiative happened to end on.

  Its second paragraph.
  ([BDS-61](https://linear.app/beausimensen/issue/BDS-61))

- **Nobody's item**. No step in the roadmap carries BDS-62, so a correct
  closeout leaves this block byte-identical.

  Its second paragraph.
  ([BDS-62](https://linear.app/beausimensen/issue/BDS-62))
EOF
  fi

  # No `manifest` global: the manifest seams are owned by
  # test-closeout-end-to-end.sh, so every assertion here reads the two backlog
  # shapes or the ledger.
  roadmap="$tmp/.wip/initiatives/demo/roadmap.md"
  backlog="$tmp/.wip/backlog.md"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing closeout "$@"; }

snapshot() {
  local s
  s="$(wip_mktemp)/snap"
  cp "$1" "$s"
  printf '%s\n' "$s"
}

# bds62_block <file> — BDS-62's block, extracted for a byte-for-byte compare.
bds62_block() {
  local out
  out="$(wip_mktemp)/bds62.txt"
  sed -n "/^- \*\*Nobody's item/,/issue\/BDS-62/p" "$1" >"$out"
  printf '%s\n' "$out"
}

# ---------------------------------------------------------------------------
# Case A — THE LOAD-BEARING PIN. One closeout run retires BOTH trackers, from
#   BOTH shapes, ACROSS both rounds. Asserted in the ledger AND on disk — a right
#   ledger over a wrong write (or the reverse) is exactly what a ledger-only
#   assertion misses.
#
#   A stub that retires only ONE tracker — the obvious wrong one being
#   `active_step`'s (which closeout does not even carry: it clears it
#   unconditionally) or "the first matching entry" — dies on every BDS-61
#   assertion below while passing every BDS-60 one.
# ---------------------------------------------------------------------------
setup_cb
bds62_expected="$(bds62_block "$backlog")"
out="$(run demo)"

assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "retired,retired" \
  "$(jq -r '.backlog_retired.repo | join(",")' <<<"$out")" \
  "A: PIN — the ledger reports BOTH trackers retired from the repo backlog"
assert_eq "retired,retired" \
  "$(jq -r '.backlog_retired.roadmap | join(",")' <<<"$out")" \
  "A: PIN — the ledger reports BOTH trackers retired from the roadmap's own backlog"
assert_eq "2" "$(jq -r '.backlog_retired.repo | length' <<<"$out")" \
  "A: one status word per TRACKER attempted — the untracked step-02 adds none"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"

# On-disk proof, repo backlog: both blocks spliced out whole, both markers written.
assert_not_grep "issue/BDS-60" "$backlog" "A: BDS-60's block is gone from the repo backlog"
assert_not_grep "issue/BDS-61" "$backlog" \
  "A: PIN — BDS-61's block is gone too (a one-tracker stub leaves this behind)"
assert_not_grep "First round's item" "$backlog" "A: BDS-60's whole block was spliced, not just its link"
assert_not_grep "Second round's item" "$backlog" "A: BDS-61's whole block was spliced too"
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-60: shipped with initiative demo\.)_$' "$backlog" \
  "A: BDS-60's canonical pruned marker is appended"
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-61: shipped with initiative demo\.)_$' "$backlog" \
  "A: PIN — BDS-61's pruned marker is appended too"

# On-disk proof, the roadmap's own `## Backlog` section.
assert_not_grep "Retire with round 1" "$roadmap" "A: the roadmap's BDS-60 backlog line is gone"
assert_not_grep "Retire with round 2" "$roadmap" \
  "A: PIN — the roadmap's BDS-61 backlog line is gone too"
assert_grep 'filed as BDS-60' "$roadmap" "A: the roadmap carries BDS-60's pruned marker"
assert_grep 'filed as BDS-61' "$roadmap" "A: the roadmap carries BDS-61's pruned marker"

# MUTATION PIN — match by TRACKER, not by position. No step carries BDS-62, so a
# writer that prunes "whatever entry it finds" rather than the tracker it was
# asked for disturbs this block. Byte-identical, in both shapes.
assert_cmp "$bds62_expected" "$(bds62_block "$backlog")" \
  "A: MUTATION PIN — BDS-62's repo-backlog block is BYTE-IDENTICAL (no step carries it)"
assert_grep '^- \*\*Leave me alone\*\* \[tracker: BDS-62\] — no step carries BDS-62\.$' "$roadmap" \
  "A: MUTATION PIN — the roadmap's BDS-62 backlog line is untouched"

# Retirement never touches the ROUNDS: closeout still only reads those.
assert_grep '^- \*\*step-01 — Auth\*\* \[tracker: BDS-60\] ✅ shipped 2026-05-01 — done\.$' "$roadmap" \
  "A: the roadmap's step bullets are untouched by backlog retirement"
assert_grep '^- \*\*step-03 — Rotation\*\* \[tracker: BDS-61\] ✅ shipped 2026-06-01 — done\.$' "$roadmap" \
  "A: the round-2 step bullet is untouched too"
# The pruned markers land inside `## Backlog`, never under the later `## Deferred`.
assert_eq "0" "$(sed -n '/^## Deferred/,$p' "$roadmap" | grep -c 'pruned' || true)" \
  "A: no pruned marker lands under '## Deferred'"

# The three manifest seams still ran — retirement is additive, not a substitute.
assert_eq "updated" "$(jq -r '.status_set' <<<"$out")" "A: status_set still updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "A: active_step still cleared"

# ---------------------------------------------------------------------------
# Case B — idempotency. A second closeout finds nothing left to prune: every
#   status word is `noop`, `changed` is false, and BOTH files are byte-identical.
#   "exit 0 twice" would pass against a verb that re-appended a pruned marker
#   every run; the cmp is the real assertion.
# ---------------------------------------------------------------------------
bl_snap="$(snapshot "$backlog")"
rm_snap="$(snapshot "$roadmap")"
out2="$(run demo)"
assert_eq "noop,noop" "$(jq -r '.backlog_retired.repo | join(",")' <<<"$out2")" \
  "B: re-run reports noop for every repo tracker"
assert_eq "noop,noop" "$(jq -r '.backlog_retired.roadmap | join(",")' <<<"$out2")" \
  "B: re-run reports noop for every roadmap tracker"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "B: re-run changed false"
assert_cmp "$bl_snap" "$backlog" "B: repo backlog BYTE-IDENTICAL across the re-run"
assert_cmp "$rm_snap" "$roadmap" "B: roadmap BYTE-IDENTICAL across the re-run"

# ---------------------------------------------------------------------------
# Case C — `changed` folding. The backlog seams speak `retired`/`noop`, not
#   `updated`/`noop`, so folding them into `changed` is its own line of code and
#   its own way to be wrong. Here the manifest is ALREADY closed out (all three
#   manifest seams report noop/skipped) and only a restored backlog entry is left
#   to retire — so `changed` is true ONLY if the backlog seams reach the fold.
# ---------------------------------------------------------------------------
cat >>"$backlog" <<'EOF'

- **Back from the dead**. Filed after the step had already shipped — precisely
  the case `closeout` exists to backstop.

  ([BDS-60](https://linear.app/beausimensen/issue/BDS-60))
EOF
out3="$(run demo)"
assert_eq "noop" "$(jq -r '.status_set' <<<"$out3")" "C: manifest status seam is noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out3")" "C: active_step seam is noop"
assert_eq "retired,noop" "$(jq -r '.backlog_retired.repo | join(",")' <<<"$out3")" \
  "C: the re-filed BDS-60 entry is retired"
assert_eq "true" "$(jq -r '.changed' <<<"$out3")" \
  "C: PIN — changed is true from the BACKLOG seam alone (every manifest seam was a noop)"
assert_not_grep "Back from the dead" "$backlog" "C: the re-filed entry is gone"

# ---------------------------------------------------------------------------
# Case D — --dry-run: the full ledger is computed (both status arrays included)
#   but NOTHING is written, to either shape. The retirement front-ends read
#   $WIP_DRY_RUN, which closeout exports before any seam runs.
# ---------------------------------------------------------------------------
setup_cb
bl_before="$(snapshot "$backlog")"
rm_before="$(snapshot "$roadmap")"
out="$(run demo --dry-run)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "D: dry_run true"
assert_eq "retired,retired" "$(jq -r '.backlog_retired.repo | join(",")' <<<"$out")" \
  "D: dry-run reports the repo statuses it WOULD have written"
assert_eq "retired,retired" "$(jq -r '.backlog_retired.roadmap | join(",")' <<<"$out")" \
  "D: dry-run reports the roadmap statuses it WOULD have written"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D: dry-run changed true"
assert_cmp "$bl_before" "$backlog" "D: repo backlog UNWRITTEN under --dry-run"
assert_cmp "$rm_before" "$roadmap" "D: roadmap UNWRITTEN under --dry-run"

# ---------------------------------------------------------------------------
# Case E — no `.wip/backlog.md` at all. A repo need not keep one: every repo
#   status word is `noop` (never an error, never a crash), while the roadmap's
#   own backlog is still retired normally.
# ---------------------------------------------------------------------------
setup_cb absent
assert_absent "$backlog" "E: fixture really has no repo backlog"
out="$(run demo)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "E: a missing repo backlog is not an error"
assert_eq "noop,noop" "$(jq -r '.backlog_retired.repo | join(",")' <<<"$out")" \
  "E: every repo status word is noop when the file is absent"
assert_eq "retired,retired" "$(jq -r '.backlog_retired.roadmap | join(",")' <<<"$out")" \
  "E: the roadmap's own backlog is still retired"
assert_absent "$backlog" "E: no backlog file is CREATED by a retirement attempt"

# ---------------------------------------------------------------------------
# Case F — a refused closeout retires nothing. The all-shipped guard runs BEFORE
#   the retirement seam (as it does before every other writer), so a roadmap that
#   is not fully shipped leaves both backlog shapes byte-identical.
# ---------------------------------------------------------------------------
setup_cb
# Un-ship round 2's heading marker — the §2j drift the guard exists to catch.
sed -i.bak 's/^## Round 2 — Polish ✅ shipped 2026-06-01$/## Round 2 — Polish/' "$roadmap"
rm -f "$roadmap.bak"
bl_before="$(snapshot "$backlog")"
rm_before="$(snapshot "$roadmap")"
set +e
out="$(run demo 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "F: an unshipped round still refuses with exit 4"
assert_eq "not-all-shipped" "$(jq -r '.error.kind' <<<"$out")" "F: kind not-all-shipped"
assert_cmp "$bl_before" "$backlog" \
  "F: PIN — the repo backlog is BYTE-IDENTICAL across a refused run (the guard precedes retirement)"
assert_cmp "$rm_before" "$roadmap" "F: the roadmap is BYTE-IDENTICAL across a refused run"

test_summary
