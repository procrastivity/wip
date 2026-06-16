#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="roadmap-parse"
# shellcheck source=test/helpers.sh
source test/helpers.sh
# shellcheck source=lib/wip/wip-plumbing-roadmap-lib.bash
source lib/wip/wip-plumbing-roadmap-lib.bash

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/roadmap.md" <<'MD'
# Roadmap — fixture

## Round 1 — Bootstrap  ✅ shipped 2026-05-01

- **step-01 — Skeleton** ✅ shipped 2026-04-15 — repo bootstrap.
- **step-02 — Plumb the seams** ✅ — small follow-up.

## Round 2 — Build out

- **step-03 — `init`** ✅ shipped 2026-06-01 — scaffold.
- **step-03.5 — patch step** — slotted between.
- **step-04 — `next`** — the headline value.

## Deferred (decided-not-now)

- Things we are not doing.

## Backlog

- **In-place fixes** — patch the stub.
- **Random idea** (scratchpad item): explore.
MD

doc="$(wip_roadmap_parse "$tmp/roadmap.md")"

assert_eq "2" "$(jq -r '.rounds | length' <<<"$doc")" "2 rounds"
assert_eq "1" "$(jq -r '.rounds[0].n' <<<"$doc")" "round 1 number"
assert_eq "Bootstrap" "$(jq -r '.rounds[0].title' <<<"$doc")" "round 1 title"
assert_eq "true" "$(jq -r '.rounds[0].shipped' <<<"$doc")" "round 1 shipped"
assert_eq "2026-05-01" "$(jq -r '.rounds[0].shipped_date' <<<"$doc")" "round 1 shipped_date"

assert_eq "Build out" "$(jq -r '.rounds[1].title' <<<"$doc")" "round 2 title"
assert_eq "false" "$(jq -r '.rounds[1].shipped' <<<"$doc")" "round 2 not shipped"
assert_eq "null" "$(jq -r '.rounds[1].shipped_date' <<<"$doc")" "round 2 no shipped date"

assert_eq "2" "$(jq -r '.rounds[0].steps | length' <<<"$doc")" "round 1 has 2 steps"
assert_eq "step-01" "$(jq -r '.rounds[0].steps[0].id' <<<"$doc")" "step-01 id"
assert_eq "Skeleton" "$(jq -r '.rounds[0].steps[0].title' <<<"$doc")" "step-01 title"
assert_eq "true" "$(jq -r '.rounds[0].steps[0].shipped' <<<"$doc")" "step-01 shipped"
assert_eq "2026-04-15" "$(jq -r '.rounds[0].steps[0].shipped_date' <<<"$doc")" "step-01 date"
assert_eq "true" "$(jq -r '.rounds[0].steps[1].shipped' <<<"$doc")" "step-02 shipped (no date)"
assert_eq "null" "$(jq -r '.rounds[0].steps[1].shipped_date' <<<"$doc")" "step-02 no date"

assert_eq "step-03" "$(jq -r '.rounds[1].steps[0].id' <<<"$doc")" "step-03 id"
assert_eq "step-03.5" "$(jq -r '.rounds[1].steps[1].id' <<<"$doc")" "step-03.5 id (decimal)"
assert_eq "false" "$(jq -r '.rounds[1].steps[1].shipped' <<<"$doc")" "step-03.5 not shipped"
assert_eq "step-04" "$(jq -r '.rounds[1].steps[2].id' <<<"$doc")" "step-04 id"

assert_eq "2" "$(jq -r '.backlog | length' <<<"$doc")" "2 backlog entries"
assert_eq "in-place-fixes" "$(jq -r '.backlog[0].id' <<<"$doc")" "backlog id slugified"
assert_eq "In-place fixes" "$(jq -r '.backlog[0].title' <<<"$doc")" "backlog title"

# Deferred section entries do not bleed into backlog.
assert_eq "0" "$(jq '.backlog | map(select(.title | contains("not doing"))) | length' <<<"$doc")" "deferred excluded"
assert_eq "Random idea" "$(jq -r '.backlog[1].title' <<<"$doc")" "second backlog title"

# Helper checks.
round="$(wip_roadmap_active_round "$doc" "step-03.5")"
assert_eq "2" "$(jq -r '.n' <<<"$round")" "active_round for step-03.5"

first="$(wip_roadmap_first_unshipped "$doc")"
assert_eq "step-03.5" "$(jq -r '.id' <<<"$first")" "first_unshipped is step-03.5"

after="$(wip_roadmap_unshipped_after "$doc" "step-03.5")"
assert_eq "1" "$(jq -r 'length' <<<"$after")" "unshipped after step-03.5: just step-04"
assert_eq "step-04" "$(jq -r '.[0].id' <<<"$after")" "next after step-03.5"

# unshipped_after with empty step -> ALL unshipped in order.
all="$(wip_roadmap_unshipped_after "$doc" "")"
assert_eq "2" "$(jq -r 'length' <<<"$all")" "all unshipped: 2"
assert_eq "step-03.5" "$(jq -r '.[0].id' <<<"$all")" "all unshipped first"

# step lookup.
s="$(wip_roadmap_step "$doc" "step-01")"
assert_eq "Skeleton" "$(jq -r '.title' <<<"$s")" "step lookup title"

# Missing file -> empty doc (same shape as a parsed doc, incl. lane_errors).
empty="$(wip_roadmap_parse "$tmp/missing.md")"
assert_eq "0" "$(jq -r '.rounds | length' <<<"$empty")" "missing file rounds=0"
assert_eq "0" "$(jq -r '.backlog | length' <<<"$empty")" "missing file backlog=0"
assert_eq "array" "$(jq -r '.lane_errors | type' <<<"$empty")" "missing file lane_errors is array"
assert_eq "0" "$(jq -r '.lane_errors | length' <<<"$empty")" "missing file lane_errors=[]"

# Linear roadmap: every step lane is null, lane_errors empty (ADR-0010 regression).
assert_eq "true" "$(jq -c '[.rounds[].steps[].lane] | all(. == null)' <<<"$doc")" "linear: all lanes null"
assert_eq "0" "$(jq -r '.lane_errors | length' <<<"$doc")" "linear: no lane errors"
assert_eq "0" "$(jq -r '.rounds[0].lanes | length' <<<"$doc")" "linear: round lanes empty"

# ---- Lanes (ADR-0010) ----
cat >"$tmp/lanes.md" <<'MD'
# Roadmap — lanes fixture

## Round 4 — Track expansion

A prereq landing first, then two parallel tracks, then a sync step.

- **step-12 — F1: taxonomy** — main-lane prereq.

### Lane A
- **step-13 — Track A part 1** — typed entity spine,
  continued on a second line.
- **step-15 — Track A part 2** — external provider.

### Lane D
- **step-14 — Track D** — SPA usability.

- **step-16 — Sync** — post-lane main-lane sync step.

## Backlog

- **Cleanup** — later.
MD
ld="$(wip_roadmap_parse "$tmp/lanes.md")"

assert_eq "0" "$(jq -r '.lane_errors | length' <<<"$ld")" "lanes: well-formed, no errors"
# Round records both declared lanes in order.
assert_eq '["A","D"]' "$(jq -c '.rounds[0].lanes' <<<"$ld")" "lanes: round.lanes = [A,D]"
# Per-step lane assignment.
assert_eq "null" "$(jq -r '.rounds[0].steps[0].lane' <<<"$ld")" "step-12 lane null (pre-lane main)"
assert_eq "A" "$(jq -r '.rounds[0].steps[1].lane' <<<"$ld")" "step-13 lane A"
assert_eq "A" "$(jq -r '.rounds[0].steps[2].lane' <<<"$ld")" "step-15 lane A (contiguous)"
assert_eq "D" "$(jq -r '.rounds[0].steps[3].lane' <<<"$ld")" "step-14 lane D"
assert_eq "null" "$(jq -r '.rounds[0].steps[4].lane' <<<"$ld")" "step-16 lane null (post-lane sync)"
# Step ids stay globally sequential, not per-lane.
assert_eq "5" "$(jq -r '.rounds[0].steps | length' <<<"$ld")" "5 steps in the round"

# lanes_in_round helper (includes an empty lane).
cat >"$tmp/empty-lane.md" <<'MD'
# R
## Round 2 — X
- **step-01 — a** — body.
### Lane A
- **step-02 — b** — body.
### Lane B
MD
el="$(wip_roadmap_parse "$tmp/empty-lane.md")"
assert_eq '["A","B"]' "$(wip_roadmap_lanes_in_round "$el" 2)" "lanes_in_round incl empty lane B"
assert_eq '[]' "$(wip_roadmap_lanes_in_round "$el" 99)" "lanes_in_round unknown round -> []"

# active_step record carries its lane.
sa="$(wip_roadmap_step "$ld" "step-13")"
assert_eq "A" "$(jq -r '.lane' <<<"$sa")" "wip_roadmap_step surfaces lane"

# ---- Malformed cases (ADR-0010 §5) ----
# nested lane (#### Lane).
cat >"$tmp/nested.md" <<'MD'
# R
## Round 1 — X
### Lane A
- **step-01 — a** — body.
#### Lane B
- **step-02 — b** — body.
MD
nx="$(wip_roadmap_parse "$tmp/nested.md")"
assert_eq "1" "$(jq '[.lane_errors[] | select(.kind == "nested-lane")] | length' <<<"$nx")" "nested-lane rejected"

# duplicate lane name in one round.
cat >"$tmp/dup.md" <<'MD'
# R
## Round 1 — X
### Lane A
- **step-01 — a** — body.
### Lane A
- **step-02 — b** — body.
MD
dx="$(wip_roadmap_parse "$tmp/dup.md")"
assert_eq "1" "$(jq '[.lane_errors[] | select(.kind == "duplicate-lane")] | length' <<<"$dx")" "duplicate-lane rejected"

# lane heading outside a round (under Backlog).
cat >"$tmp/outside.md" <<'MD'
# R
## Round 1 — X
- **step-01 — a** — body.
## Backlog
### Lane A
- **thing** — x.
MD
ox="$(wip_roadmap_parse "$tmp/outside.md")"
assert_eq "1" "$(jq '[.lane_errors[] | select(.kind == "lane-outside-round")] | length' <<<"$ox")" "lane-outside-round rejected"

# bare bullet sandwiched between two lanes.
cat >"$tmp/sandwich.md" <<'MD'
# R
## Round 1 — X
### Lane A
- **step-02 — a** — body.

- **step-03 — orphan** — body.
### Lane D
- **step-04 — d** — body.
MD
sx="$(wip_roadmap_parse "$tmp/sandwich.md")"
assert_eq "1" "$(jq '[.lane_errors[] | select(.kind == "main-step-between-lanes")] | length' <<<"$sx")" "main-step-between-lanes rejected"
assert_eq "step-03" "$(jq -r '[.lane_errors[] | select(.kind == "main-step-between-lanes")][0].step' <<<"$sx")" "sandwich names the offending step"

# --- Regression: bold (**…**) in a step body must not swallow the title -------
# The step-bullet title is matched non-greedily ([^*]+), so a **bold** run in the
# body can't capture the whole bullet. This also keeps the documented ship-marker
# position (right after the title's closing **) working when the body has bold.
cat >"$tmp/bold-body.md" <<'MD'
# Roadmap — bold body

## Round 1 — Tracks

- **step-01 — F1: taxonomy** ✅ shipped 2026-06-16 — shared prereq; touches **Track A** and **Track D**.
- **step-02 — Vertical spine** — big work on **core.document**; depends on **step-01**.
MD
bb="$(wip_roadmap_parse "$tmp/bold-body.md")"
assert_eq "F1: taxonomy" "$(jq -r '.rounds[0].steps[0].title' <<<"$bb")" "bold-body: title stops at first ** (not greedy)"
assert_eq "true" "$(jq -r '.rounds[0].steps[0].shipped' <<<"$bb")" "bold-body: ship marker after title ** still detected"
assert_eq "2026-06-16" "$(jq -r '.rounds[0].steps[0].shipped_date' <<<"$bb")" "bold-body: shipped_date parsed"
assert_eq "Vertical spine" "$(jq -r '.rounds[0].steps[1].title' <<<"$bb")" "bold-body: unshipped step title clean despite body bold"
assert_eq "false" "$(jq -r '.rounds[0].steps[1].shipped' <<<"$bb")" "bold-body: unshipped step not shipped"

test_summary
