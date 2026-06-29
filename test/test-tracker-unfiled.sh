#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-unfiled"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-09 (BRIEF §7): surface deferred / backlog items with no `[tracker: ID]`
# mapping as a SUGGESTION (never auto-filed). status -> unfiled_tracker_items;
# doctor -> informational tracker-unfiled note (status ok, never drift). Only
# when issue-tracker is enabled.

export WIP_NO_REGISTRY=1
WIP=bin/wip-plumbing

tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — x.

## Deferred (decided-not-now)

- **Filed thing** — already tracked. [tracker: BDS-50]
- **Unfiled deferred thing** — not tracked yet.

## Backlog (cross-cutting)

- **Unfiled backlog thing** — cross-cutting, untracked.
MD

# --- parser: tracker key on deferred/backlog entries ------------------------
doc="$(WIP_ROOT="$tmp" $WIP roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md")"
assert_eq "BDS-50" "$(jq -r '.deferred[] | select(.title=="Filed thing") | .tracker' <<<"$doc")" \
  "deferred entry parses its [tracker: ID]"
assert_eq "null" "$(jq -r '.deferred[] | select(.title=="Unfiled deferred thing") | .tracker' <<<"$doc")" \
  "unfiled deferred -> tracker null"
assert_eq "null" "$(jq -r '.backlog[] | select(.title=="Unfiled backlog thing") | .tracker' <<<"$doc")" \
  "unfiled backlog -> tracker null"

# --- status: unfiled_tracker_items ------------------------------------------
s="$(WIP_ROOT="$tmp" $WIP status)"
assert_eq "2" "$(jq -r '.unfiled_tracker_items | length' <<<"$s")" "status: 2 unfiled (filed one excluded)"
assert_eq "deferred" "$(jq -r '.unfiled_tracker_items[] | select(.id=="unfiled-deferred-thing") | .source' <<<"$s")" \
  "status: deferred source tagged"
assert_eq "backlog" "$(jq -r '.unfiled_tracker_items[] | select(.id=="unfiled-backlog-thing") | .source' <<<"$s")" \
  "status: backlog source tagged"
assert_eq "0" "$(jq -r '[.unfiled_tracker_items[] | select(.title=="Filed thing")] | length' <<<"$s")" \
  "status: the filed item is NOT suggested"

# --- doctor: informational note, never drift --------------------------------
set +e
WIP_ROOT="$tmp" $WIP doctor >/dev/null 2>&1
drc=$?
set -e
assert_eq "0" "$drc" "doctor stays exit 0 (a suggestion is not drift)"
dn="$(WIP_ROOT="$tmp" $WIP doctor 2>/dev/null | jq -c '.checks[] | select(.kind=="tracker-unfiled")')"
assert_eq "ok" "$(jq -r '.status' <<<"$dn")" "doctor note status ok"
assert_eq "2" "$(jq -r '.count' <<<"$dn")" "doctor note counts 2 unfiled"

# --- issue-tracker disabled -> no surfacing anywhere ------------------------
WIP_ROOT="$tmp" yq -i 'del(.features."issue-tracker")' "$tmp/.wip.yaml"
s2="$(WIP_ROOT="$tmp" $WIP status)"
assert_eq "0" "$(jq -r '.unfiled_tracker_items | length' <<<"$s2")" "tracker off -> status surfaces nothing"
assert_eq "0" "$(WIP_ROOT="$tmp" $WIP doctor 2>/dev/null | jq '[.checks[] | select(.kind=="tracker-unfiled")] | length')" \
  "tracker off -> doctor surfaces nothing"

# --- all items filed -> no suggestion ---------------------------------------
tmpF="$(wip_mktemp)"
mkdir -p "$tmpF/.wip/initiatives/demo"
cat >"$tmpF/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x.\n\n## Deferred (decided-not-now)\n\n- **All filed** — tracked. [tracker: BDS-1]\n' \
  >"$tmpF/.wip/initiatives/demo/roadmap.md"
assert_eq "0" "$(jq -r '.unfiled_tracker_items | length' <<<"$(WIP_ROOT="$tmpF" $WIP status)")" \
  "all items filed -> no unfiled suggestion"

test_summary
