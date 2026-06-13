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
