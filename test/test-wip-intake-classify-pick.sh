#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-classify-pick"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: foo
initiatives:
  - slug: foo
    title: Foo
    status: in-flight
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML

# Canonical-shape brief response. (Used for the happy path.)
cat >"$tmp/shape-brief.md" <<'MD'
# Payments

## Goal

Stand up a payments service.
MD

cat >"$tmp/resp-brief.json" <<JSON
{"choices":[{"message":{"role":"assistant","content":"$(awk 'BEGIN{ORS="\\n"}{gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); print}' "$tmp/shape-brief.md")"}}]}
JSON

# --- high-confidence classify: front-matter wip-kind sets it ---------------
cat >"$tmp/brief-fm.md" <<'MD'
---
wip-kind: brief
slug: payments
---
# Payments

## Goal

Stand up a payments service.
MD

out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-brief.json" \
  bin/wip intake "$tmp/brief-fm.md" --yes 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "high-confidence happy path ok"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "high-confidence kind=brief"
assert_eq "payments" "$(jq -r '.target' <<<"$out")" "brief target derived"

# --- low-confidence + --yes (no --kind) -> exit 4 kind-ambiguous -----------
cat >"$tmp/loose.md" <<'MD'
# Loose Notes

Some narrative without front-matter, sections, or directive keywords.
MD

set +e
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-brief.json" \
  bin/wip intake "$tmp/loose.md" --yes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "low confidence + --yes -> exit 4"
assert_eq "kind-ambiguous" "$(jq -r '.error.kind' <<<"$out")" "envelope kind=kind-ambiguous"
# classify payload is preserved
assert_eq "handoff" "$(jq -r '.error.classify.kind' <<<"$out")" "envelope carries classify guess"

# --- --kind override beats classify (override low->brief, runs LLM happy path) ---
out="$(WIP_ROOT="$tmp-new" mkdir -p "$tmp-new" &&
  cp "$tmp/.wip.yaml" "$tmp-new/.wip.yaml" &&
  cp "$tmp/loose.md" "$tmp-new/loose.md" &&
  WIP_ROOT="$tmp-new" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
    WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-brief.json" \
    bin/wip intake "$tmp-new/loose.md" --kind brief --yes 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "--kind override ok"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "--kind override kind=brief"
rm -rf "$tmp-new"

# --- classify-failed: file with no H1 -> classify exits 4 -------------------
printf 'no heading at all\n' >"$tmp/noheading.md"
set +e
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake "$tmp/noheading.md" --yes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "no-H1 -> exit 4"
assert_eq "classify-failed" "$(jq -r '.error.kind' <<<"$out")" "envelope kind=classify-failed"

test_summary
