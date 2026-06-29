#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-transport"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-06 (ADR-0019 §4): the transport adapter. Plumbing resolves a bind plan
# (issue + provider state) and the read/write shell-out seams, but never makes
# the call. Lib functions are exercised directly; the `tracker bind` verb through
# the CLI. WIP_LINEAR_{READ,WRITE}_CMD are the test seams.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
# shellcheck source=lib/wip/wip-plumbing-tracker-lib.bash
source lib/wip/wip-plumbing-tracker-lib.bash
# shellcheck source=lib/wip/wip-plumbing-tracker-transport-lib.bash
source lib/wip/wip-plumbing-tracker-transport-lib.bash
WIP=bin/wip-plumbing

# --- provider state mapping -------------------------------------------------
assert_eq "Todo" "$(_wip_tracker_provider_state linear todo)" "linear todo"
assert_eq "In Progress" "$(_wip_tracker_provider_state linear in-progress)" "linear in-progress"
assert_eq "In Review" "$(_wip_tracker_provider_state linear in-review)" "linear in-review"
assert_eq "Done" "$(_wip_tracker_provider_state linear 'done')" "linear done"
assert_eq "Canceled" "$(_wip_tracker_provider_state linear canceled)" "linear canceled"
assert_eq "" "$(_wip_tracker_provider_state linear bogus)" "unknown semantic -> empty"
assert_eq "in-review" "$(_wip_tracker_provider_state github in-review)" "unknown backend -> passthrough"

# --- read/write command resolution (env seams) ------------------------------
assert_eq "" "$(_wip_tracker_transport_read_cmd linear)" "read cmd empty by default (MCP path)"
assert_eq "" "$(_wip_tracker_transport_write_cmd linear)" "write cmd empty by default"
assert_eq "rd" "$(WIP_LINEAR_READ_CMD=rd _wip_tracker_transport_read_cmd linear)" "WIP_LINEAR_READ_CMD overrides"
assert_eq "wr" "$(WIP_LINEAR_WRITE_CMD=wr _wip_tracker_transport_write_cmd linear)" "WIP_LINEAR_WRITE_CMD overrides"
assert_eq "" "$(WIP_LINEAR_READ_CMD=rd _wip_tracker_transport_read_cmd github)" "non-linear backend -> no cmd"

# --- bind plan via the verb -------------------------------------------------
tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_map: { step-01: BDS-90, step-02: BDS-91 }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x.\n' \
  >"$tmp/.wip/initiatives/demo/roadmap.md"
# step-01 cached in-review; step-02 has NO cache entry.
_wip_tracker_cache_set "$tmp" "demo/step-01" "in-review" "ship" "2026-06-28" >/dev/null

b="$(WIP_ROOT="$tmp" $WIP tracker bind)"
assert_eq "linear" "$(jq -r '.backend' <<<"$b")" "bind: backend echo"
assert_eq "mcp" "$(jq -r '.transport' <<<"$b")" "bind: transport mcp by default"
assert_eq "2" "$(jq -r '.bindings | length' <<<"$b")" "bind: one plan per mapped node"
assert_eq "BDS-90" "$(jq -r '.bindings[] | select(.node=="demo/step-01") | .issue' <<<"$b")" "bind: issue from mirror"
assert_eq "In Review" "$(jq -r '.bindings[] | select(.node=="demo/step-01") | .target_state' <<<"$b")" \
  "bind: cached in-review -> In Review target"
# Mapped node with no cache entry: semantic_state + target_state null.
assert_eq "null" "$(jq -r '.bindings[] | select(.node=="demo/step-02") | .semantic_state' <<<"$b")" \
  "bind: unmapped-in-cache node -> semantic null"
assert_eq "null" "$(jq -r '.bindings[] | select(.node=="demo/step-02") | .target_state' <<<"$b")" \
  "bind: no cache -> target null"

# --node filters to a single binding.
bn="$(WIP_ROOT="$tmp" $WIP tracker bind --node step-01)"
assert_eq "1" "$(jq -r '.bindings | length' <<<"$bn")" "bind --node filters to one"
assert_eq "demo/step-01" "$(jq -r '.bindings[0].node' <<<"$bn")" "bind --node selects the right node"

# A wired write seam flips transport to cli (BDS-23 territory).
bc="$(WIP_ROOT="$tmp" WIP_LINEAR_WRITE_CMD='true' $WIP tracker bind)"
assert_eq "cli" "$(jq -r '.transport' <<<"$bc")" "wired write seam -> transport cli"

# --- error envelopes --------------------------------------------------------
set +e
WIP_ROOT="$tmp" $WIP tracker bind --initiative nope >/dev/null 2>&1
assert_eq "3" "$?" "unknown initiative -> exit 3"
WIP_ROOT="$tmp" $WIP tracker bind --node >/dev/null 2>&1
assert_eq "2" "$?" "--node without arg -> exit 2"
set -e

test_summary
