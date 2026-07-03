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
# Original fixture has no agent_tier_policy -> no detail key (backward-compatible).
assert_eq "null" "$(jq -r '.features[]|select(.name=="solo")|.detail // "null"' <<<"$out")" "solo detail absent when no policy"

# --- detail echo: agent_tier_policy surfaced under the solo feature (pure config read) ---
# Case A: both force_tier + fallback_tool present -> both echoed under solo detail.
tmpA="$(mktemp -d)"
cat >"$tmpA/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  solo:
    enabled: true
    agent_tier_policy:
      force_tier: large
      fallback_tool: Claude
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-01
    brief: .wip/initiatives/demo/brief.md
YAML

outA="$(WIP_ROOT="$tmpA" bin/wip-plumbing detect)"
rm -rf "$tmpA"
assert_eq "large" "$(jq -r '.features[]|select(.name=="solo").detail.force_tier' <<<"$outA")" "solo detail force_tier"
assert_eq "Claude" "$(jq -r '.features[]|select(.name=="solo").detail.fallback_tool' <<<"$outA")" "solo detail fallback_tool"

# Case B: fallback_tool absent -> force_tier echoed, fallback_tool gracefully omitted (no error).
tmpB="$(mktemp -d)"
cat >"$tmpB/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  solo:
    enabled: true
    agent_tier_policy:
      force_tier: large
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-01
    brief: .wip/initiatives/demo/brief.md
YAML

outB="$(WIP_ROOT="$tmpB" bin/wip-plumbing detect)"
rm -rf "$tmpB"
assert_eq "large" "$(jq -r '.features[]|select(.name=="solo").detail.force_tier' <<<"$outB")" "solo detail force_tier (no fallback)"
assert_eq "null" "$(jq -r '.features[]|select(.name=="solo")|.detail.fallback_tool // "null"' <<<"$outB")" "solo detail fallback_tool omitted"

# --- detail echo: forge backend surfaced under the forge feature (pure config read, D6) ---
# Case C: pinned forge backend -> backend echoed under forge detail.
tmpC="$(mktemp -d)"
cat >"$tmpC/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  forge:
    enabled: true
    backend: glab
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-01
    brief: .wip/initiatives/demo/brief.md
YAML

outC="$(WIP_ROOT="$tmpC" bin/wip-plumbing detect)"
rm -rf "$tmpC"
assert_eq "glab" "$(jq -r '.features[]|select(.name=="forge").detail.backend' <<<"$outC")" "forge detail backend (pinned)"

# Case D: unpinned forge -> no backend -> detail omitted (backward-compatible).
tmpD="$(mktemp -d)"
cat >"$tmpD/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  forge:
    enabled: true
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-01
    brief: .wip/initiatives/demo/brief.md
YAML

outD="$(WIP_ROOT="$tmpD" bin/wip-plumbing detect)"
rm -rf "$tmpD"
assert_eq "null" "$(jq -r '.features[]|select(.name=="forge")|.detail // "null"' <<<"$outD")" "forge detail omitted (unpinned)"

test_summary
