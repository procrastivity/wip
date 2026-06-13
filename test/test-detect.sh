#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="detect"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/.wip/initiatives/demo"

cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  solo: { enabled: true }
  changelog: { enabled: true }
  lds: { enabled: true, root: engineering }
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-01
    brief: .wip/initiatives/demo/brief.md
YAML

# changelog sentinel present -> active; lds manifest absent -> declared-but-missing.
: >"$tmp/CHANGELOG.md"

out="$(WIP_ROOT="$tmp" bin/wip-plumbing detect)"

assert_eq "true" "$(jq -r '.ok' <<<"$out")" "detect ok"
assert_eq "demo" "$(jq -r '.current_initiative' <<<"$out")" "current_initiative"
assert_eq "true" "$(jq -r '.features[]|select(.name=="solo").active' <<<"$out")" "solo active (no sentinel)"
assert_eq "true" "$(jq -r '.features[]|select(.name=="changelog").active' <<<"$out")" "changelog active (sentinel present)"
assert_eq "false" "$(jq -r '.features[]|select(.name=="lds").active' <<<"$out")" "lds inactive (sentinel missing)"
assert_eq "declared-but-missing" "$(jq -r '.features[]|select(.name=="lds").drift' <<<"$out")" "lds drift"
assert_eq "1" "$(jq -r '[.initiatives[]]|length' <<<"$out")" "one initiative"

test_summary
