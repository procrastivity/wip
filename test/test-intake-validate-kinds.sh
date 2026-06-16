#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="intake-validate-kinds"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

mkdir -p "$tmp/.wip/initiatives/auth"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: auth
initiatives:
  - slug: auth
    title: Auth
    status: in-flight
YAML
cat >"$tmp/.wip/initiatives/auth/roadmap.md" <<'MD'
# Roadmap — auth

- **step-01 — Skeleton**
- **step-02 — Login flow**
MD

run_v() { WIP_ROOT="$tmp" bin/wip-plumbing intake validate "$@"; }

# brief — valid.
cat >"$tmp/brief-ok.md" <<'MD'
# Foo

## Goal

Body.
MD
out="$(run_v "$tmp/brief-ok.md" --kind brief)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "brief valid"

# brief — target referencing existing slug -> use-amendment.
cat >"$tmp/brief-mistake.md" <<'MD'
---
target: auth
---
# Auth

## Goal
Body.
MD
set +e
out="$(run_v "$tmp/brief-mistake.md" --kind brief)"
rc=$?
set -e
assert_eq "4" "$rc" "brief with target exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "use-amendment")) | length' <<<"$out")" "brief use-amendment"

# amendment — insert-after happy path.
cat >"$tmp/amend-ok.md" <<'MD'
---
target: auth
insert-after: step-02
---
# New step

### step-03 — Logout

Body.
MD
out="$(run_v "$tmp/amend-ok.md" --kind amendment)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "amendment insert-after valid"

# amendment — missing directive.
cat >"$tmp/amend-no-dir.md" <<'MD'
---
target: auth
---
# No directive
Body.
MD
set +e
out="$(run_v "$tmp/amend-no-dir.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "amendment no directive exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "directive")) | length' <<<"$out")" "amendment missing directive"

# amendment — multiple directives.
cat >"$tmp/amend-multi.md" <<'MD'
---
target: auth
insert-after: step-02
replace: step-01
---
# Multi
### step-99 — X
MD
set +e
out="$(run_v "$tmp/amend-multi.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "amendment multi-directive exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "multiple-directives")) | length' <<<"$out")" "amendment multi flag"

# amendment — insert-after missing new step heading.
cat >"$tmp/amend-no-step.md" <<'MD'
---
target: auth
insert-after: step-02
---
# No heading
Just a paragraph.
MD
set +e
out="$(run_v "$tmp/amend-no-step.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "amendment no step heading exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "new-step-heading")) | length' <<<"$out")" "amendment new-step-heading"

# amendment — append-round happy.
cat >"$tmp/amend-round.md" <<'MD'
---
target: auth
append-round: Polish
---
# Round 4

## Round 4 — Polish

### step-10 — Cleanup
MD
out="$(run_v "$tmp/amend-round.md" --kind amendment)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "amendment append-round valid"

# amendment — append-round missing step heading.
cat >"$tmp/amend-round-nosteps.md" <<'MD'
---
target: auth
append-round: Polish
---
# X
## Round 4 — Polish

No steps.
MD
set +e
out="$(run_v "$tmp/amend-round-nosteps.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "append-round no steps exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "step-headings")) | length' <<<"$out")" "append-round step-headings"

# amendment — append-lane happy (target-round + step heading, no round heading).
cat >"$tmp/amend-lane.md" <<'MD'
---
target: auth
append-lane: A
target-round: 1
---
# New lane

### step-03 — Track A

Body.
MD
out="$(run_v "$tmp/amend-lane.md" --kind amendment)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "amendment append-lane valid"

# amendment — append-lane missing target-round.
cat >"$tmp/amend-lane-noround.md" <<'MD'
---
target: auth
append-lane: A
---
# New lane
### step-03 — Track A
Body.
MD
set +e
out="$(run_v "$tmp/amend-lane-noround.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "append-lane no target-round exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "target-round")) | length' <<<"$out")" "append-lane missing target-round"

# amendment — append-lane with an unexpected ## Round heading (should be append-round).
cat >"$tmp/amend-lane-round.md" <<'MD'
---
target: auth
append-lane: A
target-round: 1
---
# New lane
## Round 9 — Nope
### step-03 — Track A
Body.
MD
set +e
out="$(run_v "$tmp/amend-lane-round.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "append-lane with round heading exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "unexpected-round-heading")) | length' <<<"$out")" "append-lane unexpected-round-heading"

# amendment — append-lane is the fourth directive; two directives still rejected.
cat >"$tmp/amend-lane-multi.md" <<'MD'
---
target: auth
append-lane: A
insert-after: step-02
target-round: 1
---
# Multi
### step-03 — X
MD
set +e
out="$(run_v "$tmp/amend-lane-multi.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "append-lane + insert-after multi exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "multiple-directives")) | length' <<<"$out")" "append-lane counted toward multi"

# workplan-seed — existing step happy.
cat >"$tmp/wps-ok.md" <<'MD'
---
target: auth/step-02
---
# Workplan seed

Notes.
MD
out="$(run_v "$tmp/wps-ok.md" --kind workplan-seed)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "workplan-seed valid"

# workplan-seed — missing step.
cat >"$tmp/wps-bad.md" <<'MD'
---
target: auth/step-99
---
# Seed
Body.
MD
set +e
out="$(run_v "$tmp/wps-bad.md" --kind workplan-seed)"
rc=$?
set -e
assert_eq "4" "$rc" "workplan-seed missing step exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "step-not-in-roadmap")) | length' <<<"$out")" "step-not-in-roadmap"

# spec — Summary + User stories.
cat >"$tmp/spec-ok.md" <<'MD'
# Spec
## Summary
Foo.
## User stories
- a
MD
out="$(run_v "$tmp/spec-ok.md" --kind spec)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "spec valid"

# spec — missing Summary.
cat >"$tmp/spec-bad.md" <<'MD'
# Spec
## Requirements
- one
MD
set +e
out="$(run_v "$tmp/spec-bad.md" --kind spec)"
rc=$?
set -e
assert_eq "4" "$rc" "spec missing summary exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "summary-section")) | length' <<<"$out")" "spec summary-section"

# handoff — title only is valid.
cat >"$tmp/handoff-ok.md" <<'MD'
# Here is some context

Words.
MD
out="$(run_v "$tmp/handoff-ok.md" --kind handoff)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "handoff valid"

# amendment — insert-step-in-lane happy (target-round + step heading, no round).
cat >"$tmp/amend-isil.md" <<'MD'
---
target: auth
insert-step-in-lane: A
target-round: 1
---
# Track A
### step-03 — Track A
Body.
MD
out="$(run_v "$tmp/amend-isil.md" --kind amendment)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "amendment insert-step-in-lane valid"

# amendment — insert-step-in-lane missing target-round.
cat >"$tmp/amend-isil-noround.md" <<'MD'
---
target: auth
insert-step-in-lane: A
---
# Track A
### step-03 — Track A
Body.
MD
set +e
out="$(run_v "$tmp/amend-isil-noround.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "insert-step-in-lane no target-round exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "target-round")) | length' <<<"$out")" "isil missing target-round"

# amendment — insert-step-in-lane counts toward the multi-directive guard.
cat >"$tmp/amend-isil-multi.md" <<'MD'
---
target: auth
insert-step-in-lane: A
insert-after: step-02
target-round: 1
---
# Multi
### step-03 — X
MD
set +e
out="$(run_v "$tmp/amend-isil-multi.md" --kind amendment)"
rc=$?
set -e
assert_eq "4" "$rc" "isil + insert-after multi exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "multiple-directives")) | length' <<<"$out")" "isil counted toward multi"

# ---- bundle (intake-kinds.md §2/§3a) ----
# Child docs the bundle references (relative to the lead).
cat >"$tmp/childA.md" <<'MD'
# Track A
Body.
MD
cat >"$tmp/childD.md" <<'MD'
# Track D
Body.
MD

# bundle — valid (lead-as amendment, readable children, valid lead body).
cat >"$tmp/bundle-ok.md" <<'MD'
---
wip-kind: bundle
lead-as: amendment
target: auth
append-round: Track expansion
children:
  - path: childA.md
    lane: A
  - path: childD.md
    lane: D
cross-cuts:
  shared-seams:
    - shared seam
  parallel-groups:
    - [A, D]
---
# Track expansion

## Round 2 — Track expansion

- **step-03 — F1 prereq** — shared.
MD
out="$(run_v "$tmp/bundle-ok.md" --kind bundle)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "bundle valid"

# bundle — lead-as brief is also valid.
cat >"$tmp/bundle-brief.md" <<'MD'
---
wip-kind: bundle
lead-as: brief
children:
  - path: childA.md
---
# New initiative

## Goal

Do the thing.
MD
out="$(run_v "$tmp/bundle-brief.md" --kind bundle)"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "bundle lead-as brief valid"

# bundle — empty children.
cat >"$tmp/bundle-empty.md" <<'MD'
---
wip-kind: bundle
lead-as: brief
children: []
---
# X
## Goal
y
MD
set +e
out="$(run_v "$tmp/bundle-empty.md" --kind bundle)"
rc=$?
set -e
assert_eq "4" "$rc" "bundle empty children exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "children")) | length' <<<"$out")" "bundle empty children flag"

# bundle — unreadable child path.
cat >"$tmp/bundle-badchild.md" <<'MD'
---
wip-kind: bundle
lead-as: amendment
target: auth
append-round: X
children:
  - path: nope-missing.md
---
# X
## Round 2 — X
### step-03 — a
MD
set +e
out="$(run_v "$tmp/bundle-badchild.md" --kind bundle)"
rc=$?
set -e
assert_eq "4" "$rc" "bundle unreadable child exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "child-unreadable")) | length' <<<"$out")" "bundle child-unreadable flag"

# bundle — bad lead-as.
cat >"$tmp/bundle-badleadas.md" <<'MD'
---
wip-kind: bundle
lead-as: spec
children:
  - path: childA.md
---
# X
## Goal
y
MD
set +e
out="$(run_v "$tmp/bundle-badleadas.md" --kind bundle)"
rc=$?
set -e
assert_eq "4" "$rc" "bundle bad lead-as exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "lead-as")) | length' <<<"$out")" "bundle lead-as flag"

# bundle — invalid lead body (amendment lead with no directive) -> lead:directive.
cat >"$tmp/bundle-badbody.md" <<'MD'
---
wip-kind: bundle
lead-as: amendment
target: auth
children:
  - path: childA.md
---
# X

No directive at all.
MD
set +e
out="$(run_v "$tmp/bundle-badbody.md" --kind bundle)"
rc=$?
set -e
assert_eq "4" "$rc" "bundle invalid lead body exit 4"
assert_eq "1" "$(jq '.missing | map(select(. == "lead:directive")) | length' <<<"$out")" "bundle lead:directive flag"

# --kind unknown -> exit 2.
set +e
run_v "$tmp/brief-ok.md" --kind nope >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "unknown --kind exit 2"

# no --kind: classify guess, then validate. Brief heuristic file passes brief.
out="$(run_v "$tmp/brief-ok.md")"
assert_eq "true" "$(jq -r '.valid' <<<"$out")" "no --kind: valid via classify"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "no --kind: kind from classify"

test_summary
