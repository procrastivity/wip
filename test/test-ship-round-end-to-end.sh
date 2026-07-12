#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="ship-round-end-to-end"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the ROUND-LEVEL closeout trigger composed into `ship` and driven
# END-TO-END through `bin/wip-plumbing ship` — proving the round marker fires
# exactly when the shipped step was its round's LAST unshipped step, and is a
# byte-identical no-op every other time. Marker-placement mechanics (tracker-key
# preservation, date correction, the writer's own comment-shadow return) are
# owned by test-ship-round-writer.sh; the step-level lane and the `changed` OR
# table are owned by test-ship-end-to-end.sh — NOT re-tested here.
#
# Regression pins (step-03 workplan, Test strategy):
#   1. last-unshipped-step-in-round ships the round marker            → Case A
#   2. non-last-step ship leaves the round heading untouched          → Case B
#   3. re-run on a round-complete round is a byte-identical no-op,
#      proven by file diff, not merely a `noop` status word           → Case C
#   4. a round heading inside an HTML comment span is never mistaken
#      for the real one                                               → Cases G, H
#   6. shipping THIS initiative's own step-03 (Round 1, step-04 still
#      open) does not fire the round marker                           → Case E
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-27

# setup [active_step] — fresh tmp root + a `demo` manifest. The roadmap itself is
# written per-case by the caller (every case here turns on a differently shaped
# round, so a single shared roadmap fixture would obscure more than it saves).
setup() {
  local active="${1-}"
  tmp="$(wip_mktemp)"
  mkdir -p "$tmp/.wip/initiatives/demo"
  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    printf 'current_initiative: demo\n'
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    [[ -n "$active" ]] && printf '    active_step: %s\n' "$active"
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
  } >"$tmp/.wip.yaml"
  roadmap="$tmp/.wip/initiatives/demo/roadmap.md"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing ship "$@"; }

# snap <file> — copy the whole roadmap (for the total-no-write cases).
snap() { cp "$roadmap" "$1"; }

# snap_headings <file> — dump every `## Round` line, commented ones included.
# This is what makes "the round heading is byte-identical" provable in the cases
# where the step BULLET is legitimately rewritten by the step-level lane: a
# whole-file cmp would fail for the wrong reason, so we diff the headings alone.
snap_headings() { grep -F '## Round' "$roadmap" >"$1" || true; }

# ---------------------------------------------------------------------------
# Case A — pin 1. Shipping the LAST unshipped step in a round writes the round
#   marker in the SAME invocation, alongside the step-level marker and the
#   `active_step` clear.
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
EOF
out="$(run demo step-02)"
assert_eq "1" "$(jq -r '.round' <<<"$out")" "A: round number reported"
assert_eq "updated" "$(jq -r '.round_marked_shipped' <<<"$out")" "A: round_marked_shipped updated"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "A: step marked_shipped updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"
assert_eq '## Round 1 — Build ✅ shipped 2026-06-27' \
  "$(grep -F '## Round 1' "$roadmap")" "A: round heading marked shipped"

# ---------------------------------------------------------------------------
# Case B — pin 2. Shipping a NON-last unshipped step does not touch the round
#   heading: `skipped`, and the heading is byte-identical before/after (the step
#   bullet itself is legitimately rewritten, hence the heading-only diff).
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
- **step-03 — Rotation** — later.
EOF
before_h="$(mktemp)"
snap_headings "$before_h"
out="$(run demo step-02)"
assert_eq "1" "$(jq -r '.round' <<<"$out")" "B: round number reported"
assert_eq "skipped" "$(jq -r '.round_marked_shipped' <<<"$out")" "B: round_marked_shipped skipped"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "B: step still marked"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "B: changed true (step lane alone)"
after_h="$(mktemp)"
snap_headings "$after_h"
assert_cmp "$before_h" "$after_h" "B: round heading byte-identical (no round write)"
assert_not_grep "Build ✅" "$roadmap" "B: no marker on the round heading"

# ---------------------------------------------------------------------------
# Case C — pin 3. Re-running `ship` on an already-round-complete round is a full
#   no-op at EVERY layer. Proven as a byte-identical whole-file diff, not merely
#   a `noop` status word — a writer that reports `noop` but still rewrites the
#   line (trailing whitespace, re-spelled marker) would pass the word check and
#   fail this one. The ledger's run-vs-run stability is checked in addition.
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
EOF
run demo step-02 >/dev/null # run 1 — completes the round, writes both markers
snap_c="$(mktemp)"
snap "$snap_c"
out2="$(run demo step-02)" # run 2 — steady state
assert_eq "noop" "$(jq -r '.marked_shipped' <<<"$out2")" "C: run2 marked_shipped noop"
assert_eq "noop" "$(jq -r '.round_marked_shipped' <<<"$out2")" "C: run2 round_marked_shipped noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out2")" "C: run2 active_step_cleared noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "C: run2 changed false"
assert_cmp "$snap_c" "$roadmap" "C: roadmap byte-identical across re-run"
out3="$(run demo step-02)" # run 3 — steady state again
assert_eq "$out2" "$out3" "C: steady-state ledger stable (run2 == run3)"
assert_cmp "$snap_c" "$roadmap" "C: roadmap still byte-identical after run 3"

# ---------------------------------------------------------------------------
# Case D — `--dry-run` on a round-completing ship: the round status is still
#   COMPUTED and reported (`updated`), but nothing is written. Note the trigger
#   must survive dry-run: the step-level marker is deliberately NOT on disk, so
#   a re-parse alone would see step-02 unshipped and wrongly report `skipped`.
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
EOF
before_d="$(mktemp)"
snap "$before_d"
out="$(run demo step-02 --dry-run)"
assert_eq "updated" "$(jq -r '.round_marked_shipped' <<<"$out")" "D: dry-run round_marked_shipped updated"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "D: dry-run marked_shipped updated"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D: dry-run changed true"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "D: dry_run flag true"
assert_cmp "$before_d" "$roadmap" "D: roadmap unwritten under --dry-run"

# ---------------------------------------------------------------------------
# Case E — pin 6, the LIVE regression case. This fixture is shaped like
#   `closeout-write-ladder`'s real Round 1 at the moment step-03 ships: steps
#   01–02 already shipped, step-03 the target, step-04 STILL OPEN. Shipping
#   step-03 must NOT fire the round marker — this is the exact misfire the round
#   trigger has to not commit on this initiative's own roadmap.
# ---------------------------------------------------------------------------
setup step-03
cat >"$roadmap" <<'EOF'
# Roadmap — closeout-write-ladder

## Round 1 — Trustworthy roadmap grammar, then the missing closeout writers

- **step-01 — roadmap grammar round-trips what the write path accepts** ✅ shipped 2026-07-11 — done. [tracker: BDS-91]
- **step-02 — writers never target a comment span, and never fail without an envelope** ✅ shipped 2026-07-11 — done. [tracker: BDS-92]
- **step-03 — round-level closeout writer (the round shipped marker)** — current. [tracker: BDS-93]
- **step-04 — initiative-level closeout verb (`closeout <slug>`) + doctor drift checks** — later. [tracker: BDS-94]

## Round 2 — Declared state the tooling never enforces

- **step-05 — enforce the `always_commit` gitignore policy** — later.
EOF
before_e="$(mktemp)"
snap_headings "$before_e"
out="$(run demo step-03)"
assert_eq "1" "$(jq -r '.round' <<<"$out")" "E: live shape — round 1 reported"
assert_eq "skipped" "$(jq -r '.round_marked_shipped' <<<"$out")" "E: live shape — round marker NOT fired (step-04 still open)"
assert_eq "updated" "$(jq -r '.marked_shipped' <<<"$out")" "E: live shape — step-03 itself marked"
after_e="$(mktemp)"
snap_headings "$after_e"
assert_cmp "$before_e" "$after_e" "E: live shape — '## Round 1' heading byte-identical"

# ---------------------------------------------------------------------------
# Case F — round isolation. Shipping the last step of Round 2 marks ROUND 2 and
#   reports `round: 2` — Round 1 (all-shipped but unmarked, i.e. pre-existing
#   drift that is doctor's business, not ship's) is left strictly alone. ship
#   only ever writes the round of the step it was handed.
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two

- **step-02 — Second** — current.
EOF
out="$(run demo step-02)"
assert_eq "2" "$(jq -r '.round' <<<"$out")" "F: round 2 reported"
assert_eq "updated" "$(jq -r '.round_marked_shipped' <<<"$out")" "F: round 2 marked"
assert_eq '## Round 2 — Two ✅ shipped 2026-06-27' \
  "$(grep -F '## Round 2' "$roadmap")" "F: round 2 heading marked"
assert_eq '## Round 1 — One' \
  "$(grep -F '## Round 1' "$roadmap")" "F: round 1 heading untouched (not ship's round)"

# ---------------------------------------------------------------------------
# Case G — pin 4. A COMMENTED `## Round 1` example (init scaffold / hand-authored
#   sample) sitting above the real one is never mistaken for the write anchor:
#   the marker lands on the REAL heading and the comment span is byte-identical.
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

<!--
## Round 1 — Commented example round
- **step-99 — Example** — scaffold, not real.
-->

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
EOF
out="$(run demo step-02)"
assert_eq "updated" "$(jq -r '.round_marked_shipped' <<<"$out")" "G: real round marked"
assert_eq '## Round 1 — Commented example round' \
  "$(grep -F 'Commented example' "$roadmap")" "G: commented heading byte-identical (never the anchor)"
assert_eq '## Round 1 — Build ✅ shipped 2026-06-27' \
  "$(grep -F '## Round 1 — Build' "$roadmap")" "G: marker landed on the REAL heading"

# ---------------------------------------------------------------------------
# Case H — pin 4, the fully-shadowed round. When the ONLY `## Round 1` heading
#   lives inside a comment span, the parser takes its steps down with it (they
#   parse into no round at all), so `ship` REFUSES with an error envelope rather
#   than reporting a false success — and writes nothing. This is the reachable
#   end-to-end face of the writer's `return 2`: ship cannot reach a state where a
#   round is in the parse but its heading is only in a comment, because reader
#   and writer share one comment state machine (see the note in ship.bash).
# ---------------------------------------------------------------------------
setup step-02
cat >"$roadmap" <<'EOF'
# Roadmap

<!--
## Round 1 — Shadowed
- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
-->
EOF
before_h2="$(mktemp)"
snap "$before_h2"
set +e
out_shadow="$(run demo step-02 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "H: shadowed round — exit 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out_shadow")" "H: shadowed round — error envelope, not a false success"
assert_eq "null" "$(jq -r '.round_marked_shipped // null' <<<"$out_shadow")" "H: shadowed round — no round ledger emitted"
assert_cmp "$before_h2" "$roadmap" "H: shadowed round — roadmap byte-identical (no write)"

test_summary
