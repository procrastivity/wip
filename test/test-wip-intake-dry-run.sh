#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-dry-run"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML

cat >"$tmp/half.md" <<'MD'
---
wip-kind: brief
slug: ephemeral
---
# Ephemeral

(narrative)
MD

shaped=$'# Ephemeral\n\n## Goal\n\nDo a thing.\n'
resp=$(jq -nc --arg c "$shaped" '{choices:[{message:{role:"assistant",content:$c}}]}')
printf '%s\n' "$resp" >"$tmp/resp.json"

# --- --dry-run does NOT write initiative ------------------------------------
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp.json" \
  bin/wip intake "$tmp/half.md" --yes --dry-run 2>/dev/null)"

assert_eq "true" "$(jq -r '.ok' <<<"$out")" "dry-run ok"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "dry_run flag set"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "dry-run kind"
assert_eq "ephemeral" "$(jq -r '.target' <<<"$out")" "dry-run target derived"
shaped_path="$(jq -r '.shaped_path' <<<"$out")"
assert_file "$shaped_path" "shaped artifact persisted on dry-run"
assert_absent "$tmp/.wip/initiatives/ephemeral/BRIEF.md" "no BRIEF written"
rm -f "$shaped_path"

# --- --output writes the shaped file to the given path ----------------------
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp.json" \
  bin/wip intake "$tmp/half.md" --yes --dry-run --output "$tmp/shaped.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "dry-run + --output ok"
assert_file "$tmp/shaped.md" "--output wrote the shaped file"
assert_grep "## Goal" "$tmp/shaped.md" "shaped file has Goal section"
assert_absent "$tmp/.wip/initiatives/ephemeral/BRIEF.md" "still no BRIEF after dry-run + --output"

# --- --output without --dry-run still writes the shaped file AND applies ----
tmp2="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp2/.wip.yaml"
cp "$tmp/half.md" "$tmp2/half.md"
out="$(WIP_ROOT="$tmp2" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp.json" \
  bin/wip intake "$tmp2/half.md" --yes --output "$tmp2/shaped.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "--output (no dry-run) ok"
assert_file "$tmp2/shaped.md" "--output wrote shaped file"
assert_file "$tmp2/.wip/initiatives/ephemeral/BRIEF.md" "BRIEF still written"
rm -rf "$tmp2"

test_summary
