#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-retires-backlog"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: `ship`'s BACKLOG RETIREMENT seams (workplan step-06, Chunk 3), driven
# end-to-end through `bin/wip-plumbing ship`. A step whose roadmap bullet carries
# a `[tracker: BDS-NN]` marker retires the matching entry in BOTH backlogs — the
# repo-level `.wip/backlog.md` (multi-paragraph grammar) and the initiative
# roadmap's own `## Backlog` section (one-line grammar) — and reports both in
# `backlog_retired: {repo, roadmap}`, folded into `changed`.
#
# The retirement mechanics themselves (splice ranges, pruned-marker convention,
# boundary handling) are owned by test-backlog-retire.sh and NOT re-tested here.
# What this suite owns is the WIRING: that ship calls BOTH front-ends, that it
# calls them ONLY on a tracker match, and that the ledger + `changed` reflect it.
#
# HARD BOUNDARY: every fixture is a disposable tmp root. This suite NEVER touches
# the live `.wip/backlog.md` or any live roadmap — pruning the live backlog is
# the Orchestrator's call alone (workplan Open Question 1).
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-27

# setup [tracker] [presettled] — build a fresh fixture root and set the globals
# `tmp`, `roadmap`, `repo_backlog`.
#   tracker    : the `[tracker: …]` marker on step-02's bullet. Default "BDS-60"
#                (the matching case); pass "" to give step-02 NO tracker at all —
#                the mutation-pin fixture for "retires unconditionally".
#   presettled : "1" → step-02's bullet is ALREADY marked shipped and
#                `active_step` is unset, so both of ship's original writer seams
#                report `noop` and the retirement seams are the only ones left
#                that can report anything. That is the only fixture in which the
#                `changed` fold over the retirement seams is observable at all.
#
# Both backlogs carry TWO tracked entries: BDS-60 (the one step-02 names) and
# BDS-61 (which must survive every run untouched — the match-by-tracker pin).
# Round 1 keeps an unshipped step-03 so the round seam never fires and `changed`
# stays attributable to the seams under test.
setup() {
  local tracker="${1-BDS-60}" presettled="${2:-0}" marker="" shipped=""
  [[ -n "$tracker" ]] && marker=" [tracker: $tracker]"
  [[ "$presettled" == "1" ]] && shipped=" ✅ shipped 2026-06-27"

  tmp="$(wip_mktemp)"
  mkdir -p "$tmp/.wip/initiatives/demo"

  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    printf 'current_initiative: demo\n'
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    [[ "$presettled" == "1" ]] || printf '    active_step: step-02\n'
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
  } >"$tmp/.wip.yaml"

  {
    printf '# Roadmap — demo\n\n'
    printf '## Round 1 — One\n\n'
    printf -- '- **step-01 — First** ✅ shipped 2026-05-01 — done.\n'
    printf -- '- **step-02 — Second**%s%s — current.\n' "$shipped" "$marker"
    printf -- '- **step-03 — Third** — later.\n\n'
    printf '## Backlog (cross-cutting)\n\n'
    printf -- '- **Roadmap backlog item, retired by step-02** [tracker: BDS-60] — matched by tracker.\n'
    printf -- '- **Roadmap backlog item nobody named** [tracker: BDS-61] — must be untouched.\n\n'
    printf '## Deferred (decided-not-now)\n\n'
    printf -- '- **Something postponed** — revisit later.\n'
  } >"$tmp/.wip/initiatives/demo/roadmap.md"

  # The repo backlog uses the live file's shape: multi-paragraph prose blocks
  # under `## Nice-to-have`, tracker spelled as a markdown link on a TRAILING
  # line — not the roadmap's terse `[tracker: …]` one-liner.
  cat >"$tmp/.wip/backlog.md" <<'EOF'
# Backlog — cross-cutting

## Nice-to-have

- **Repo backlog item, retired by step-02**. A multi-paragraph block, exactly
  like the live file's entries.

  Its second paragraph, so "the whole block was spliced" is observable.
  ([BDS-60](https://linear.app/beausimensen/issue/BDS-60))

- **Repo backlog item nobody named**. Must survive every run byte-identical.

  Its own second paragraph.
  ([BDS-61](https://linear.app/beausimensen/issue/BDS-61))
EOF

  roadmap="$tmp/.wip/initiatives/demo/roadmap.md"
  repo_backlog="$tmp/.wip/backlog.md"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# roadmap_backlog_section — the roadmap's `## Backlog` section only, so a pruned
# marker landing under `## Deferred` can never be mistaken for a pass.
roadmap_backlog_section() { sed -n '/^## Backlog/,/^## Deferred/p' "$roadmap"; }

# ---------------------------------------------------------------------------
# Case A — the matching case. ONE `ship` invocation retires the step's tracker
#   from BOTH backlogs and reports both in the ledger.
#
# This case IS the "forgot the roadmap front-end" mutation pin: an implementation
# that wires only `_wip_backlog_retire_entry` (the repo backlog) and never calls
# `_wip_roadmap_backlog_retire_entry` passes every repo assertion below and fails
# every roadmap one — the roadmap's `## Backlog` entry survives and
# `.backlog_retired.roadmap` is not `retired`. Both halves are asserted precisely
# so half a wiring cannot look like a whole one.
# ---------------------------------------------------------------------------
setup
out="$(run demo step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "retired" "$(jq -r '.backlog_retired.repo' <<<"$out")" \
  "A: ledger reports the REPO backlog entry retired"
assert_eq "retired" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" \
  "A: ledger reports the ROADMAP backlog entry retired (pins the second front-end)"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"

# The repo backlog: BDS-60's whole block is gone, a canonical pruned marker
# replaces it, and the date/reason come from ship's own shipped_date + node id.
assert_not_grep "Repo backlog item, retired by step-02" "$repo_backlog" \
  "A: BDS-60's repo entry title is gone"
assert_not_grep "issue/BDS-60" "$repo_backlog" "A: BDS-60's repo tracker link is gone"
assert_grep '^- _(pruned 2026-06-27 → filed as BDS-60: shipped as demo/step-02\.)_$' "$repo_backlog" \
  "A: the repo backlog carries a canonical pruned marker dated from ship's shipped_date"

# The roadmap's own `## Backlog` section: same treatment, different grammar.
assert_not_grep "Roadmap backlog item, retired by step-02" "$roadmap" \
  "A: the roadmap's matching ## Backlog bullet is spliced out"
assert_eq "1" "$(grep -c 'pruned 2026-06-27 → filed as BDS-60' <<<"$(roadmap_backlog_section)")" \
  "A: the roadmap's pruned marker lands INSIDE its ## Backlog section"

# The step's own writes are unaffected by the new seams.
assert_grep '^- \*\*step-02 — Second\*\* ✅ shipped 2026-06-27 \[tracker: BDS-60\] — current\.$' "$roadmap" \
  "A: the step bullet is still marked shipped (retirement did not disturb the marker writer)"

# ---------------------------------------------------------------------------
# MUTATION PIN (match by tracker, both sources) — the entry NOBODY named is
# byte-identical. A stub that retires "the first backlog entry" or "every entry"
# once a step ships disturbs BDS-61; a correct match-by-tracker implementation
# cannot.
# ---------------------------------------------------------------------------
assert_grep "issue/BDS-61" "$repo_backlog" \
  "MUTATION PIN: BDS-61's repo entry survives (ship retires by TRACKER, not by position)"
assert_grep "Repo backlog item nobody named" "$repo_backlog" \
  "MUTATION PIN: BDS-61's repo entry title survives"
assert_grep '\[tracker: BDS-61\]' "$roadmap" \
  "MUTATION PIN: BDS-61's roadmap backlog bullet survives"
assert_eq "0" "$(grep -c 'BDS-61' <<<"$(grep '^- _(pruned' "$repo_backlog" || true)" || true)" \
  "MUTATION PIN: no pruned marker was written for the tracker ship never named"

# ---------------------------------------------------------------------------
# Case B — MUTATION PIN: a step with NO tracker retires NOTHING.
#
# This is the pin for "retires unconditionally regardless of tracker match". The
# fixture is identical except step-02 carries no `[tracker: …]` marker, so there
# is nothing to match on. An implementation that calls the front-ends anyway (with
# an empty tracker, or that prunes whatever it finds because a step shipped)
# mutates one or both backlogs — here BOTH files are asserted BYTE-IDENTICAL, and
# both statuses must be `skipped` (the seam never ran), not `noop` (it ran and
# found nothing).
# ---------------------------------------------------------------------------
setup ""
before_repo="$(wip_mktemp)/repo-before.md"
cp "$repo_backlog" "$before_repo"
before_roadmap_backlog="$(wip_mktemp)/roadmap-backlog-before.txt"
roadmap_backlog_section >"$before_roadmap_backlog"

out="$(run demo step-02)"
assert_eq "null" "$(jq -r '.tracker // "null"' <<<"$out")" "B: (sanity) ledger carries no tracker field"
assert_eq "skipped" "$(jq -r '.backlog_retired.repo' <<<"$out")" \
  "MUTATION PIN: a step with NO tracker reports repo skipped (the seam never ran)"
assert_eq "skipped" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" \
  "MUTATION PIN: a step with NO tracker reports roadmap skipped"
assert_cmp "$before_repo" "$repo_backlog" \
  "MUTATION PIN: the repo backlog is BYTE-IDENTICAL when the shipped step names no tracker"
after_roadmap_backlog="$(wip_mktemp)/roadmap-backlog-after.txt"
roadmap_backlog_section >"$after_roadmap_backlog"
assert_cmp "$before_roadmap_backlog" "$after_roadmap_backlog" \
  "MUTATION PIN: the roadmap's ## Backlog section is BYTE-IDENTICAL when the step names no tracker"
# The rest of ship still works — the no-tracker path is not an error path.
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "B: the step still ships normally"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "B: changed still true from the marker seam"

# ---------------------------------------------------------------------------
# Case C — a tracker that matches NOTHING in either backlog is a quiet `noop`,
#   never an error and never a write. This is the COMMON case in real repos: most
#   shipped steps have no backlog item at all.
# ---------------------------------------------------------------------------
setup "BDS-999"
before_repo="$(wip_mktemp)/c-repo-before.md"
cp "$repo_backlog" "$before_repo"
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.backlog_retired.repo' <<<"$out")" \
  "C: an unmatched tracker reports repo noop (not an error)"
assert_eq "noop" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" \
  "C: an unmatched tracker reports roadmap noop"
assert_cmp "$before_repo" "$repo_backlog" "C: a noop never writes to the repo backlog"

# ---------------------------------------------------------------------------
# Case D — `changed` folds the retirement seams in exactly as `active_step_cleared`
#   does: on their own affirmative word, and on nothing else. Re-shipping an
#   already-shipped step whose backlog entries are already retired is a full
#   double-noop → `changed: false`, even though a retirement was "considered".
# ---------------------------------------------------------------------------
setup
run demo step-02 >/dev/null # run 1 — mutates everything
snap_repo="$(wip_mktemp)/d-repo.md"
cp "$repo_backlog" "$snap_repo"
snap_roadmap="$(wip_mktemp)/d-roadmap.md"
cp "$roadmap" "$snap_roadmap"

out2="$(run demo step-02)" # run 2 — steady state
assert_eq "noop" "$(jq -r '.backlog_retired.repo' <<<"$out2")" \
  "D: re-shipping reports repo noop (idempotent — the entry is already retired)"
assert_eq "noop" "$(jq -r '.backlog_retired.roadmap' <<<"$out2")" \
  "D: re-shipping reports roadmap noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" \
  "D: an all-noop re-run is changed:false — retirement folds on the retired word ONLY"
assert_cmp "$snap_repo" "$repo_backlog" "D: the repo backlog is byte-identical across the re-run"
assert_cmp "$snap_roadmap" "$roadmap" "D: the roadmap is byte-identical across the re-run"
out3="$(run demo step-02)"
assert_eq "$out2" "$out3" "D: the steady-state ledger is stable (run2 == run3)"

# ---------------------------------------------------------------------------
# Case D2 — MUTATION PIN: `changed` actually FOLDS the retirement seams.
#
# Every other case here has the marker seam reporting `updated`, which forces
# `changed: true` on its own — so an implementation that computes
# `backlog_retired` correctly and then FORGETS to OR it into `changed` passes all
# of them. (Built and run: it does. That is why this case exists.) The only
# fixture that can see the difference is one where the retirement is the ONLY
# affirmative seam: step-02 already marked shipped (`marked_shipped: noop`) and
# `active_step` already unset (`active_step_cleared: noop`), but the backlog
# entries still present. `changed` must be TRUE here, and it can only be true
# because a retirement fired.
# ---------------------------------------------------------------------------
setup BDS-60 1
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out")" "D2: (fixture) marker seam is a noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out")" "D2: (fixture) active_step seam is a noop"
assert_eq "skipped" "$(jq -r '.round_marked_shipped' <<<"$out")" "D2: (fixture) round seam never fires"
assert_eq "retired" "$(jq -r '.backlog_retired.repo' <<<"$out")" "D2: the repo retirement is the only work done"
assert_eq "retired" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" "D2: the roadmap retirement fires too"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" \
  "MUTATION PIN: changed is TRUE with retirement as the ONLY affirmative seam (kills an unfolded backlog_retired)"

# ---------------------------------------------------------------------------
# Case E — `--dry-run`. Both statuses are still computed and reported as the
#   `retired` they WOULD have written, and NEITHER backlog is touched.
# ---------------------------------------------------------------------------
setup
before_repo="$(wip_mktemp)/e-repo-before.md"
cp "$repo_backlog" "$before_repo"
before_roadmap="$(wip_mktemp)/e-roadmap-before.md"
cp "$roadmap" "$before_roadmap"

out="$(run demo step-02 --dry-run)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "E: dry_run true"
assert_eq "retired" "$(jq -r '.backlog_retired.repo' <<<"$out")" \
  "E: --dry-run reports the repo status it WOULD have written"
assert_eq "retired" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" \
  "E: --dry-run reports the roadmap status it WOULD have written"
assert_cmp "$before_repo" "$repo_backlog" "E: the repo backlog is unwritten under --dry-run"
assert_cmp "$before_roadmap" "$roadmap" "E: the roadmap is unwritten under --dry-run"

# NB: `--dry-run` is the only inherited path here. An *inherited* `WIP_DRY_RUN=1`
# in the caller's environment is deliberately NOT honored through the binary —
# `bin/wip-plumbing:48` hard-resets it to 0 before dispatch, so the env var is an
# INTERNAL seam contract (ship sets and exports it from the flag; the retirement
# front-ends read it), not a user-facing one. Asserting otherwise would pin a
# behavior the CLI has never had. The seam-level $WIP_DRY_RUN contract is owned by
# test-backlog-retire.sh, which drives the front-ends directly.

# ---------------------------------------------------------------------------
# Case F — a repo with NO `.wip/backlog.md` at all. The repo seam is guarded on
#   the file existing, so it reports `noop` rather than crashing; the roadmap seam
#   still fires. A repo need not have a repo-level backlog.
# ---------------------------------------------------------------------------
setup
rm -f "$repo_backlog"
out="$(run demo step-02)"
assert_eq "noop" "$(jq -r '.backlog_retired.repo' <<<"$out")" \
  "F: a missing .wip/backlog.md is a repo noop, not a crash"
assert_eq "retired" "$(jq -r '.backlog_retired.roadmap' <<<"$out")" \
  "F: the roadmap seam still fires when the repo backlog is absent"
assert_absent "$repo_backlog" "F: no .wip/backlog.md is conjured into existence"

test_summary
