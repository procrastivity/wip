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

# Missing file -> empty doc.
empty="$(wip_roadmap_parse "$tmp/missing.md")"
assert_eq "0" "$(jq -r '.rounds | length' <<<"$empty")" "missing file rounds=0"
assert_eq "0" "$(jq -r '.backlog | length' <<<"$empty")" "missing file backlog=0"

test_summary
