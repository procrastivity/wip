#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="forge-transport"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# The transport seam (ADR-0018, step-02) is a standalone lib both Round 2 lanes
# build on. Source it directly and exercise it via env seams — never a real
# gh/glab, never a network.
# shellcheck source=lib/wip/wip-plumbing-forge-lib.bash
source lib/wip/wip-plumbing-forge-lib.bash

# --- detection: WIP_FORGE_CLI override (deterministic) ----------------------
assert_eq "gh" "$(WIP_FORGE_CLI=gh _wip_forge_detect)" "detect honors WIP_FORGE_CLI=gh"
assert_eq "glab" "$(WIP_FORGE_CLI=glab _wip_forge_detect)" "detect honors WIP_FORGE_CLI=glab"
assert_eq "" "$(WIP_FORGE_CLI='' _wip_forge_detect)" "set-but-empty WIP_FORGE_CLI forces none"

# --- detection: real command -v branch via PATH-controlled stubs ------------
stub="$(wip_mktemp)"
printf '#!/bin/sh\nexit 0\n' >"$stub/gh" && chmod +x "$stub/gh"
printf '#!/bin/sh\nexit 0\n' >"$stub/glab" && chmod +x "$stub/glab"
assert_eq "gh" "$(
  unset WIP_FORGE_CLI
  PATH="$stub" _wip_forge_detect
)" "detect prefers gh when both present"
rm -f "$stub/gh"
assert_eq "glab" "$(
  unset WIP_FORGE_CLI
  PATH="$stub" _wip_forge_detect
)" "detect falls back to glab"
rm -f "$stub/glab"
assert_eq "" "$(
  unset WIP_FORGE_CLI
  PATH="$stub" _wip_forge_detect
)" "detect echoes none when neither present"

# --- detection: config layer (arg 1) — env → config → probe (step-06) -------
# A non-empty configured pin (arg 1) is the NEW middle layer: it beats the
# binary probe and is authoritative as a value (D4 — honored without its binary,
# never re-validated against PATH). env still wins; an empty arg falls through.
cfg="$(wip_mktemp)"
printf '#!/bin/sh\nexit 0\n' >"$cfg/gh" && chmod +x "$cfg/gh"
# config wins over probe: only gh on PATH, but the pin selects glab (core fix).
assert_eq "glab" "$(
  unset WIP_FORGE_CLI
  PATH="$cfg" _wip_forge_detect "glab"
)" "config pin beats gh-wins probe (core bug fix)"
# env beats config: WIP_FORGE_CLI overrides the pin.
assert_eq "gh" "$(WIP_FORGE_CLI=gh _wip_forge_detect "glab")" "env WIP_FORGE_CLI beats config pin"
# set-but-empty env still forces none, even over a config pin (D3 precedence edge).
assert_eq "" "$(WIP_FORGE_CLI='' _wip_forge_detect "glab")" "set-but-empty WIP_FORGE_CLI forces none over config pin"
# empty config arg = "not configured" -> fall through to probe (D3 asymmetry).
printf '#!/bin/sh\nexit 0\n' >"$cfg/glab" && chmod +x "$cfg/glab"
assert_eq "gh" "$(
  unset WIP_FORGE_CLI
  PATH="$cfg" _wip_forge_detect ""
)" "empty config arg falls through to probe (gh-wins, D3)"
# pin honored without its binary present at all (D4 — no PATH revalidation).
nobin="$(wip_mktemp)"
assert_eq "glab" "$(
  unset WIP_FORGE_CLI
  PATH="$nobin" _wip_forge_detect "glab"
)" "config pin honored without its binary on PATH (D4)"

# --- status command resolution ---------------------------------------------
assert_eq "gh auth status" "$(
  unset WIP_FORGE_STATUS_CMD
  _wip_forge_status_cmd gh
)" "status cmd for gh"
assert_eq "glab auth status" "$(
  unset WIP_FORGE_STATUS_CMD
  _wip_forge_status_cmd glab
)" "status cmd for glab"
assert_eq "" "$(
  unset WIP_FORGE_STATUS_CMD
  _wip_forge_status_cmd ''
)" "status cmd empty for no cli"
assert_eq "echo probe" "$(WIP_FORGE_STATUS_CMD='echo probe' _wip_forge_status_cmd gh)" "WIP_FORGE_STATUS_CMD overrides"
assert_eq "echo probe" "$(WIP_FORGE_STATUS_CMD='echo probe' _wip_forge_status_cmd '')" "override works even with no cli"

# --- observe command resolution --------------------------------------------
assert_eq "gh pr view main --json state,mergedAt,url" \
  "$(
    unset WIP_FORGE_OBSERVE_CMD
    _wip_forge_observe_cmd gh main
  )" "observe cmd for gh"
assert_eq "glab mr view main --output json" \
  "$(
    unset WIP_FORGE_OBSERVE_CMD
    _wip_forge_observe_cmd glab main
  )" "observe cmd for glab"
assert_eq "" "$(
  unset WIP_FORGE_OBSERVE_CMD
  _wip_forge_observe_cmd '' main
)" "observe cmd empty for no cli"
assert_eq "cat fake.json" "$(WIP_FORGE_OBSERVE_CMD='cat fake.json' _wip_forge_observe_cmd gh main)" "WIP_FORGE_OBSERVE_CMD overrides"

# --- runner -----------------------------------------------------------------
assert_eq "hello" "$(_wip_forge_run 'printf hello')" "run captures stdout"
assert_eq "ok" "$(_wip_forge_run 'printf oops >&2; printf ok')" "run swallows stderr"

set +e
_wip_forge_run "" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "run with empty cmd returns 2"

set +e
_wip_forge_run 'exit 7' >/dev/null 2>&1
rc=$?
set -e
assert_eq "7" "$rc" "run propagates command exit status"

test_summary
