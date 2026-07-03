#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="status"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(wip_mktemp)"
export WIP_NO_REGISTRY=1

wip_fixture_init "$tmp" --solo --title Demo --brief
wip_fixture_roadmap "$tmp" --deferred

out="$(WIP_ROOT="$tmp" bin/wip-plumbing status)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.initiative' <<<"$out")" "initiative"
assert_eq "in-flight" "$(jq -r '.status' <<<"$out")" "status field"
assert_eq "1" "$(jq -r '.round.n' <<<"$out")" "round 1"
assert_eq "One" "$(jq -r '.round.title' <<<"$out")" "round title"
assert_eq "step-02" "$(jq -r '.active_step.id' <<<"$out")" "active step id"
assert_eq "Second" "$(jq -r '.active_step.title' <<<"$out")" "active step title"
assert_eq "false" "$(jq -r '.active_step.shipped' <<<"$out")" "active step not shipped"
assert_eq "true" "$(jq -r '.solo_available' <<<"$out")" "solo available"
assert_eq "0" "$(jq -r '.signals | length' <<<"$out")" "no signals"

# Deferred items surface as informational, NOT-actionable context (BDS-17):
# present under .deferred, never leaking into active_step / lanes_in_flight.
assert_eq "1" "$(jq -r '.deferred | length' <<<"$out")" "1 deferred entry"
assert_eq "round-level-closeout-writes" "$(jq -r '.deferred[0].id' <<<"$out")" "deferred id"
assert_eq "Round-level closeout writes" "$(jq -r '.deferred[0].title' <<<"$out")" "deferred title"
assert_eq "step-02" "$(jq -r '.active_step.id' <<<"$out")" "deferred does not displace active_step"
assert_eq "0" "$(jq -r '.lanes_in_flight | length' <<<"$out")" "deferred does not leak into lanes_in_flight"

# --initiative <unknown> -> exit 3.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing status --initiative bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"

# Manifest active_step names a shipped step -> signal manifest-step-ahead.
tmp2="$(wip_mktemp)"
mkdir -p "$tmp2/.wip/initiatives/demo"
sed 's/active_step: step-02/active_step: step-01/' "$tmp/.wip.yaml" >"$tmp2/.wip.yaml"
cp "$tmp/.wip/initiatives/demo/roadmap.md" "$tmp2/.wip/initiatives/demo/roadmap.md"
out2="$(WIP_ROOT="$tmp2" bin/wip-plumbing status)"
assert_eq "1" "$(jq -r '.signals | map(select(. == "manifest-step-ahead")) | length' <<<"$out2")" "manifest-step-ahead signal"

# No current_initiative + no --initiative -> exit 3.
tmp3="$(wip_mktemp)"
cat >"$tmp3/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
initiatives: []
YAML
set +e
WIP_ROOT="$tmp3" bin/wip-plumbing status >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "no initiative -> exit 3"

# solo disabled -> solo_available false.
tmp4="$(wip_mktemp)"
mkdir -p "$tmp4/.wip/initiatives/demo"
sed 's/solo: { enabled: true }/solo: { enabled: false }/' "$tmp/.wip.yaml" >"$tmp4/.wip.yaml"
cp "$tmp/.wip/initiatives/demo/roadmap.md" "$tmp4/.wip/initiatives/demo/roadmap.md"
out4="$(WIP_ROOT="$tmp4" bin/wip-plumbing status)"
assert_eq "false" "$(jq -r '.solo_available' <<<"$out4")" "solo disabled -> false"

# dirty_wip_files is an array (empty on a non-git tree).
assert_eq "array" "$(jq -r '.dirty_wip_files | type' <<<"$out")" "dirty_wip_files array"

# Linear roadmap: active step lane is null, lanes_in_flight empty.
assert_eq "null" "$(jq -r '.active_step.lane' <<<"$out")" "linear: active step lane null"
assert_eq "0" "$(jq -r '.lanes_in_flight | length' <<<"$out")" "linear: no lanes in flight"

# ---- Lane disclosure (ADR-0010) ----
tmpL="$(wip_mktemp)"
wip_fixture_init "$tmpL" --title Demo --active-step step-13
cat >"$tmpL/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 4 — Track expansion

- **step-12 — F1 prereq** ✅ shipped 2026-06-01 — done.

### Lane A
- **step-13 — Track A part 1** — spine.
- **step-15 — Track A part 2** — provider.

### Lane D
- **step-14 — Track D** — SPA.
MD
outL="$(WIP_ROOT="$tmpL" bin/wip-plumbing status)"
assert_eq "A" "$(jq -r '.active_step.lane' <<<"$outL")" "active step discloses lane A"
assert_eq "2" "$(jq -r '.lanes_in_flight | length' <<<"$outL")" "two lanes in flight"
assert_eq "A" "$(jq -r '.lanes_in_flight[0].lane' <<<"$outL")" "lanes_in_flight[0] lane A (declared order)"
assert_eq "step-13" "$(jq -r '.lanes_in_flight[0].step' <<<"$outL")" "lane A next actionable step-13"
assert_eq "D" "$(jq -r '.lanes_in_flight[1].lane' <<<"$outL")" "lanes_in_flight[1] lane D"
assert_eq "step-14" "$(jq -r '.lanes_in_flight[1].step' <<<"$outL")" "lane D next actionable step-14"

# Only one lane has unshipped work -> lanes_in_flight empty.
cat >"$tmpL/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 4 — Track expansion

### Lane A
- **step-13 — Track A part 1** — spine.

### Lane D
- **step-14 — Track D** ✅ shipped 2026-06-10 — done.
MD
outL2="$(WIP_ROOT="$tmpL" bin/wip-plumbing status)"
assert_eq "0" "$(jq -r '.lanes_in_flight | length' <<<"$outL2")" "single in-flight lane -> empty"

# --- Solo liveness probe (--probe-solo, ADR-0014) -----------------------
# Isolated root: Solo declared + orchestration backend solo. The probe is fed
# from a file via the WIP_SOLO_STATUS_CMD seam (no real `solo` CLI dependency).
tmpP="$(wip_mktemp)"
wip_fixture_init "$tmpP" --solo --orchestration --title Demo
cat >"$tmpP/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-02 — Second** — current.
MD
printf '%s\n' '{"ok":true,"data":{"ready":true}}' >"$tmpP/solo-ready.json"
printf '%s\n' '{"ok":true,"data":{"ready":false}}' >"$tmpP/solo-down.json"

runp() { WIP_ROOT="$tmpP" bin/wip-plumbing status "$@"; }

# p1. No flag -> no probe; solo_reachable null, no solo-unreachable signal.
p1="$(runp)"
assert_eq "null" "$(jq -r '.solo_reachable' <<<"$p1")" "no flag -> solo_reachable null"
assert_eq "0" "$(jq -r '.signals | map(select(. == "solo-unreachable")) | length' <<<"$p1")" \
  "no flag -> no solo-unreachable signal"

# p2. --probe-solo, Solo READY -> reachable true, no signal.
p2="$(WIP_SOLO_STATUS_CMD="cat $tmpP/solo-ready.json" runp --probe-solo)"
assert_eq "true" "$(jq -r '.solo_reachable' <<<"$p2")" "probe ready -> solo_reachable true"
assert_eq "0" "$(jq -r '.signals | map(select(. == "solo-unreachable")) | length' <<<"$p2")" \
  "probe ready -> no signal"

# p3. --probe-solo, Solo DOWN (backend solo) -> reachable false + signal.
p3="$(WIP_SOLO_STATUS_CMD="cat $tmpP/solo-down.json" runp --probe-solo)"
assert_eq "false" "$(jq -r '.solo_reachable' <<<"$p3")" "probe down -> solo_reachable false"
assert_eq "1" "$(jq -r '.signals | map(select(. == "solo-unreachable")) | length' <<<"$p3")" \
  "probe down + backend solo -> solo-unreachable signal"

# p4. Backend is task -> Solo down is NOT actionable: reachable false, NO signal.
WIP_ROOT="$tmpP" yq -i '.features.orchestration.backend = "task"' "$tmpP/.wip.yaml"
p4="$(WIP_SOLO_STATUS_CMD="cat $tmpP/solo-down.json" runp --probe-solo)"
assert_eq "false" "$(jq -r '.solo_reachable' <<<"$p4")" "probe down (backend task) -> reachable false"
assert_eq "0" "$(jq -r '.signals | map(select(. == "solo-unreachable")) | length' <<<"$p4")" \
  "backend task -> no solo-unreachable signal (not actionable)"
WIP_ROOT="$tmpP" yq -i '.features.orchestration.backend = "solo"' "$tmpP/.wip.yaml"

# p5. --probe-solo but Solo not declared -> null (no probe), even with the seam.
WIP_ROOT="$tmpP" yq -i '.features.solo.enabled = false' "$tmpP/.wip.yaml"
p5="$(WIP_SOLO_STATUS_CMD="cat $tmpP/solo-down.json" runp --probe-solo)"
assert_eq "null" "$(jq -r '.solo_reachable' <<<"$p5")" "solo not declared -> reachable null (no probe)"
WIP_ROOT="$tmpP" yq -i '.features.solo.enabled = true' "$tmpP/.wip.yaml"

# p6. --probe-solo, Solo declared but CLI absent -> unreachable + signal.
nosolo_bin="$tmpP/nosolo-bin"
mkdir -p "$nosolo_bin"
for exe in bash jq yq git awk dirname head; do
  target="$(command -v "$exe")"
  ln -s "$target" "$nosolo_bin/$exe"
done
p6="$(PATH="$nosolo_bin" runp --probe-solo)"
assert_eq "false" "$(jq -r '.solo_reachable' <<<"$p6")" "probe with missing solo CLI -> solo_reachable false"
assert_eq "1" "$(jq -r '.signals | map(select(. == "solo-unreachable")) | length' <<<"$p6")" \
  "missing solo CLI + backend solo -> solo-unreachable signal"

# --- Forge liveness probe (--probe-forge, ADR-0018) ---------------------
# Mirrors the solo probe: opt-in --probe-forge sets a tri-state forge_reachable
# and emits forge-unreachable only when a forge is the declared transition owner
# (features.forge.enabled) and the probe says it's down. The transport seam's
# WIP_FORGE_STATUS_CMD feeds the probe (no real gh/glab); `true`/`false` stand in
# for an authed / unreachable forge by exit status.
tmpF="$(wip_mktemp)"
wip_fixture_init "$tmpF" --title Demo
cat >"$tmpF/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-02 — Second** — current.
MD
WIP_ROOT="$tmpF" yq -i '.features.forge.enabled = true' "$tmpF/.wip.yaml"

runf() { WIP_ROOT="$tmpF" bin/wip-plumbing status "$@"; }

# f0. forge_available echoes the declared key regardless of probing.
assert_eq "true" "$(jq -r '.forge_available' <<<"$(runf)")" "forge declared -> forge_available true"

# f1. No flag -> no probe; forge_reachable null, no forge-unreachable signal.
f1="$(runf)"
assert_eq "null" "$(jq -r '.forge_reachable' <<<"$f1")" "no flag -> forge_reachable null"
assert_eq "0" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f1")" \
  "no flag -> no forge-unreachable signal"

# f2. --probe-forge, forge UP (status cmd exits 0) -> reachable true, no signal.
f2="$(WIP_FORGE_STATUS_CMD="true" runf --probe-forge)"
assert_eq "true" "$(jq -r '.forge_reachable' <<<"$f2")" "probe up -> forge_reachable true"
assert_eq "0" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f2")" \
  "probe up -> no signal"

# f3. --probe-forge, forge DOWN (status cmd exits nonzero) -> reachable false + signal.
f3="$(WIP_FORGE_STATUS_CMD="false" runf --probe-forge)"
assert_eq "false" "$(jq -r '.forge_reachable' <<<"$f3")" "probe down -> forge_reachable false"
assert_eq "1" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f3")" \
  "probe down + forge declared -> forge-unreachable signal"

# f4. Forge not declared -> probe is NOT actionable: reachable null, no signal.
WIP_ROOT="$tmpF" yq -i '.features.forge.enabled = false' "$tmpF/.wip.yaml"
f4="$(WIP_FORGE_STATUS_CMD="false" runf --probe-forge)"
assert_eq "null" "$(jq -r '.forge_reachable' <<<"$f4")" "forge not declared -> reachable null (no probe)"
assert_eq "0" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f4")" \
  "forge not declared -> no forge-unreachable signal (not actionable)"
WIP_ROOT="$tmpF" yq -i '.features.forge.enabled = true' "$tmpF/.wip.yaml"

# f5. --probe-forge, forge declared but no gh/glab CLI -> unreachable + signal.
f5="$(WIP_FORGE_CLI='' runf --probe-forge)"
assert_eq "false" "$(jq -r '.forge_reachable' <<<"$f5")" "declared but no forge CLI -> forge_reachable false"
assert_eq "1" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f5")" \
  "missing forge CLI + forge declared -> forge-unreachable signal"

# f6. A committed .features.forge.backend pin must not break the probe path
# (step-06): status reads the pin from mj and passes it to _wip_forge_detect,
# but the liveness probe still keys off the resolved status cmd (WIP_FORGE_STATUS_CMD
# seam). The f2/f3 up/down assertions hold unchanged with a pin present.
WIP_ROOT="$tmpF" yq -i '.features.forge.backend = "glab"' "$tmpF/.wip.yaml"
f6up="$(WIP_FORGE_STATUS_CMD="true" runf --probe-forge)"
assert_eq "true" "$(jq -r '.forge_reachable' <<<"$f6up")" "pin present + probe up -> forge_reachable true"
f6down="$(WIP_FORGE_STATUS_CMD="false" runf --probe-forge)"
assert_eq "false" "$(jq -r '.forge_reachable' <<<"$f6down")" "pin present + probe down -> forge_reachable false"
assert_eq "1" "$(jq -r '.signals | map(select(. == "forge-unreachable")) | length' <<<"$f6down")" \
  "pin present + probe down -> forge-unreachable signal still emitted"
WIP_ROOT="$tmpF" yq -i 'del(.features.forge.backend)' "$tmpF/.wip.yaml"

# ---- Closeout hint (half-done-closeout, step-06) ------------------------
# active_step names a not-yet-shipped step whose workplan is already archived ->
# signals carries "half-done-closeout" (single-sourced with doctor's check). The
# rolling-context sidecar alone must NOT trigger it. (The no-archive baseline is
# already covered by the "no signals" assertion on the main fixture above.)
tmpH="$(wip_mktemp)"
wip_fixture_init "$tmpH" --title Demo
mkdir -p "$tmpH/.wip/initiatives/demo/archive"
cat >"$tmpH/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
- **step-03 — Third** — later.
MD

# Negative: only the rolling-context sidecar is archived -> no signal.
: >"$tmpH/.wip/initiatives/demo/archive/step-02-rolling-context.md"
outHneg="$(WIP_ROOT="$tmpH" bin/wip-plumbing status)"
assert_eq "0" "$(jq -r '.signals | map(select(. == "half-done-closeout")) | length' <<<"$outHneg")" \
  "closeout hint: sidecar-only archive -> no half-done-closeout signal"

# Positive: the workplan itself is archived for the unshipped active step.
: >"$tmpH/.wip/initiatives/demo/archive/step-02-second-workplan.md"
outHpos="$(WIP_ROOT="$tmpH" bin/wip-plumbing status)"
assert_eq "false" "$(jq -r '.active_step.shipped' <<<"$outHpos")" "closeout hint: active step still unshipped"
assert_eq "1" "$(jq -r '.signals | map(select(. == "half-done-closeout")) | length' <<<"$outHpos")" \
  "closeout hint: archived workplan for unshipped active step -> half-done-closeout signal"

test_summary
