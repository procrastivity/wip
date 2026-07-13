#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-closeout-round"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Round-level closeout drift (kind:"closeout-round"): a round whose every step is
# shipped but whose `## Round N` heading carries no `✅ shipped` marker — the
# round marker `ship` now writes when the round's last step lands. Every step
# shipped ∧ heading unmarked → "round-not-marked-shipped" (+ fix hint).
# A round with zero steps, a round still mid-flight, and a marked round all add
# NO entry. Scope mirrors the step-level closeout check (§2b): in-flight/proposed
# initiatives only; `status: shipped`/`archived` are skipped.
#
# Fixtures mirror test-doctor-closeout.sh: an in-flight `demo` initiative driven
# through WIP_ROOT. Shipped steps get an archived workplan so the step-level
# closeout check (§2b, "shipped-not-archived") stays quiet and the only drift in
# play is the round-level one under test.

# mkfix <tmp> — in-flight `demo` initiative: manifest + initiative dir + archive/.
mkfix() {
  local tmp="$1"
  wip_fixture_init "$tmp" --no-active-step
  mkdir -p "$tmp/.wip/initiatives/demo/archive"
}

# archive <tmp> <step-id> — mark a step archived (satisfies the §2b step check).
archive() {
  : >"$1/.wip/initiatives/demo/archive/$2-workplan.md"
}

# run_doctor <tmp> — run doctor under WIP_ROOT; set globals OUT (json) and RC.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" bin/wip-plumbing doctor)"
  RC=$?
  set -e
}

# n_round <selector?> — count closeout-round entries in OUT (optional jq filter).
n_round() {
  local sel="${1:-true}"
  jq "[.checks[] | select(.kind==\"closeout-round\") | select($sel)] | length" <<<"$OUT"
}

# ── Case A: every step shipped, heading unmarked → one entry + exit 4 ────────
# Regression pin #5 (first half): doctor flags an all-shipped/unmarked round.
# Round 2 is open on purpose: it keeps the INITIATIVE un-closeable, so the
# initiative-level check (§2l, initiative-shipped-not-closed) stays quiet and the
# round marker remains the only drift these assertions have to account for.
tmpA="$(wip_mktemp)"
mkfix "$tmpA"
cat >"$tmpA/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** ✅ shipped 2026-05-02 — done.

## Round 2 — Two

- **step-03 — Third** — current.
MD
archive "$tmpA" step-01
archive "$tmpA" step-02
run_doctor "$tmpA"
assert_eq "4" "$RC" "all-shipped/unmarked: exit 4"
assert_eq "1" "$(n_round)" "all-shipped/unmarked: one closeout-round entry"
assert_eq "1" "$(jq -r '.drift_count' <<<"$OUT")" \
  "all-shipped/unmarked: the round marker is the ONLY drift (step-level check quiet)"
assert_eq "round-not-marked-shipped" \
  "$(jq -r '.checks[]|select(.kind=="closeout-round").status' <<<"$OUT")" \
  "all-shipped/unmarked: status"
assert_eq "demo" \
  "$(jq -r '.checks[]|select(.kind=="closeout-round").slug' <<<"$OUT")" \
  "all-shipped/unmarked: offending slug"
assert_eq "1" \
  "$(jq -r '.checks[]|select(.kind=="closeout-round").round' <<<"$OUT")" \
  "all-shipped/unmarked: offending round"
assert_eq "number" \
  "$(jq -r '.checks[]|select(.kind=="closeout-round").round|type' <<<"$OUT")" \
  "all-shipped/unmarked: round is a number, not a string"
assert_eq "run wip ship demo step-02 (or re-run it) to write the round marker" \
  "$(jq -r '.checks[]|select(.kind=="closeout-round").fix' <<<"$OUT")" \
  "all-shipped/unmarked: fix hint names the round's last step"

# ── Case B: the transition — the same round, marker now landed → quiet ───────
# Regression pin #5 (second half): doctor STOPS flagging once the marker lands.
# Deliberately mutates Case A's fixture in place rather than building a second
# one: the pin is about the transition, not two unrelated static states.
cat >"$tmpA/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-02

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** ✅ shipped 2026-05-02 — done.

## Round 2 — Two

- **step-03 — Third** — current.
MD
run_doctor "$tmpA"
assert_eq "0" "$RC" "marker landed: exit 0"
assert_eq "0" "$(n_round)" "marker landed: the closeout-round entry disappears"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "marker landed: zero drift"

# ── Case C: mid-flight round (mixed shipped/unshipped), unmarked → quiet ─────
# The ordinary in-progress case: not every step is shipped, so an unmarked
# heading is correct, not drift.
tmpC="$(wip_mktemp)"
mkfix "$tmpC"
cat >"$tmpC/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
MD
archive "$tmpC" step-01
run_doctor "$tmpC"
assert_eq "0" "$RC" "mid-flight round: exit 0"
assert_eq "0" "$(n_round)" "mid-flight round: no closeout-round entry"

# ── Case D: scope — status:shipped/archived initiatives are skipped ──────────
# Same guard as the step-level closeout check: a legacy closed-out initiative
# with an unmarked all-shipped round must NOT trip drift; an in-flight one does.
tmpD="$(wip_mktemp)"
mkdir -p "$tmpD/.wip/initiatives/demo/archive" \
  "$tmpD/.wip/initiatives/legacy/archive" \
  "$tmpD/.wip/initiatives/attic/archive"
cat >"$tmpD/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  wip: { enabled: true, root: .wip }
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
  - slug: legacy
    status: shipped
    roadmap: .wip/initiatives/legacy/roadmap.md
  - slug: attic
    status: archived
    roadmap: .wip/initiatives/attic/roadmap.md
YAML
for s in demo legacy attic; do
  cat >"$tmpD/.wip/initiatives/$s/roadmap.md" <<'MD'
# Roadmap

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
MD
  : >"$tmpD/.wip/initiatives/$s/archive/step-01-workplan.md"
done
run_doctor "$tmpD"
assert_eq "4" "$RC" "scope: in-flight initiative still trips round drift"
assert_eq "1" "$(n_round)" "scope: exactly one closeout-round entry (demo only)"
assert_eq "1" "$(n_round '.slug=="demo"')" "scope: demo (in-flight) is checked"
assert_eq "0" "$(n_round '.slug=="legacy"')" "scope: legacy (shipped) is skipped"
assert_eq "0" "$(n_round '.slug=="attic"')" "scope: attic (archived) is skipped"

# ── Case E: a round with zero steps never false-positives ────────────────────
# `all(.shipped)` over an EMPTY array is vacuously true in jq, so without the
# `length > 0` guard an empty/placeholder round would be flagged as "fully
# shipped but unmarked". Round 1 is closed out cleanly; Round 2 is an empty
# placeholder — total drift must be zero. Round 3 carries an open step so the
# INITIATIVE is not closeable either, keeping §2l (initiative-shipped-not-closed)
# out of the drift count; the empty round 2 sitting between them is what this case
# actually pins.
tmpE="$(wip_mktemp)"
mkfix "$tmpE"
cat >"$tmpE/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two

## Round 3 — Three

- **step-02 — Second** — current.
MD
archive "$tmpE" step-01
run_doctor "$tmpE"
assert_eq "0" "$RC" "empty round: exit 0"
assert_eq "0" "$(n_round)" "empty round: no closeout-round entry"
assert_eq "0" "$(n_round '.round==2')" "empty round: round 2 specifically is not flagged"

# ── Case F: lane-bearing round — lane steps are first-class in .steps[] ──────
# Resolves the workplan's open question: a round whose steps all live under
# `### Lane` headings still parses them into `.rounds[].steps[]`, so the
# all-shipped predicate sees them and the check behaves exactly as for a
# main-lane round — flagged when every lane step is shipped...
tmpF="$(wip_mktemp)"
mkfix "$tmpF"
cat >"$tmpF/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

### Lane A

- **step-01 — First** ✅ shipped 2026-05-01 — done.

### Lane B

- **step-02 — Second** ✅ shipped 2026-05-02 — done.
MD
archive "$tmpF" step-01
archive "$tmpF" step-02
run_doctor "$tmpF"
assert_eq "4" "$RC" "lane round, all lane steps shipped: exit 4"
assert_eq "1" "$(n_round '.round==1')" "lane round, all lane steps shipped: flagged"

# ...and quiet while any lane step is still unshipped.
cat >"$tmpF/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

### Lane A

- **step-01 — First** ✅ shipped 2026-05-01 — done.

### Lane B

- **step-02 — Second** — current.
MD
rm -f "$tmpF/.wip/initiatives/demo/archive/step-02-workplan.md"
run_doctor "$tmpF"
assert_eq "0" "$RC" "lane round, one lane step open: exit 0"
assert_eq "0" "$(n_round)" "lane round, one lane step open: no closeout-round entry"

test_summary
