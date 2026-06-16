#!/usr/bin/env bash
# test-wip-intake-bundle-roundtrip — the canonical 4-doc post-phase-0 round-trip
# (kickoff verification gate). Input: a roadmap-shaped LEAD doc + 3 child
# handoffs (F1 = model-profile taxonomy, Track A = core.document spine, Track D
# = SPA usability). Expected on disk: a "Track expansion" round with F1 in the
# main lane, Lane A / Lane D each carrying one step, and a Cross-cuts section —
# lane_errors empty, idempotent on re-run. F1 is the shared prereq: the bundle
# shaper folds it into the lead's main-lane step, so it appears as a child but
# the explode records it folded-into-lead rather than re-applying it.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-bundle-roundtrip"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-15"

mkdir -p "$tmp/.wip/initiatives/typed-context"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: typed-context
initiatives:
  - slug: typed-context
    title: Typed Context
    status: in-flight
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML
# Existing roadmap ends at round 3 / step-11, so the new round is Round 4 with
# step-12 (F1) / step-13 (A) / step-14 (D) — the walkthrough's exact ids.
cat >"$tmp/.wip/initiatives/typed-context/roadmap.md" <<'MD'
# Roadmap — typed-context

## Round 3 — Phase 0

- **step-10 — Phase-0 groundwork** ✅ — done.
- **step-11 — Phase-0 close** — current.

## Deferred
- nothing.
MD
roadmap="$tmp/.wip/initiatives/typed-context/roadmap.md"

# The four source docs (lead + 3 children) live together. The per-child marker
# tokens (MPTAXONOMY / SPINEDOC / SPADOC) live ONLY in the child docs so the
# dispatcher can route each child's shape request — the lead must not contain
# them, or the bundle-shape request (which embeds the lead) would mis-route.
cat >"$tmp/handoff-post-phase0-roadmap.md" <<'MD'
# Post phase-0 roadmap

## Foundational items

- F1 — model-profile taxonomy (prereq for both tracks).

## Track A — the vertical spine

The core.document spine.

## Track D — daily-driver usability

Daily-driver usability, parallel to A.

## Recommended sequence

F1 first, then Track A and Track D in parallel.
MD
cat >"$tmp/handoff-model-profile-taxonomy.md" <<'MD'
# Model-profile taxonomy MPTAXONOMY

The shared prereq feeding every track.
MD
cat >"$tmp/handoff-core-document-spine.md" <<'MD'
# Core document spine SPINEDOC

Build the core.document spine for Track A.
MD
cat >"$tmp/handoff-spa-usability-v1.md" <<'MD'
# SPA usability v1 SPADOC

Ship daily-driver SPA usability for Track D.
MD

mk() { jq -nc --arg c "$1" '{choices:[{message:{role:"assistant",content:$c}}]}'; }

# Bundle shape: F1 folded into the lead's main-lane step-12; F1 listed as a child
# with no lane/directive (folded); spine -> Lane A, spa -> Lane D.
bundle_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: typed-context\nappend-round: Track expansion\nchildren:\n  - path: handoff-model-profile-taxonomy.md\n    kind: amendment\n  - path: handoff-core-document-spine.md\n    kind: amendment\n    lane: A\n    depends-on: handoff-model-profile-taxonomy.md\n  - path: handoff-spa-usability-v1.md\n    kind: amendment\n    lane: D\n    depends-on: handoff-model-profile-taxonomy.md\ncross-cuts:\n  shared-seams:\n    - ChatRespondLoop prompt-assembly (touches Track A and Track D)\n  parallel-groups:\n    - [A, D]\n---\n# Track expansion\n\n## Round 4 — Track expansion\n\n- **step-12 — F1: model-profile taxonomy** — the shared prereq for both lanes.\n'
spine_art=$'---\nwip-kind: amendment\ntarget: typed-context\ninsert-step-in-lane: A\ntarget-round: 4\n---\n# Track A\n\n### step-13 — Track A: core.document spine\n\nThe vertical spine.\n'
spa_art=$'---\nwip-kind: amendment\ntarget: typed-context\ninsert-step-in-lane: D\ntarget-round: 4\n---\n# Track D\n\n### step-14 — Track D: SPA usability v1\n\nDaily-driver usability.\n'
mk "$bundle_art" >"$tmp/respBundle.json"
mk "$spine_art" >"$tmp/respSpine.json"
mk "$spa_art" >"$tmp/respSpa.json"

cat >"$tmp/dispatch.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q SPINEDOC; then cat "$tmp/respSpine.json"
elif printf '%s' "\$req" | grep -q SPADOC; then cat "$tmp/respSpa.json"
else cat "$tmp/respBundle.json"; fi
EOF

RUN() {
  WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
    WIP_PROVIDER_CMD="bash $tmp/dispatch.sh" \
    bin/wip intake "$tmp/handoff-post-phase0-roadmap.md" --kind bundle --yes 2>/dev/null
}

# --- run the explode -------------------------------------------------------
out="$(RUN)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "round-trip ok"
assert_eq "true" "$(jq -r '.lead.ok' <<<"$out")" "lead applied"
assert_eq "3" "$(jq -r '.summary.children' <<<"$out")" "3 children in manifest"
assert_eq "2" "$(jq -r '.summary.applied' <<<"$out")" "2 children applied (F1 folded)"
# F1 recorded folded-into-lead, not re-applied.
assert_eq "folded-into-lead" \
  "$(jq -r '[.children[] | select(.path=="handoff-model-profile-taxonomy.md") | .skipped][0]' <<<"$out")" \
  "F1 folded into lead"

# --- on-disk shape matches the walkthrough ---------------------------------
P="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$roadmap")"
assert_eq "Track expansion" "$(jq -r '[.rounds[]|select(.n==4)|.title][0]' <<<"$P")" "Round 4 = Track expansion"
assert_eq '["A","D"]' "$(jq -c '[.rounds[]|select(.n==4)|.lanes[]]' <<<"$P")" "round 4 lanes [A,D]"
assert_eq "null" "$(jq -r '[.rounds[].steps[]|select(.id=="step-12")][0].lane' <<<"$P")" "F1 step-12 main lane"
assert_eq "A" "$(jq -r '[.rounds[].steps[]|select(.id=="step-13")][0].lane' <<<"$P")" "spine step-13 lane A"
assert_eq "D" "$(jq -r '[.rounds[].steps[]|select(.id=="step-14")][0].lane' <<<"$P")" "spa step-14 lane D"
assert_eq "0" "$(jq -r '.lane_errors | length' <<<"$P")" "lane_errors empty (lanes regression intact)"
assert_grep "## Cross-cuts (from bundle)" "$roadmap" "Cross-cuts section on disk"
assert_grep "ChatRespondLoop prompt-assembly" "$roadmap" "shared seam persisted"

# --- the four source docs are untouched ------------------------------------
assert_grep "MPTAXONOMY" "$tmp/handoff-model-profile-taxonomy.md" "F1 doc untouched"
assert_grep "SPINEDOC" "$tmp/handoff-core-document-spine.md" "spine doc untouched"
assert_grep "SPADOC" "$tmp/handoff-spa-usability-v1.md" "spa doc untouched"
assert_grep "Recommended sequence" "$tmp/handoff-post-phase0-roadmap.md" "lead doc untouched"

# --- re-run is idempotent --------------------------------------------------
before="$(md5sum "$roadmap" | awk '{print $1}')"
out2="$(RUN)"
assert_eq "true" "$(jq -r '.ok' <<<"$out2")" "re-run ok"
after="$(md5sum "$roadmap" | awk '{print $1}')"
assert_eq "$before" "$after" "re-run idempotent (roadmap unchanged)"

# --- lanes regression: roadmap parse stays clean ---------------------------
assert_eq "0" "$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$roadmap" | jq -r '.lane_errors | length')" "parse lane_errors: [] after round-trip"

test_summary
