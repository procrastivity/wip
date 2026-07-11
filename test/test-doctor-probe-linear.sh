#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-probe-linear"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-07 (Lane A, ADR-0019 §6): doctor --probe-linear — a READ-ONLY live drift
# probe mirroring --probe-solo/--probe-forge. Compares the tracker's reported
# state (via the WIP_LINEAR_READ_CMD seam, invoked `<cmd> <issue>`) to wip's
# expected (cached → provider) state. Mismatch = drift (exit 4); a silent tracker
# or no wired transport is non-actionable (never fails doctor).

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
WIP=bin/wip-plumbing

# Build a fixture whose ONLY possible drift is the live probe: tracker_map agrees
# with the roadmap (no mirror drift), step unshipped (no closeout drift), cache
# seeded in-review. A read stub maps BDS-90 -> $STUB_STATE.
mkfx() {
  local dir="$1"
  mkdir -p "$dir/.wip/initiatives/demo"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_map: { step-01: BDS-90 }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
  printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x. [tracker: BDS-90]\n' \
    >"$dir/.wip/initiatives/demo/roadmap.md"
  _wip_tracker_cache_set "$dir" "demo/step-01" "in-review" "ship" "2026-06-28" >/dev/null
  cat >"$dir/read.sh" <<'SH'
#!/bin/sh
[ "$1" = "BDS-90" ] && printf '%s' "${STUB_STATE:-}"
SH
  chmod +x "$dir/read.sh"
}
rc_of() { # rc_of <dir> [env...]; runs doctor --probe-linear, echoes exit code
  local dir="$1"
  shift
  set +e
  env "$@" WIP_ROOT="$dir" "$WIP" doctor --probe-linear >/dev/null 2>&1
  local rc=$?
  set -e
  printf '%s' "$rc"
}
checks_of() { # checks_of <dir> [env...]; echoes the JSON checks (doctor may exit 4)
  local dir="$1"
  shift
  env "$@" WIP_ROOT="$dir" "$WIP" doctor --probe-linear 2>/dev/null || true
}

# --- baseline: no flag -> probe never runs ----------------------------------
tmp="$(wip_mktemp)"
mkfx "$tmp"
set +e
WIP_ROOT="$tmp" $WIP doctor >/dev/null 2>&1
assert_eq "0" "$?" "no --probe-linear -> clean (probe inert)"
set -e

# --- tracker agrees -> no drift, exit 0 -------------------------------------
t1="$(wip_mktemp)"
mkfx "$t1"
assert_eq "0" "$(rc_of "$t1" STUB_STATE="In Review" WIP_LINEAR_READ_CMD="$t1/read.sh")" \
  "tracker agrees (In Review) -> exit 0"
assert_eq "0" "$(checks_of "$t1" STUB_STATE="In Review" WIP_LINEAR_READ_CMD="$t1/read.sh" |
  jq '[.checks[] | select(.kind=="tracker-probe")] | length')" \
  "tracker agrees -> no tracker-probe check"

# --- tracker drifts -> tracker-state-drift, exit 4 --------------------------
t2="$(wip_mktemp)"
mkfx "$t2"
assert_eq "4" "$(rc_of "$t2" STUB_STATE="Todo" WIP_LINEAR_READ_CMD="$t2/read.sh")" \
  "tracker drifts (Todo vs In Review) -> exit 4"
d="$(checks_of "$t2" STUB_STATE="Todo" WIP_LINEAR_READ_CMD="$t2/read.sh" | jq -c '.checks[] | select(.kind=="tracker-probe")')"
assert_eq "tracker-state-drift" "$(jq -r '.status' <<<"$d")" "drift status"
assert_eq "In Review" "$(jq -r '.expected' <<<"$d")" "drift expected = wip cached state"
assert_eq "Todo" "$(jq -r '.actual' <<<"$d")" "drift actual = tracker state"
assert_eq "BDS-90" "$(jq -r '.issue' <<<"$d")" "drift names the issue"

# --- canonical --probe-tracker flag routes to the same probe (ADR-0026) -----
# The deprecated --probe-linear alias is exercised above via the helpers; here we
# assert the canonical flag drives the identical probe (drift -> exit 4).
set +e
env STUB_STATE="Todo" WIP_LINEAR_READ_CMD="$t2/read.sh" WIP_ROOT="$t2" "$WIP" doctor --probe-tracker >/dev/null 2>&1
rc_tracker=$?
set -e
assert_eq "4" "$rc_tracker" "--probe-tracker drives the same probe (drift -> exit 4)"
dt="$(env STUB_STATE="Todo" WIP_LINEAR_READ_CMD="$t2/read.sh" WIP_ROOT="$t2" "$WIP" doctor --probe-tracker 2>/dev/null | jq -c '.checks[] | select(.kind=="tracker-probe")' || true)"
assert_eq "tracker-state-drift" "$(jq -r '.status' <<<"$dt")" "--probe-tracker: same tracker-state-drift status"

# --- tracker silent (empty read) -> non-actionable, exit 0 ------------------
t3="$(wip_mktemp)"
mkfx "$t3"
assert_eq "0" "$(rc_of "$t3" STUB_STATE="" WIP_LINEAR_READ_CMD="$t3/read.sh")" \
  "tracker silent -> exit 0 (down tracker never fails doctor)"

# --- no transport wired -> informational unavailable note, exit 0 -----------
t4="$(wip_mktemp)"
mkfx "$t4"
assert_eq "0" "$(rc_of "$t4")" "no read transport -> exit 0"
assert_eq "unavailable" "$(checks_of "$t4" | jq -r '.checks[] | select(.kind=="tracker-probe") | .probe')" \
  "no transport -> probe:unavailable note"

# --- issue-tracker disabled -> probe inert even with the flag ---------------
t5="$(wip_mktemp)"
mkfx "$t5"
WIP_ROOT="$t5" yq -i 'del(.features."issue-tracker")' "$t5/.wip.yaml"
assert_eq "0" "$(checks_of "$t5" STUB_STATE="Todo" WIP_LINEAR_READ_CMD="$t5/read.sh" |
  jq '[.checks[] | select(.kind=="tracker-probe")] | length')" \
  "issue-tracker disabled -> no probe check at all"

test_summary
