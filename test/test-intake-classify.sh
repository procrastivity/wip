#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="intake-classify"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# Build a tmp repo with two initiatives — "auth" with a couple of steps,
# and "billing" with one.
mkdir -p "$tmp/.wip/initiatives/auth" "$tmp/.wip/initiatives/billing"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: auth
initiatives:
  - slug: auth
    title: Auth
    status: in-flight
  - slug: billing
    title: Billing
    status: in-flight
YAML
cat >"$tmp/.wip/initiatives/auth/roadmap.md" <<'MD'
# Roadmap — auth

## Round 1 — Bootstrap

- **step-01 — Skeleton** — repo bootstrap.
- **step-02 — Login flow** — happy path only.
- **step-02.5 — MFA prompt** — gated on step-02.
MD

run_classify() { WIP_ROOT="$tmp" bin/wip-plumbing intake classify "$1"; }

# 1. wip-kind front-matter -> high.
cat >"$tmp/a.md" <<'MD'
---
wip-kind: brief
---
# New Thing

## Goal

Hi.
MD
out="$(run_classify "$tmp/a.md")"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "wip-kind brief"
assert_eq "high" "$(jq -r '.confidence' <<<"$out")" "wip-kind brief confidence"

# 2. target + directive -> amendment high.
cat >"$tmp/b.md" <<'MD'
---
target: auth
insert-after: step-02
---
# New step
### step-03 — Logout

Body.
MD
out="$(run_classify "$tmp/b.md")"
assert_eq "amendment" "$(jq -r '.kind' <<<"$out")" "target+directive amendment"
assert_eq "high" "$(jq -r '.confidence' <<<"$out")" "target+directive confidence"

# 3. target slug/step (existing) -> workplan-seed high.
cat >"$tmp/c.md" <<'MD'
---
target: auth/step-02
---
# Login workplan

Body.
MD
out="$(run_classify "$tmp/c.md")"
assert_eq "workplan-seed" "$(jq -r '.kind' <<<"$out")" "target slug/step workplan-seed"
assert_eq "high" "$(jq -r '.confidence' <<<"$out")" "workplan-seed confidence"

# 4. target alone -> amendment medium.
cat >"$tmp/d.md" <<'MD'
---
target: auth
---
# Notes
Body.
MD
out="$(run_classify "$tmp/d.md")"
assert_eq "amendment" "$(jq -r '.kind' <<<"$out")" "target alone amendment"
assert_eq "medium" "$(jq -r '.confidence' <<<"$out")" "target alone medium"

# 5. spec body sections -> spec medium.
cat >"$tmp/e.md" <<'MD'
# Spec
## Summary
Foo.
## User stories
- a
MD
out="$(run_classify "$tmp/e.md")"
assert_eq "spec" "$(jq -r '.kind' <<<"$out")" "spec heuristic"
assert_eq "medium" "$(jq -r '.confidence' <<<"$out")" "spec confidence"

# 6. brief heuristic.
cat >"$tmp/f.md" <<'MD'
# Brief
## Goal
Words.
MD
out="$(run_classify "$tmp/f.md")"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "brief heuristic"
assert_eq "medium" "$(jq -r '.confidence' <<<"$out")" "brief medium"

# 7. handoff fallback (title only).
cat >"$tmp/g.md" <<'MD'
# Just a title

Some text.
MD
out="$(run_classify "$tmp/g.md")"
assert_eq "handoff" "$(jq -r '.kind' <<<"$out")" "handoff fallback"
assert_eq "low" "$(jq -r '.confidence' <<<"$out")" "handoff low"

# 8. no title -> exit 4.
cat >"$tmp/h.md" <<'MD'
## Goal
Body.
MD
set +e
out="$(run_classify "$tmp/h.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "no title exit 4"

# 9. target unknown slug (manifest present) -> handoff low with signal.
cat >"$tmp/i.md" <<'MD'
---
target: nope
---
# Foo

Body.
MD
out="$(run_classify "$tmp/i.md")"
assert_eq "handoff" "$(jq -r '.kind' <<<"$out")" "unknown target -> handoff"
assert_eq "low" "$(jq -r '.confidence' <<<"$out")" "unknown target low"
assert_eq "1" "$(jq '.signals | map(select(. == "unknown-target")) | length' <<<"$out")" "unknown-target signal"

# 9b. roadmap-shaped lead doc -> bundle low (intake-kinds.md §4).
cat >"$tmp/roadmapish.md" <<'MD'
# Post phase-0 roadmap

## Tracks

- Track A — spine
- Track D — SPA

## Recommended sequence

F1 first, then A and D in parallel.
MD
out="$(run_classify "$tmp/roadmapish.md")"
assert_eq "bundle" "$(jq -r '.kind' <<<"$out")" "roadmap-shaped -> bundle"
assert_eq "low" "$(jq -r '.confidence' <<<"$out")" "bundle low"
assert_eq "1" "$(jq '.signals | map(select(. == "roadmap-shaped-handoff")) | length' <<<"$out")" "roadmap-shaped signal"

# 9b-ii. per-track headings (## Track A / ## Track D) + foundational + sequence,
# the real post-phase-0 roadmap shape, also trip the bundle heuristic.
cat >"$tmp/pertrack.md" <<'MD'
# Post phase-0 roadmap

## Foundational items

- F1 — model-profile taxonomy.

## Track A — the vertical spine

Spine work.

## Track D — daily-driver usability

SPA work.

## Recommended sequence

F1, then A and D in parallel.
MD
out="$(run_classify "$tmp/pertrack.md")"
assert_eq "bundle" "$(jq -r '.kind' <<<"$out")" "per-track headings -> bundle"
assert_eq "low" "$(jq -r '.confidence' <<<"$out")" "per-track bundle low"

# 9c. wip-kind: bundle front-matter -> bundle high.
cat >"$tmp/bundle-fm.md" <<'MD'
---
wip-kind: bundle
lead-as: amendment
---
# Lead

## Tracks
- A
MD
out="$(run_classify "$tmp/bundle-fm.md")"
assert_eq "bundle" "$(jq -r '.kind' <<<"$out")" "wip-kind bundle -> bundle"
assert_eq "high" "$(jq -r '.confidence' <<<"$out")" "wip-kind bundle high"

# 9d. tracks WITHOUT a sequence section does NOT trip the bundle heuristic.
cat >"$tmp/tracks-only.md" <<'MD'
# Just tracks

## Tracks
- A
- D
MD
out="$(run_classify "$tmp/tracks-only.md")"
assert_eq "handoff" "$(jq -r '.kind' <<<"$out")" "tracks-only stays handoff (no sequence)"

# 10. no manifest reachable -> signal no-manifest.
mkdir -p "$tmp/empty"
out="$(WIP_ROOT="$tmp/empty" bin/wip-plumbing intake classify "$tmp/d.md" 2>/dev/null || true)"
# WIP_ROOT="$tmp/empty" has no .wip.yaml; wip_find_root fails — but the dispatcher
# does NOT call wip_find_root for intake. The classifier's existing_slugs returns
# [] and emits the no-manifest signal.
if [[ -n "$out" ]]; then
  assert_eq "1" "$(jq '.signals | map(select(. == "no-manifest")) | length' <<<"$out")" "no-manifest signal"
fi

test_summary
