#!/usr/bin/env bash
# test-wip-bundle — the multi-file bundle assembler porcelain (ADR-0011).
#
# `wip bundle <f1> <f2> ...` assembles N loose handoff files into ONE bundle
# lead manifest (provider-shaper, mocked via WIP_PROVIDER_CMD), writes it to the
# inputs' common-parent dir (or -o), validates it via the EXISTING plumbing
# `intake validate --kind bundle`, and on --intake chains into the UNCHANGED
# `wip intake <manifest> --kind bundle` explode.
#
# The mock dispatcher distinguishes three provider calls by markers:
#   - assemble call          → system prompt contains "ASSEMBLE N loose handoff"
#   - intake bundle reshape  → system prompt contains "Target kind: bundle"
#   - per-child amendment    → seed body contains SPINEDOC / SPADOC
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-bundle"
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
MD
}

# Input handoff files live next to where the manifest will be written.
cat >"$tmp/spine.md" <<'MD'
# Core document spine SPINEDOC

Build the core.document spine for Track A.
MD
cat >"$tmp/spa.md" <<'MD'
# SPA usability SPADOC

Ship SPA usability v1 for Track D.
MD

mk() { jq -nc --arg c "$1" '{choices:[{message:{role:"assistant",content:$c}}]}'; }

# The manifest the assemble shaper (and the intake reshape) returns. Child
# paths are basenames — inputs sit in the manifest's own directory.
bundle_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: tc\nappend-round: Track expansion\nchildren:\n  - path: spine.md\n    kind: amendment\n    lane: A\n  - path: spa.md\n    kind: amendment\n    lane: D\ncross-cuts:\n  shared-seams:\n    - ChatRespondLoop prompt-assembly (touches Track A and Track D)\n  parallel-groups:\n    - [A, D]\n---\n# Track expansion\n\n## Round 2 — Track expansion\n\n- **step-03 — F1: model-profile taxonomy** — the shared prereq for both lanes.\n'
childA_art=$'---\nwip-kind: amendment\ntarget: tc\ninsert-step-in-lane: A\ntarget-round: 2\n---\n# Track A\n\n### step-04 — Track A: core.document spine\n\nBuild the spine.\n'
childD_art=$'---\nwip-kind: amendment\ntarget: tc\ninsert-step-in-lane: D\ntarget-round: 2\n---\n# Track D\n\n### step-05 — Track D: SPA usability v1\n\nShip SPA usability.\n'
mk "$bundle_art" >"$tmp/respBundle.json"
mk "$childA_art" >"$tmp/respA.json"
mk "$childD_art" >"$tmp/respD.json"

cat >"$tmp/dispatch.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q "ASSEMBLE N loose handoff"; then cat "$tmp/respBundle.json"
elif printf '%s' "\$req" | grep -q "Target kind: bundle"; then cat "$tmp/respBundle.json"
elif printf '%s' "\$req" | grep -q SPINEDOC; then cat "$tmp/respA.json"
else cat "$tmp/respD.json"; fi
EOF

RUN() {
  WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
    WIP_PROVIDER_CMD="bash $tmp/dispatch.sh" \
    bin/wip bundle "$@" 2>/dev/null
}
roadmap="$tmp/.wip/initiatives/tc/roadmap.md"

# 1. Assemble-only (no --intake): valid manifest written, review-first default.
mk_repo
rm -f "$tmp/bundle.md"
out="$(RUN "$tmp/spine.md" "$tmp/spa.md" --yes)"
mapfile -t F < <(jq -r '.ok, .verb, .manifest, .lead_as, .target, ([.children[].path]|join(",")), .children[0].lane, .wrote[0]' <<<"$out")
assert_eq "true" "${F[0]}" "assemble ok"
assert_eq "bundle" "${F[1]}" "verb bundle"
assert_eq "$tmp/bundle.md" "${F[2]}" "manifest in common parent"
assert_eq "amendment" "${F[3]}" "lead_as amendment"
assert_eq "tc" "${F[4]}" "target tc"
assert_eq "spine.md,spa.md" "${F[5]}" "children basenames"
assert_eq "A" "${F[6]}" "child lane A surfaced"
assert_eq "$tmp/bundle.md" "${F[7]}" "wrote the manifest"
assert_file "$tmp/bundle.md" "bundle.md exists on disk"
assert_grep "wip-kind: bundle" "$tmp/bundle.md" "manifest carries wip-kind: bundle"
# Assemble alone does NOT touch the roadmap (no --intake).
assert_not_grep "## Round 2 — Track expansion" "$roadmap" "assemble-only leaves roadmap alone"
# No leftover shape tempfiles next to the manifest.
assert_eq "0" "$(find "$tmp" -maxdepth 1 -name '.wip-bundle-shape.*' | wc -l | tr -d ' ')" "no leftover shape temps"
# Inputs untouched.
assert_grep "SPINEDOC" "$tmp/spine.md" "spine.md untouched"

# 2. Child paths resolve RELATIVE to the manifest dir. Inputs in distinct
#    subdirs → common parent is $tmp → child paths carry the subdir prefix and
#    resolve under it for validate.
mk_repo
mkdir -p "$tmp/a" "$tmp/b"
printf '# Alpha SPINEDOC\n\nAlpha.\n' >"$tmp/a/x.md"
printf '# Bravo SPADOC\n\nBravo.\n' >"$tmp/b/y.md"
sub_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: tc\nappend-round: Sub expansion\nchildren:\n  - path: a/x.md\n    kind: amendment\n    lane: A\n  - path: b/y.md\n    kind: amendment\n    lane: D\n---\n# Sub expansion\n\n## Round 2 — Sub expansion\n\n- **step-03 — prereq** — shared.\n'
mk "$sub_art" >"$tmp/respSub.json"
cat >"$tmp/dispatch-sub.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q "ASSEMBLE N loose handoff"; then cat "$tmp/respSub.json"
else cat "$tmp/respSub.json"; fi
EOF
out2="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="bash $tmp/dispatch-sub.sh" \
  bin/wip bundle "$tmp/a/x.md" "$tmp/b/y.md" --yes 2>/dev/null)"
mapfile -t F2 < <(jq -r '.ok, .manifest, ([.children[].path]|join(","))' <<<"$out2")
assert_eq "true" "${F2[0]}" "subdir assemble ok"
assert_eq "$tmp/bundle.md" "${F2[1]}" "subdir manifest at common parent"
assert_eq "a/x.md,b/y.md" "${F2[2]}" "child paths keep subdir prefix"

# 3. --intake chains into the explode and fans out lead + 2 lane children.
mk_repo
rm -f "$tmp/bundle.md"
out3="$(RUN "$tmp/spine.md" "$tmp/spa.md" --yes --intake)"
mapfile -t F3 < <(jq -r '.ok, .intake.ok, .intake.kind, .intake.lead.ok, .intake.summary.applied' <<<"$out3")
assert_eq "true" "${F3[0]}" "intake-chain ok"
assert_eq "true" "${F3[1]}" "intake envelope ok"
assert_eq "bundle" "${F3[2]}" "intake kind bundle"
assert_eq "true" "${F3[3]}" "intake lead applied"
assert_eq "2" "${F3[4]}" "intake fanned out 2 children"
assert_grep "## Round 2 — Track expansion" "$roadmap" "round 2 added by explode"
assert_grep "### Lane A" "$roadmap" "Lane A heading"
assert_grep "### Lane D" "$roadmap" "Lane D heading"
assert_grep "## Cross-cuts (from bundle)" "$roadmap" "Cross-cuts section"
assert_grep "step-04 — Track A: core.document spine" "$roadmap" "step-04 in roadmap"
assert_grep "step-05 — Track D: SPA usability v1" "$roadmap" "step-05 in roadmap"

# 4. Fewer than two inputs → exit 2 (bundle-too-few-inputs).
set +e
out4="$(RUN "$tmp/spine.md" --yes)"
rc=$?
set -e
assert_eq "2" "$rc" "one input exit 2"
assert_eq "bundle-too-few-inputs" "$(jq -r '.error.kind' <<<"$out4")" "one input error kind"

# 5. Unreadable input → exit 2 (bundle-input-unreadable).
set +e
out5="$(RUN "$tmp/spine.md" "$tmp/nope.md" --yes)"
rc=$?
set -e
assert_eq "2" "$rc" "unreadable input exit 2"
assert_eq "bundle-input-unreadable" "$(jq -r '.error.kind' <<<"$out5")" "unreadable error kind"

# 6. --dry-run writes nothing.
mk_repo
rm -f "$tmp/bundle.md"
out6="$(RUN "$tmp/spine.md" "$tmp/spa.md" --yes --dry-run)"
mapfile -t F6 < <(jq -r '.ok, .dry_run, (.wrote|length)' <<<"$out6")
assert_eq "true" "${F6[0]}" "dry-run ok"
assert_eq "true" "${F6[1]}" "dry-run flag"
assert_eq "0" "${F6[2]}" "dry-run wrote nothing in envelope"
assert_absent "$tmp/bundle.md" "dry-run left no manifest on disk"

# 7. Nested-bundle child refused: a child declaring kind: bundle fails that
#    child in the chained explode (captured non-fatally) → aggregate ok:false.
mk_repo
rm -f "$tmp/bundle.md"
nested_art=$'---\nwip-kind: bundle\nlead-as: amendment\ntarget: tc\nappend-round: Nested\nchildren:\n  - path: spine.md\n    kind: bundle\n---\n# Nested\n\n## Round 2 — Nested\n\n- **step-03 — prereq** — body.\n'
mk "$nested_art" >"$tmp/respNested.json"
cat >"$tmp/dispatch-nested.sh" <<EOF
req="\$(cat)"
if printf '%s' "\$req" | grep -q "ASSEMBLE N loose handoff"; then cat "$tmp/respNested.json"
else cat "$tmp/respNested.json"; fi
EOF
set +e
out7="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="bash $tmp/dispatch-nested.sh" \
  bin/wip bundle "$tmp/spine.md" "$tmp/spa.md" --yes --intake 2>/dev/null)"
rc=$?
set -e
mapfile -t F7 < <(jq -r '.ok, ([.intake.children[]|select(.ok==false)|.result.error.kind][0])' <<<"$out7")
assert_eq "false" "${F7[0]}" "nested-bundle -> aggregate not ok"
assert_eq "nested-bundle" "${F7[1]}" "nested-bundle child error kind"

# 8. Bad --lead-as → exit 2 usage.
set +e
out8="$(RUN "$tmp/spine.md" "$tmp/spa.md" --lead-as bogus --yes)"
rc=$?
set -e
assert_eq "2" "$rc" "bad --lead-as exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out8")" "bad --lead-as kind usage"

# 9. `-o -` streams the assembled manifest to stdout (raw bytes, no envelope).
mk_repo
rm -f "$tmp/bundle.md"
out9="$(RUN "$tmp/spine.md" "$tmp/spa.md" --yes -o -)"
printf '%s\n' "$out9" >"$tmp/stdout9.md"
assert_eq "---" "$(head -1 "$tmp/stdout9.md")" "-o - emits raw front-matter, not JSON"
assert_grep "wip-kind: bundle" "$tmp/stdout9.md" "-o - manifest body to stdout"
assert_absent "$tmp/bundle.md" "-o - wrote no file"

test_summary
