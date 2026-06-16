#!/usr/bin/env bash
# test-wip-intake-bundle — the bundle explode porcelain (ADR-0009 + ADR-0010).
#
# A bundle is a roadmap-shaped LEAD doc + N child handoffs. `wip intake` shapes
# the bundle, applies the lead (append-round + empty lanes + Cross-cuts), then
# fans each child through the single-file pipeline (insert-step-in-lane). The
# provider is stubbed via WIP_PROVIDER_CMD, dispatching per-call canned shapes.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-bundle"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-15"

mk_repo() {
  rm -rf "$tmp/.wip"
  mkdir -p "$tmp/.wip/initiatives/tc"
  cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: tc
initiatives:
  - slug: tc
    title: Typed Context
    status: in-flight
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML
  cat >"$tmp/.wip/initiatives/tc/roadmap.md" <<'MD'
# Roadmap — tc

## Round 1 — Foundations

- **step-01 — Alpha** ✅ — done.
- **step-02 — Beta** — current.

## Deferred
- nothing.
MD
}

# Child docs live next to the lead.
cat >"$tmp/spine.md" <<'MD'
# Core document spine SPINEDOC

Build the core.document spine for Track A.
MD
cat >"$tmp/spa.md" <<'MD'
# SPA usability SPADOC

Ship SPA usability v1 for Track D.
MD
cat >"$tmp/lead.md" <<'MD'
# Post phase-0 roadmap POSTPHASE0

## Tracks

- Track A — core.document spine
- Track D — SPA usability

## Recommended sequence

F1 first, then A and D in parallel.
MD

mk() { jq -nc --arg c "$1" '{choices:[{message:{role:"assistant",content:$c}}]}'; }

bundle_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: tc\nappend-round: Track expansion\nchildren:\n  - path: spine.md\n    kind: amendment\n    lane: A\n    depends-on: spa.md\n  - path: spa.md\n    kind: amendment\n    lane: D\ncross-cuts:\n  shared-seams:\n    - ChatRespondLoop prompt-assembly (touches Track A and Track D)\n  parallel-groups:\n    - [A, D]\n---\n# Track expansion\n\n## Round 2 — Track expansion\n\n- **step-03 — F1: model-profile taxonomy** — the shared prereq for both lanes.\n'
childA_art=$'---\nwip-kind: amendment\ntarget: tc\ninsert-step-in-lane: A\ntarget-round: 2\n---\n# Track A\n\n### step-04 — Track A: core.document spine\n\nBuild the spine.\n'
childD_art=$'---\nwip-kind: amendment\ntarget: tc\ninsert-step-in-lane: D\ntarget-round: 2\n---\n# Track D\n\n### step-05 — Track D: SPA usability v1\n\nShip SPA usability.\n'
mk "$bundle_art" >"$tmp/respBundle.json"
mk "$childA_art" >"$tmp/respA.json"
mk "$childD_art" >"$tmp/respD.json"

# Dispatcher: distinguish the three shape calls by markers in the request.
cat >"$tmp/dispatch.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q SPINEDOC; then cat "$tmp/respA.json"
elif printf '%s' "\$req" | grep -q SPADOC; then cat "$tmp/respD.json"
else cat "$tmp/respBundle.json"; fi
EOF

RUN() {
  WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
    WIP_PROVIDER_CMD="bash $tmp/dispatch.sh" \
    bin/wip intake "$tmp/lead.md" --kind bundle --yes "$@" 2>/dev/null
}
roadmap="$tmp/.wip/initiatives/tc/roadmap.md"

# 1. Full explode: lead + 2 lane children, aggregate ok.
mk_repo
out="$(RUN)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "explode ok"
assert_eq "bundle" "$(jq -r '.kind' <<<"$out")" "kind bundle"
assert_eq "tc" "$(jq -r '.target' <<<"$out")" "target tc"
assert_eq "true" "$(jq -r '.lead.ok' <<<"$out")" "lead applied"
assert_eq "2" "$(jq -r '.summary.children' <<<"$out")" "2 children"
assert_eq "2" "$(jq -r '.summary.applied' <<<"$out")" "2 applied"
assert_eq "true" "$(jq -r '[.children[].ok] | all' <<<"$out")" "all children ok"

# On disk: lane-shaped round 2.
assert_grep "## Round 2 — Track expansion" "$roadmap" "round 2 added"
assert_grep "### Lane A" "$roadmap" "Lane A heading"
assert_grep "### Lane D" "$roadmap" "Lane D heading"
assert_grep "## Cross-cuts (from bundle)" "$roadmap" "Cross-cuts section"
assert_grep "ChatRespondLoop prompt-assembly" "$roadmap" "shared seam persisted"
assert_grep "step-04 — Track A: core.document spine" "$roadmap" "step-04 in roadmap"
assert_grep "step-05 — Track D: SPA usability v1" "$roadmap" "step-05 in roadmap"

P="$(WIP_ROOT="$tmp" bin/wip-plumbing roadmap parse "$roadmap")"
assert_eq '["A","D"]' "$(jq -c '[.rounds[]|select(.n==2)|.lanes[]]' <<<"$P")" "round 2 lanes [A,D]"
assert_eq "A" "$(jq -r '[.rounds[].steps[]|select(.id=="step-04")][0].lane' <<<"$P")" "step-04 lane A"
assert_eq "D" "$(jq -r '[.rounds[].steps[]|select(.id=="step-05")][0].lane' <<<"$P")" "step-05 lane D"
assert_eq "null" "$(jq -r '[.rounds[].steps[]|select(.id=="step-03")][0].lane' <<<"$P")" "step-03 (F1) main lane"
assert_eq "0" "$(jq -r '.lane_errors | length' <<<"$P")" "no lane errors"

# Source child docs are untouched.
assert_grep "SPINEDOC" "$tmp/spine.md" "spine.md untouched"
assert_grep "SPADOC" "$tmp/spa.md" "spa.md untouched"

# No leftover shape tempfiles in the lead's directory.
assert_eq "0" "$(find "$tmp" -maxdepth 1 -name '.wip-intake-shape.*' | wc -l | tr -d ' ')" "no leftover shape temps"

# 2. Idempotent re-apply: re-running writes nothing new.
before="$(md5sum "$roadmap" | awk '{print $1}')"
out2="$(RUN)"
assert_eq "true" "$(jq -r '.ok' <<<"$out2")" "re-apply ok"
after="$(md5sum "$roadmap" | awk '{print $1}')"
assert_eq "$before" "$after" "re-apply idempotent (roadmap unchanged)"

# 3. --dry-run fans out without writing.
mk_repo
out3="$(RUN --dry-run)"
assert_eq "true" "$(jq -r '.ok' <<<"$out3")" "dry-run ok"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out3")" "dry-run flag"
assert_eq "true" "$(jq -r '.lead.ok' <<<"$out3")" "dry-run lead valid"
assert_eq "2" "$(jq -r '.summary.applied' <<<"$out3")" "dry-run fanned out 2 children"
assert_not_grep "## Round 2 — Track expansion" "$roadmap" "dry-run wrote nothing"

# 4. Topo order honors depends-on: spine.md depends-on spa.md, so spa (D)
#    is applied before spine (A) in the children envelope.
idxA="$(jq -r '[.children | to_entries[] | select(.value.path=="spine.md") | .key][0]' <<<"$out")"
idxD="$(jq -r '[.children | to_entries[] | select(.value.path=="spa.md") | .key][0]' <<<"$out")"
if [[ "$idxD" -lt "$idxA" ]]; then
  _WIP_PASS=$((_WIP_PASS + 1))
  echo "  ok   depends-on ordering (spa before spine)"
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  echo "  FAIL depends-on ordering (D=$idxD A=$idxA)" >&2
fi

# 5. Nested bundle refused: a child declaring kind: bundle fails that child
#    (captured non-fatally), so the aggregate is ok:false.
mk_repo
nested_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: tc\nappend-round: X\nchildren:\n  - path: spine.md\n    kind: bundle\n---\n# X\n\n## Round 2 — X\n\n- **step-03 — prereq** — body.\n'
mk "$nested_art" >"$tmp/respNested.json"
cat >"$tmp/dispatch-nested.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q SPINEDOC; then cat "$tmp/respBundle.json"
else cat "$tmp/respNested.json"; fi
EOF
# Aggregate ok:false -> wip exits non-zero, so capture under set +e.
set +e
out5="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="bash $tmp/dispatch-nested.sh" \
  bin/wip intake "$tmp/lead.md" --kind bundle --yes 2>/dev/null)"
set -e
assert_eq "false" "$(jq -r '.ok' <<<"$out5")" "nested bundle -> aggregate not ok"
assert_eq "false" "$(jq -r '[.children[].ok] | all' <<<"$out5")" "nested-bundle child failed"
assert_eq "nested-bundle" "$(jq -r '[.children[] | select(.ok==false) | .result.error.kind][0]' <<<"$out5")" "nested-bundle error kind"

# 6. --kind bundle is accepted by the porcelain flag validator.
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="bash $tmp/dispatch.sh" \
  bin/wip intake "$tmp/lead.md" --kind nonsense --yes >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "bad --kind exit 2 (flag list still rejects nonsense)"

test_summary
