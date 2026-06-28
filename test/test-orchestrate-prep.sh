#!/usr/bin/env bash
# test-orchestrate-prep — pin the `orchestrate prep` JSON shape + exit-code
# gate (ADR-0012). Deterministic; no LLM, no MCP, no network.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="orchestrate-prep"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(wip_mktemp)"
export WIP_NO_REGISTRY=1

wip_fixture_init "$tmp" --orchestration
mkdir -p "$tmp/.wip/initiatives/demo/workplans"
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
- **step-03 — MFA prompt** — slot.
MD

run() { WIP_ROOT="$tmp" bin/wip-plumbing orchestrate prep "$@"; }

# 1. Ready brief, workplan MISSING (not an error — Researcher produces it).
out="$(run)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.initiative' <<<"$out")" "initiative"
assert_eq "true" "$(jq -r '.orchestration.enabled' <<<"$out")" "orchestration.enabled"
assert_eq "solo" "$(jq -r '.orchestration.backend' <<<"$out")" "orchestration.backend"
assert_eq "step-02" "$(jq -r '.active_step.id' <<<"$out")" "active_step.id"
assert_eq "Refresh tokens" "$(jq -r '.active_step.title' <<<"$out")" "active_step.title"
assert_eq "false" "$(jq -r '.active_step.shipped' <<<"$out")" "active_step.shipped"
assert_eq "false" "$(jq -r '.workplan.exists' <<<"$out")" "missing workplan -> exists false"
assert_eq ".wip/initiatives/demo/workplans/step-02-refresh-tokens.md" \
  "$(jq -r '.workplan.path' <<<"$out")" "derived canonical workplan path"
assert_eq "[]" "$(jq -c '.signals' <<<"$out")" "no signals for unshipped step"

# Exit code on the happy path is 0.
set +e
run >/dev/null 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "ready brief exits 0"

# 2. Existing workplan is found by glob (<step-id>-*.md), exists: true.
touch "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md"
out2="$(run)"
assert_eq "true" "$(jq -r '.workplan.exists' <<<"$out2")" "existing workplan -> exists true"
assert_eq ".wip/initiatives/demo/workplans/step-02-refresh-tokens.md" \
  "$(jq -r '.workplan.path' <<<"$out2")" "globbed workplan path"

# 3. Seam: prep NEVER names a backend MCP tool / agent_tool_id (ADR-0007).
if ! grep -qE 'mcp__solo__|agent_tool_id' <<<"$out2"; then
  _WIP_PASS=$((_WIP_PASS + 1))
  printf '  ok   output names no backend MCP tool / agent_tool_id\n'
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  printf '  FAIL output leaked a backend tool name\n' >&2
fi

# 4. active-step-shipped signal when the active step is already shipped.
WIP_ROOT="$tmp" yq -i '(.initiatives[] | select(.slug == "demo") | .active_step) = "step-01"' "$tmp/.wip.yaml"
out3="$(run)"
assert_eq "true" "$(jq -r '.ok' <<<"$out3")" "shipped step still ok"
assert_eq "true" "$(jq -r '.active_step.shipped' <<<"$out3")" "active_step.shipped true"
assert_eq '["active-step-shipped"]' "$(jq -c '.signals' <<<"$out3")" "active-step-shipped signal"

# 5. orchestration-not-enabled -> exit 3.
WIP_ROOT="$tmp" yq -i '.features.orchestration.enabled = false' "$tmp/.wip.yaml"
set +e
out4="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "orchestration disabled exit 3"
assert_eq "orchestration-not-enabled" "$(jq -r '.error.kind' <<<"$out4")" "orchestration-not-enabled kind"
WIP_ROOT="$tmp" yq -i '.features.orchestration.enabled = true' "$tmp/.wip.yaml"

# 6. no-active-step -> exit 4.
WIP_ROOT="$tmp" yq -i 'del(.initiatives[] | select(.slug == "demo") | .active_step)' "$tmp/.wip.yaml"
set +e
out5="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "no active_step exit 4"
assert_eq "no-active-step" "$(jq -r '.error.kind' <<<"$out5")" "no-active-step kind"

# 7. step-not-in-roadmap -> exit 4 (active_step set to a non-roadmap step).
WIP_ROOT="$tmp" yq -i '(.initiatives[] | select(.slug == "demo") | .active_step) = "step-99"' "$tmp/.wip.yaml"
set +e
out6="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "non-roadmap active_step exit 4"
assert_eq "step-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out6")" "step-not-in-roadmap kind"

# 8. unknown-initiative (--initiative) -> exit 3.
set +e
out7="$(run --initiative nope 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"
assert_eq "unknown-initiative" "$(jq -r '.error.kind' <<<"$out7")" "unknown-initiative kind"

# 9. bad subcommand / args -> exit 2.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing orchestrate >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing subcommand exit 2"
set +e
run --bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "unknown flag exit 2"

# --- orchestrate backend (ADR-0013) -------------------------------------
# Isolated root with a roles/backends/ tree; neutralize CLAUDE_PLUGIN_ROOT so
# the real plugin dir is never picked up.
tmp2="$(wip_mktemp)"
mkdir -p "$tmp2/roles/backends"
printf 'SOLO BINDING\n' >"$tmp2/roles/backends/solo.md"
printf 'TASK BINDING\n' >"$tmp2/roles/backends/task.md"
cp "$tmp2/roles/backends/solo.md" "$tmp2/roles/backends/active.md"
cat >"$tmp2/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  orchestration: { enabled: true, backend: solo }
current_initiative: demo
initiatives: []
YAML

runb() { CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp2" bin/wip-plumbing orchestrate backend "$@"; }

# b1. show (no arg): current backend + available list + in-sync pointer.
b1="$(runb)"
assert_eq "true" "$(jq -r '.ok' <<<"$b1")" "backend show ok"
assert_eq "solo" "$(jq -r '.backend' <<<"$b1")" "backend show: current solo"
assert_eq '["solo","task"]' "$(jq -c '.available' <<<"$b1")" "backend show: available list"
assert_eq "true" "$(jq -r '.active_in_sync' <<<"$b1")" "backend show: active.md in sync"

# b2. switch to task: regenerates pointer + flips manifest.
b2="$(runb task)"
assert_eq "task" "$(jq -r '.backend' <<<"$b2")" "switch: backend task"
assert_eq "true" "$(jq -r '.active_regenerated' <<<"$b2")" "switch: active regenerated"
assert_eq ".wip.yaml" "$(jq -r '.manifest_updated' <<<"$b2")" "switch: manifest updated"
assert_cmp "$tmp2/roles/backends/task.md" "$tmp2/roles/backends/active.md" \
  "switch: active.md == task.md on disk"
assert_eq "task" "$(yq -r '.features.orchestration.backend' "$tmp2/.wip.yaml")" \
  "switch: manifest backend == task"

# b3. idempotent re-switch to task: no regen, no manifest write.
b3="$(runb task)"
assert_eq "false" "$(jq -r '.active_regenerated' <<<"$b3")" "re-switch: no regen"
assert_eq "null" "$(jq -r '.manifest_updated' <<<"$b3")" "re-switch: manifest noop"

# b4. switch back to solo: regen flips pointer again.
b4="$(runb solo)"
assert_eq "solo" "$(jq -r '.backend' <<<"$b4")" "switch back: backend solo"
assert_eq "true" "$(jq -r '.active_regenerated' <<<"$b4")" "switch back: regen"
assert_cmp "$tmp2/roles/backends/solo.md" "$tmp2/roles/backends/active.md" \
  "switch back: active.md == solo.md"

# b5. unknown backend -> exit 4.
set +e
b5="$(runb bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "unknown backend exit 4"
assert_eq "unknown-backend" "$(jq -r '.error.kind' <<<"$b5")" "unknown-backend kind"

# b6. reserved 'active' name -> exit 2.
set +e
runb active >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "reserved 'active' exit 2"

# b6b. backend names are identifiers, not paths.
set +e
runb ../README >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "path-like backend name exit 2"

# b7. --dry-run: reports the would-be regen, mutates nothing on disk.
b7="$(CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp2" bin/wip-plumbing --dry-run orchestrate backend task)"
assert_eq "true" "$(jq -r '.active_regenerated' <<<"$b7")" "dry-run: reports regen"
assert_cmp "$tmp2/roles/backends/solo.md" "$tmp2/roles/backends/active.md" \
  "dry-run: active.md unchanged on disk (still solo)"
assert_eq "solo" "$(yq -r '.features.orchestration.backend' "$tmp2/.wip.yaml")" \
  "dry-run: manifest backend unchanged"

# b8. no roles/backends/ reachable -> exit 4 no-roles-dir.
tmp3="$(wip_mktemp)"
cp "$tmp2/.wip.yaml" "$tmp3/.wip.yaml"
set +e
b8="$(CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp3" bin/wip-plumbing orchestrate backend task 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "no roles dir exit 4"
assert_eq "no-roles-dir" "$(jq -r '.error.kind' <<<"$b8")" "no-roles-dir kind"

test_summary
