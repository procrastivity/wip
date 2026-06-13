#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/.wip/initiatives/demo"

cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  lds: { enabled: true, root: engineering }
initiatives:
  - slug: demo
    status: in-flight
YAML

# Drift: lds enabled but engineering/.lds-manifest.yaml missing.
set +e
out="$(WIP_ROOT="$tmp" bin/wip-plumbing doctor)"
rc=$?
set -e
assert_eq "4" "$rc" "doctor exits 4 on drift"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "doctor ok=false on drift"
assert_eq "1" "$(jq -r '.drift_count' <<<"$out")" "one drift (lds missing)"
assert_eq "declared-but-missing" "$(jq -r '.checks[]|select(.name=="lds").status' <<<"$out")" "lds check status"

# Heal it: add the sentinel.
mkdir -p "$tmp/engineering"
: >"$tmp/engineering/.lds-manifest.yaml"
set +e
out2="$(WIP_ROOT="$tmp" bin/wip-plumbing doctor)"
rc2=$?
set -e
assert_eq "0" "$rc2" "doctor exits 0 when healthy"
assert_eq "0" "$(jq -r '.drift_count' <<<"$out2")" "no drift when sentinel present"

# Unregistered initiative dir is drift.
mkdir -p "$tmp/.wip/initiatives/stray"
set +e
out3="$(WIP_ROOT="$tmp" bin/wip-plumbing doctor)"
rc3=$?
set -e
assert_eq "4" "$rc3" "doctor exits 4 on unregistered initiative"
assert_eq "unregistered" "$(jq -r '.checks[]|select(.slug=="stray").status' <<<"$out3")" "stray unregistered"

test_summary
