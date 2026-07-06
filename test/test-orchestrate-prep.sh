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

# 8b. Duo backend reachability gate (ADR-0025 §4): backend=duo hard-errors at
# preflight when Duo is unreachable, and proceeds when reachable. WIP_DUO_PROBE_CMD
# is the test seam (no real `duo` dependency). Restore the enabled + active_step
# state mutated by tests 5-7.
WIP_ROOT="$tmp" yq -i '.features.orchestration.enabled = true' "$tmp/.wip.yaml"
WIP_ROOT="$tmp" yq -i '.features.orchestration.backend = "duo"' "$tmp/.wip.yaml"
WIP_ROOT="$tmp" yq -i '(.initiatives[] | select(.slug == "demo") | .active_step) = "step-02"' "$tmp/.wip.yaml"

# Reachable: probe returns a resolved project id -> normal brief, exit 0.
# (jq -n emits valid JSON that survives the `bash -c "$probe"` re-parse.)
export WIP_DUO_PROBE_CMD='jq -n "{project_id:15}"'
out_duo_ok="$(run)"
assert_eq "true" "$(jq -r '.ok' <<<"$out_duo_ok")" "duo reachable -> ok brief"
assert_eq "duo" "$(jq -r '.orchestration.backend' <<<"$out_duo_ok")" "duo reachable -> backend duo"

# Unreachable: probe returns no project id -> exit 3 backend-unreachable.
export WIP_DUO_PROBE_CMD='jq -n "{}"'
set +e
out_duo_down="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "duo unreachable -> exit 3"
assert_eq "backend-unreachable" "$(jq -r '.error.kind' <<<"$out_duo_down")" "duo unreachable kind"

# Probe command itself fails (Duo not installed / not answering) -> exit 3 too.
export WIP_DUO_PROBE_CMD='false'
set +e
run >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "duo probe failure -> exit 3"

unset WIP_DUO_PROBE_CMD
# Reset backend to solo for the remaining shared-fixture cases.
WIP_ROOT="$tmp" yq -i '.features.orchestration.backend = "solo"' "$tmp/.wip.yaml"

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

# b1. show (no arg): current backend + available list. The show-path
# `active_in_sync` assertions migrated to test-orchestrate-backend.sh (step-04
# C5), which owns the backend verb's drift-oracle coverage.
b1="$(runb)"
assert_eq "true" "$(jq -r '.ok' <<<"$b1")" "backend show ok"
assert_eq "solo" "$(jq -r '.backend' <<<"$b1")" "backend show: current solo"
assert_eq '["solo","task"]' "$(jq -c '.available' <<<"$b1")" "backend show: available list"

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

# --- orchestrate backend, VENDORED path (ADR-0020 / step-04) -------------
# A flattened (source: vendored) consumer has NO local roles/ or active.md —
# its four self-contained .claude/agents/wip/<role>.md files ARE the agents — so
# a backend switch RE-RENDERS those four via wip_flatten_render instead of
# regenerating an active.md pointer (D-04.1/.6). Fixture is honest: it seeds the
# files through the same shipped install path (`setup agents`) and points the
# renderer at the repo's real roles/ via the WIP_ROLES_DIR seam (mirrors
# test-setup.sh). b1-b8 above (a source-less / plugin manifest) still exercise
# the unchanged active.md path — the regression guard for that path.

# Non-equality assertion (helpers.sh has assert_cmp for equality only).
assert_differ() {
  local a="$1" b="$2" msg="${3:-assert_differ}"
  if ! cmp -s -- "$a" "$b"; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       unexpectedly identical: %s == %s\n' "$msg" "$a" "$b" >&2
  fi
}

# Reference render: the pure renderer's bytes for <role> <backend>, produced in
# a subshell so sourcing the libs never leaks state into the test's main shell
# (mirrors test-flatten-render.sh's source seam).
render_ref() (
  export WIP_LIB="$PWD/lib/wip" WIP_TEMPLATES_DIR="$PWD/templates" WIP_ROLES_DIR="$PWD/roles"
  # shellcheck source=lib/wip/wip-plumbing-lib.bash
  source "$WIP_LIB/wip-plumbing-lib.bash"
  # shellcheck source=lib/wip/wip-plumbing-flatten-lib.bash
  source "$WIP_LIB/wip-plumbing-flatten-lib.bash"
  wip_flatten_render "$1" "$2"
)

tmpv="$(wip_mktemp)"
cat >"$tmpv/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  orchestration: { enabled: true, backend: solo, source: vendored }
current_initiative: demo
initiatives: []
YAML

# Seed the four agent files honestly via the shipped vendored install path. With
# backend: solo this renders the solo-backend agents — the install bytes the
# round-trip below must reproduce.
WIP_ROLES_DIR="$PWD/roles" WIP_ROOT="$tmpv" bin/wip-plumbing setup agents >/dev/null 2>&1

vroles=(orchestrator coordinator researcher builder)

# Snapshot the install (solo) bytes and pre-render the task-backend reference,
# both outside the consumer tree so they can't be mistaken for consumer state.
snap="$(wip_mktemp)"
ref="$(wip_mktemp)"
for role in "${vroles[@]}"; do
  cp "$tmpv/.claude/agents/wip/$role.md" "$snap/$role.md"
  render_ref "$role" task >"$ref/$role.md"
done

runv() { WIP_ROLES_DIR="$PWD/roles" CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv" bin/wip-plumbing orchestrate backend "$@"; }

# v1. Switch to task: flips manifest, re-flattens the four files (no active.md).
v1="$(runv task)"
assert_eq "task" "$(jq -r '.backend' <<<"$v1")" "vendored switch: backend task"
assert_eq "vendored" "$(jq -r '.source' <<<"$v1")" "vendored switch: source vendored"
assert_eq ".wip.yaml" "$(jq -r '.manifest_updated' <<<"$v1")" "vendored switch: manifest updated"
assert_eq '[".claude/agents/wip/orchestrator.md",".claude/agents/wip/coordinator.md",".claude/agents/wip/researcher.md",".claude/agents/wip/builder.md"]' \
  "$(jq -c '.reflattened' <<<"$v1")" "vendored switch: reflattened lists the four paths"
# No active.md mechanism fired → the JSON carries no active_regenerated key.
assert_eq "false" "$(jq -r 'has("active_regenerated")' <<<"$v1")" "vendored switch: no active_regenerated key"
assert_eq "task" "$(yq -r '.features.orchestration.backend' "$tmpv/.wip.yaml")" "vendored switch: manifest backend == task"
for role in "${vroles[@]}"; do
  assert_cmp "$ref/$role.md" "$tmpv/.claude/agents/wip/$role.md" \
    "vendored switch: $role byte-equal to wip_flatten_render $role task"
  assert_differ "$snap/$role.md" "$tmpv/.claude/agents/wip/$role.md" \
    "vendored switch: $role differs from pre-switch (solo) content"
done
# No active.md / roles/ ever created in the consumer tree.
assert_absent "$tmpv/roles" "vendored switch: no roles/ created in consumer"
assert_absent "$tmpv/.claude/agents/wip/active.md" "vendored switch: no active.md created"

# v1b. No-Solo-token (no-leak) gate on the now-task-backend INSTALLED files
# (BRIEF AC: "Switch re-flattens … passes the no-Solo-token assertion"). The
# step-04 block above proves the task files DIFFER from solo (v1, line 282) but
# never that they name ZERO Solo tokens — that single assertion is step-05's.
# FORBIDDEN is the Solo-specific token set, kept in sync VERBATIM with
# test/test-roles-backend-seam.sh:38 (mirrored in test-flatten-render.sh:29).
FORBIDDEN='mcp__solo|solo_process_id|agent_tool_id|spawn_process|scratchpad|todo_create|todo_list|whoami|list_agent_tools|mcp-cli|kv_set|kv_get|timer_set|timer_fire_when_idle|rename_process|wait_for_bound_port|kind="agent"|kind=\\"agent\\"'
# task.md's own benign FORBIDDEN hit is the bare word `whoami` in PROSE ("There
# is no whoami ..."), NOT a Solo-tool reference — so the no-leak shape is the
# hard mcp__solo__ absence PLUS render_hits == src_hits, NOT a naive full
# FORBIDDEN grep (mirrors test-flatten-render.sh:144-148, applied on-disk).
task_src_hits="$(grep -Eo -- "$FORBIDDEN" roles/backends/task.md 2>/dev/null | LC_ALL=C sort -u | tr '\n' ' ' || true)"
for role in "${vroles[@]}"; do
  installed="$tmpv/.claude/agents/wip/$role.md"
  assert_not_grep 'mcp__solo__' "$installed" \
    "vendored switch no-leak: $role install names no Solo MCP tool"
  inst_hits="$(grep -Eo -- "$FORBIDDEN" "$installed" 2>/dev/null | LC_ALL=C sort -u | tr '\n' ' ' || true)"
  assert_eq "$task_src_hits" "$inst_hits" \
    "vendored switch no-leak: $role install leaks no Solo token beyond task.md's own prose"
done

# v1c. `setup agents --check` is CLEAN after the switch — proves --check is
# backend-aware: it re-renders for the manifest's now-`task` backend and matches
# the installed bytes (exit 0, drift:[]). The --check flag exists as of Task 1.
set +e
vchk="$(WIP_ROLES_DIR="$PWD/roles" CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv" bin/wip-plumbing setup agents --check)"
rc=$?
set -e
assert_eq "0" "$rc" "vendored switch --check: clean exit 0 for task backend"
assert_eq "[]" "$(jq -c '.drift' <<<"$vchk")" "vendored switch --check: drift empty for task backend"

# v2. Round-trip back to solo reproduces the original install bytes exactly.
v2="$(runv solo)"
assert_eq "solo" "$(jq -r '.backend' <<<"$v2")" "vendored round-trip: backend solo"
for role in "${vroles[@]}"; do
  assert_cmp "$snap/$role.md" "$tmpv/.claude/agents/wip/$role.md" \
    "vendored round-trip: $role reproduces install bytes"
done

# v3. Idempotent re-switch to the current backend: nothing re-flattened, manifest noop.
v3="$(runv solo)"
assert_eq "[]" "$(jq -c '.reflattened' <<<"$v3")" "vendored idempotent: reflattened empty"
assert_eq "null" "$(jq -r '.manifest_updated' <<<"$v3")" "vendored idempotent: manifest noop"

# v4. --dry-run switch: reports would-be re-renders, mutates no file and no manifest.
v4="$(WIP_ROLES_DIR="$PWD/roles" CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv" bin/wip-plumbing --dry-run orchestrate backend task)"
assert_eq "4" "$(jq -r '.reflattened | length' <<<"$v4")" "vendored dry-run: reports four would-be re-renders"
assert_eq "solo" "$(yq -r '.features.orchestration.backend' "$tmpv/.wip.yaml")" "vendored dry-run: manifest backend unchanged"
for role in "${vroles[@]}"; do
  assert_cmp "$snap/$role.md" "$tmpv/.claude/agents/wip/$role.md" \
    "vendored dry-run: $role unchanged on disk (still solo)"
done

# v5. roles/ genuinely unreachable -> exit 4 no-roles-dir, with NO partial write.
# The renderer self-locates roles/ from its own install tree ($WIP_LIB/../../roles,
# mirroring the templates/ seam), so clearing WIP_ROLES_DIR / $root/roles /
# CLAUDE_PLUGIN_ROOT alone is NOT enough — a real install always ships roles/ next
# to lib/. To exercise the clean-failure path we point WIP_LIB at a lib-only copy
# whose sibling roles/ does not exist (a truly broken/partial install).
tmpv2="$(wip_mktemp)"
cp "$tmpv/.wip.yaml" "$tmpv2/.wip.yaml"
brokenlib="$(wip_mktemp)"
mkdir -p "$brokenlib/lib"
cp -R lib/wip "$brokenlib/lib/wip" # no sibling roles/ → self-locating seam misses
set +e
v5="$(env -u WIP_ROLES_DIR WIP_TEMPLATES_DIR="$PWD/templates" CLAUDE_PLUGIN_ROOT="" \
  WIP_LIB="$brokenlib/lib/wip" WIP_ROOT="$tmpv2" bin/wip-plumbing orchestrate backend task 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "vendored roles-unreachable exit 4"
assert_eq "no-roles-dir" "$(jq -r '.error.kind' <<<"$v5")" "vendored roles-unreachable: no-roles-dir kind"
assert_absent "$tmpv2/.claude" "vendored roles-unreachable: no partial write"

# v6. Self-locating install seam: a vendored consumer with EVERY roles env seam
# cleared (no WIP_ROLES_DIR, no $root/roles, empty CLAUDE_PLUGIN_ROOT) still
# re-flattens successfully, because the renderer finds roles/ next to its own
# lib/ via $WIP_LIB/../../roles — the same self-location templates/ already uses.
# Regression guard for the direct-invocation / no-CLAUDE_PLUGIN_ROOT case
# (running bin/wip-plumbing by absolute path from a foreign repo).
tmpv6="$(wip_mktemp)"
cat >"$tmpv6/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  orchestration: { enabled: true, backend: solo, source: vendored }
current_initiative: demo
initiatives: []
YAML
# Seed the vendored install honestly (roles self-located; no env seams needed).
env -u WIP_ROLES_DIR CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv6" bin/wip-plumbing setup agents >/dev/null 2>&1
set +e
v6="$(env -u WIP_ROLES_DIR CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv6" bin/wip-plumbing orchestrate backend task 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "self-locating roles: vendored switch succeeds with no env seams"
assert_eq "task" "$(jq -r '.backend' <<<"$v6")" "self-locating roles: backend switched to task"
for role in "${vroles[@]}"; do
  assert_cmp "$ref/$role.md" "$tmpv6/.claude/agents/wip/$role.md" \
    "self-locating roles: $role byte-equal to reference task render"
done

test_summary
