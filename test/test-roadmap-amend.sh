#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="roadmap-amend"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
make_roadmap() {
  cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — First** ✅ — done.
- **step-02 — Second** — current.

## Deferred

- nothing yet.

## Backlog

- **Cleanup** — later.
MD
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing roadmap amend "$@"; }

# 1. insert-after happy path + idempotent re-apply.
make_roadmap
cat >"$tmp/insert.md" <<'MD'
---
target: demo
insert-after: step-02
---
# Amend

### step-03 — Third

A new step.
MD
out="$(run demo --from "$tmp/insert.md")"
mapfile -t F < <(jq -r '.ok, .idempotent_noop' <<<"$out")
assert_eq "true" "${F[0]}" "insert-after ok"
assert_eq "false" "${F[1]}" "first apply not idempotent"
assert_grep "step-03 — Third" "$tmp/.wip/initiatives/demo/roadmap.md" "step-03 in roadmap"
assert_grep "<!-- wip-amend:" "$tmp/.wip/initiatives/demo/roadmap.md" "marker present"

out2="$(run demo --from "$tmp/insert.md")"
mapfile -t F < <(jq -r '.idempotent_noop, (.wrote | length)' <<<"$out2")
assert_eq "true" "${F[0]}" "second apply idempotent"
assert_eq "0" "${F[1]}" "second apply wrote 0"

# 2. CLI flag matching artifact -> ok.
make_roadmap
out3="$(run demo --from "$tmp/insert.md" --insert-after step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out3")" "matching CLI directive ok"

# 3. CLI flag disagreeing -> exit 2.
set +e
run demo --from "$tmp/insert.md" --insert-after step-01 >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "disagreeing directive exit 2"

# 4. Missing target step -> exit 4.
make_roadmap
cat >"$tmp/bad.md" <<'MD'
---
target: demo
insert-after: step-99
---
# Amend
### step-03 — X
Body.
MD
set +e
run demo --from "$tmp/bad.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "missing target step exit 4"

# 5. Shape failure (missing directive) -> exit 4.
make_roadmap
cat >"$tmp/shape-bad.md" <<'MD'
---
target: demo
---
# No directive
Body.
MD
set +e
out6="$(run demo --from "$tmp/shape-bad.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "shape failure exit 4"
assert_eq "false" "$(jq -r '.valid' <<<"$out6")" "shape failure valid=false"

# 6. replace happy path.
make_roadmap
cat >"$tmp/replace.md" <<'MD'
---
target: demo
replace: step-02
---
# Replace

### step-02 — Second (updated)

Replaced body content.
MD
out7="$(run demo --from "$tmp/replace.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out7")" "replace ok"
assert_grep "Second (updated)" "$tmp/.wip/initiatives/demo/roadmap.md" "replace title applied"

# 7. append-round happy path before ## Deferred.
make_roadmap
cat >"$tmp/round.md" <<'MD'
---
target: demo
append-round: Polish
---
# Append

## Round 2 — Polish

### step-10 — Cleanup

Final touches.
MD
out8="$(run demo --from "$tmp/round.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out8")" "append-round ok"
# The new round must be inserted before "## Deferred".
def_line=$(grep -n "^## Deferred" "$tmp/.wip/initiatives/demo/roadmap.md" | cut -d: -f1)
round_line=$(grep -n "^## Round 2 — Polish" "$tmp/.wip/initiatives/demo/roadmap.md" | cut -d: -f1)
if [[ "$round_line" -lt "$def_line" ]]; then
  _WIP_PASS=$((_WIP_PASS + 1))
  echo "  ok   round inserted before Deferred"
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  echo "  FAIL round position wrong (round=$round_line, def=$def_line)" >&2
fi

# 8. dry-run with insert: no writes.
make_roadmap
out9="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run roadmap amend demo --from "$tmp/insert.md")"
mapfile -t F < <(jq -r '.ok, .dry_run' <<<"$out9")
assert_eq "true" "${F[0]}" "dry-run ok"
assert_eq "true" "${F[1]}" "dry-run flag"
assert_not_grep "step-03 — Third" "$tmp/.wip/initiatives/demo/roadmap.md" "dry-run did not write"

# 9. Unknown initiative -> exit 3.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing roadmap amend bogus --from "$tmp/insert.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"

# 10. Missing --from -> exit 2.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing roadmap amend demo >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing --from exit 2"

# ---- append-lane (ADR-0010) ----
# 11. append-lane happy path (target-round in front-matter) + idempotent re-apply.
make_roadmap
cat >"$tmp/lane.md" <<'MD'
---
target: demo
append-lane: A
target-round: 1
---
# Add Lane A

### step-03 — Track A work

Parallel track A.
MD
out_l="$(run demo --from "$tmp/lane.md")"
mapfile -t F < <(jq -r '.ok, .directive' <<<"$out_l")
assert_eq "true" "${F[0]}" "append-lane ok"
assert_eq "append-lane A (round 1)" "${F[1]}" "append-lane directive label"
assert_grep "### Lane A" "$tmp/.wip/initiatives/demo/roadmap.md" "lane heading written"
assert_grep "step-03 — Track A work" "$tmp/.wip/initiatives/demo/roadmap.md" "lane step written as bullet"
# The new step parses with lane A.
lane_of_03="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md" |
  jq -r '[.rounds[].steps[] | select(.id == "step-03")][0].lane')"
assert_eq "A" "$lane_of_03" "step-03 parses into lane A"
out_l2="$(run demo --from "$tmp/lane.md")"
assert_eq "true" "$(jq -r '.idempotent_noop' <<<"$out_l2")" "append-lane idempotent re-apply"

# 12. append-lane via CLI --target-round flag (matching front-matter).
make_roadmap
out_l3="$(run demo --from "$tmp/lane.md" --target-round 1)"
assert_eq "true" "$(jq -r '.ok' <<<"$out_l3")" "append-lane --target-round flag ok"

# 13. append-lane with a non-existent target round -> exit 4.
make_roadmap
cat >"$tmp/lane-badround.md" <<'MD'
---
target: demo
append-lane: A
target-round: 9
---
# Add Lane A
### step-03 — X
Body.
MD
set +e
run demo --from "$tmp/lane-badround.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "append-lane missing round exit 4"

# 14. append-lane missing target-round -> shape failure exit 4.
make_roadmap
cat >"$tmp/lane-noround.md" <<'MD'
---
target: demo
append-lane: A
---
# Add Lane A
### step-03 — X
Body.
MD
set +e
run demo --from "$tmp/lane-noround.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "append-lane no target-round exit 4"

# 14b. append-round whose body carries parallel lanes (bullet form) round-trips
# through parse with correct lane assignment (ADR-0010 decision 6).
make_roadmap
cat >"$tmp/round-lanes.md" <<'MD'
---
target: demo
append-round: Track expansion
---
# Track expansion

## Round 2 — Track expansion

- **step-12 — prereq** — lands first.

### Lane A
- **step-13 — track A** — spine.
- **step-15 — track A part 2** — provider.

### Lane D
- **step-14 — track D** — SPA.
MD
out_rl="$(run demo --from "$tmp/round-lanes.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out_rl")" "append-round-with-lanes ok"
rl_parse="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md")"
mapfile -t F < <(jq -r '
  ([.rounds[] | select(.n==2) | .lanes[]] | @json),
  ([.rounds[].steps[] | select(.id=="step-13")][0].lane),
  ([.rounds[].steps[] | select(.id=="step-14")][0].lane),
  (.lane_errors | length)' <<<"$rl_parse")
assert_eq '["A","D"]' "${F[0]}" "round 2 lanes [A,D]"
assert_eq "A" "${F[1]}" "step-13 lane A via append-round"
assert_eq "D" "${F[2]}" "step-14 lane D via append-round"
assert_eq "0" "${F[3]}" "append-round lanes: no lane errors"

# 14c. append-lane into a round that already has lanes + a post-lane main sync
# step must insert BEFORE the sync step, preserving `main* (lane+)? main*`.
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — Track expansion

- **step-01 — prereq** — lands first.

### Lane A
- **step-02 — track A** — spine.

- **step-03 — sync** — post-lane main sync step.

## Deferred
- nothing.
MD
cat >"$tmp/lane-b.md" <<'MD'
---
target: demo
append-lane: B
target-round: 1
---
# Add Lane B
### step-04 — track B
Body.
MD
out_lb="$(run demo --from "$tmp/lane-b.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out_lb")" "append-lane before sync ok"
lb_parse="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md")"
mapfile -t F < <(jq -r '
  (.lane_errors | length),
  ([.rounds[].steps[] | select(.id=="step-04")][0].lane),
  ([.rounds[].steps[] | select(.id=="step-03")][0].lane)' <<<"$lb_parse")
assert_eq "0" "${F[0]}" "append-lane before sync: no lane errors"
assert_eq "B" "${F[1]}" "step-04 lane B"
assert_eq "null" "${F[2]}" "sync step-03 stays main-lane"
# step-04 (Lane B) precedes the sync step-03 in declared order.
order="$(jq -r '[.rounds[].steps[].id] | (index("step-04")) < (index("step-03"))' <<<"$lb_parse")"
assert_eq "true" "$order" "new lane inserted before the sync step"

# 14d. append-lane with a name already present in the target round -> exit 4.
set +e
out_dup="$(run demo --from "$tmp/lane-b.md" --target-round 1 2>/dev/null)"
rc=$?
set -e
# (re-applying the SAME Lane B is an idempotent no-op, not a duplicate.)
assert_eq "true" "$(jq -r '.idempotent_noop' <<<"$out_dup")" "re-apply same lane is idempotent, not duplicate"
cat >"$tmp/lane-b-dup.md" <<'MD'
---
target: demo
append-lane: B
target-round: 1
---
# Add Lane B again (different content)
### step-05 — different track B
Body.
MD
set +e
out_dup2="$(run demo --from "$tmp/lane-b-dup.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "duplicate lane name exit 4"
assert_eq "duplicate-lane" "$(jq -r '.error.kind' <<<"$out_dup2")" "duplicate-lane error kind"

# ---- insert-step-in-lane (ADR-0010 §6, bundle promotion) ----
# 14e. insert-step-in-lane into an EMPTY declared lane (the bundle's emit
# pattern) + idempotent re-apply. Lane A is declared but has no steps yet.
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 2 — Track expansion

- **step-12 — F1 prereq** — lands first.

### Lane A

### Lane D

## Cross-cuts (from bundle)

- shared seam.

## Deferred
- nothing.
MD
cat >"$tmp/isil-a.md" <<'MD'
---
target: demo
insert-step-in-lane: A
target-round: 2
---
# Track A
### step-13 — Track A spine
Spine work.
MD
out_i="$(run demo --from "$tmp/isil-a.md")"
mapfile -t F < <(jq -r '.ok, .directive' <<<"$out_i")
assert_eq "true" "${F[0]}" "insert-step-in-lane ok"
assert_eq "insert-step-in-lane A (round 2)" "${F[1]}" "isil directive label"
isil_parse="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md")"
mapfile -t F < <(jq -r '
  ([.rounds[].steps[] | select(.id=="step-13")][0].lane),
  (.lane_errors | length)' <<<"$isil_parse")
assert_eq "A" "${F[0]}" "step-13 parses into empty lane A"
assert_eq "0" "${F[1]}" "isil: no lane errors"
out_i2="$(run demo --from "$tmp/isil-a.md")"
assert_eq "true" "$(jq -r '.idempotent_noop' <<<"$out_i2")" "insert-step-in-lane idempotent re-apply"

# 14f. insert-step-in-lane targeting a lane absent from the round -> exit 4.
cat >"$tmp/isil-z.md" <<'MD'
---
target: demo
insert-step-in-lane: Z
target-round: 2
---
# Track Z
### step-99 — nope
Body.
MD
set +e
out_iz="$(run demo --from "$tmp/isil-z.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "insert-step-in-lane absent lane exit 4"
assert_eq "lane-not-in-round" "$(jq -r '.error.kind' <<<"$out_iz")" "lane-not-in-round error kind"

# 14g. insert-step-in-lane targeting an absent round -> exit 4 round-not-in-roadmap.
cat >"$tmp/isil-r9.md" <<'MD'
---
target: demo
insert-step-in-lane: A
target-round: 9
---
# Track A
### step-98 — nope
Body.
MD
set +e
out_ir="$(run demo --from "$tmp/isil-r9.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "insert-step-in-lane absent round exit 4"
assert_eq "round-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out_ir")" "round-not-in-roadmap error kind"

# 14g-ii. insert-step-in-lane: --target-round disagreeing with the artifact's
# target-round: -> exit 2 directive-mismatch (the round is part of the contract).
set +e
out_im="$(run demo --from "$tmp/isil-a.md" --target-round 5 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "insert-step-in-lane target-round mismatch exit 2"
assert_eq "directive-mismatch" "$(jq -r '.error.kind' <<<"$out_im")" "isil target-round mismatch kind"
# A matching --target-round flag is accepted (round 2 matches the artifact).
out_iok="$(run demo --from "$tmp/isil-a.md" --target-round 2)"
assert_eq "true" "$(jq -r '.idempotent_noop' <<<"$out_iok")" "isil matching --target-round ok (idempotent)"

# 14h. insert-step-in-lane missing target-round -> shape failure exit 4.
cat >"$tmp/isil-noround.md" <<'MD'
---
target: demo
insert-step-in-lane: A
---
# Track A
### step-97 — x
Body.
MD
set +e
run demo --from "$tmp/isil-noround.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "insert-step-in-lane no target-round exit 4"

# 15. Refuse to amend a roadmap with a malformed lane structure (exit 4 lane-malformed).
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

### Lane A
- **step-01 — a** — body.

- **step-02 — orphan** — body.
### Lane D
- **step-03 — d** — body.

## Deferred
- nothing.
MD
set +e
out_m="$(run demo --from "$tmp/insert.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "malformed lane refuses amend exit 4"
assert_eq "lane-malformed" "$(jq -r '.error.kind' <<<"$out_m")" "lane-malformed error kind"

test_summary
