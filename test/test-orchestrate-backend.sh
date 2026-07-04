#!/usr/bin/env bash
# test-orchestrate-backend — pin the `orchestrate backend --check` commit-time
# drift gate for the generated active.md pointer (ADR-0013 / step-04; BDS-18).
# The backend verb is distinct from `prep`, so its cases live here rather than
# squatting in test-orchestrate-prep.sh; the show-path `active_in_sync`
# assertions migrated here from that file (C5). Homegrown harness (ADR-0017 — no
# bats); deterministic, no LLM/MCP/network. Picked up by test/run's `test-*.sh`
# glob.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="orchestrate-backend"
# shellcheck source=test/helpers.sh
source test/helpers.sh

export WIP_NO_REGISTRY=1

# --- plugin (source != vendored) fixture --------------------------------
# An isolated root with a roles/backends/ tree; CLAUDE_PLUGIN_ROOT neutralized
# so the real plugin dir is never picked up (mirrors test-orchestrate-prep.sh's
# backend block). Built inline rather than via wip_fixture_init --orchestration
# because that helper seeds neither the roles/backends/{solo,task,active}.md tree
# nor `source: plugin` (soft flag: a future DRY pass could grow the helper).
tmp="$(wip_mktemp)"
mkdir -p "$tmp/roles/backends"
printf 'SOLO BINDING\n' >"$tmp/roles/backends/solo.md"
printf 'TASK BINDING\n' >"$tmp/roles/backends/task.md"
cp "$tmp/roles/backends/solo.md" "$tmp/roles/backends/active.md"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  orchestration: { enabled: true, backend: solo, source: plugin }
current_initiative: demo
initiatives: []
YAML

runb() { CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp" bin/wip-plumbing orchestrate backend "$@"; }

# --- Show path unchanged (migrated C1 regression guard) ------------------
# The pre-existing show-path `active_in_sync` assertion (was test-orchestrate-
# prep.sh b1) migrated here, strengthened with the drifted case. Proves the C1
# shared-oracle refactor preserved the show path's behavior byte-for-byte.
s1="$(runb)"
assert_eq "true" "$(jq -r '.ok' <<<"$s1")" "show: ok"
assert_eq "solo" "$(jq -r '.backend' <<<"$s1")" "show: current backend solo"
assert_eq "true" "$(jq -r '.active_in_sync' <<<"$s1")" "show: active_in_sync true when synced"

printf 'HAND EDIT\n' >>"$tmp/roles/backends/active.md"
s2="$(runb)"
assert_eq "false" "$(jq -r '.active_in_sync' <<<"$s2")" "show: active_in_sync false when drifted"
cp "$tmp/roles/backends/solo.md" "$tmp/roles/backends/active.md" # heal

# --- --check: in sync ---------------------------------------------------
c1="$(runb --check)"
assert_eq "true" "$(jq -r '.ok' <<<"$c1")" "check in-sync: ok true"
assert_eq "solo" "$(jq -r '.backend' <<<"$c1")" "check in-sync: backend solo"
assert_eq "plugin" "$(jq -r '.source' <<<"$c1")" "check in-sync: source plugin"
assert_eq "true" "$(jq -r '.active_in_sync' <<<"$c1")" "check in-sync: active_in_sync true"
assert_eq "[]" "$(jq -c '.drift' <<<"$c1")" "check in-sync: drift empty"
set +e
runb --check >/dev/null 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "check in-sync: exit 0"

# --- No-write invariant (in-sync case) ----------------------------------
# --check NEVER writes: byte-compare active.md AND .wip.yaml before/after.
before_active="$(wip_mktemp)/active.md"
before_manifest="$(wip_mktemp)/.wip.yaml"
cp "$tmp/roles/backends/active.md" "$before_active"
cp "$tmp/.wip.yaml" "$before_manifest"
runb --check >/dev/null 2>&1 || true
assert_cmp "$before_active" "$tmp/roles/backends/active.md" "no-write (in sync): active.md unchanged"
assert_cmp "$before_manifest" "$tmp/.wip.yaml" "no-write (in sync): .wip.yaml unchanged"

# --- --check: hand-edited drift -----------------------------------------
printf 'HAND EDIT DRIFT\n' >>"$tmp/roles/backends/active.md"
set +e
c2="$(runb --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "check drift: exit 4"
assert_eq "false" "$(jq -r '.ok' <<<"$c2")" "check drift: ok false"
assert_eq "backend-drift" "$(jq -r '.error.kind' <<<"$c2")" "check drift: kind backend-drift"
assert_eq "4" "$(jq -r '.error.code' <<<"$c2")" "check drift: error.code 4"
assert_eq '["roles/backends/active.md"]' "$(jq -c '.error.paths' <<<"$c2")" \
  "check drift: error.paths names active.md"

# --- No-write invariant (drift case) ------------------------------------
# Still no write even on the failing branch.
drift_active="$(wip_mktemp)/active.md"
drift_manifest="$(wip_mktemp)/.wip.yaml"
cp "$tmp/roles/backends/active.md" "$drift_active"
cp "$tmp/.wip.yaml" "$drift_manifest"
runb --check >/dev/null 2>&1 || true
assert_cmp "$drift_active" "$tmp/roles/backends/active.md" "no-write (drift): active.md unchanged"
assert_cmp "$drift_manifest" "$tmp/.wip.yaml" "no-write (drift): .wip.yaml unchanged"

# --- Heal via switch, --check back to 0 ---------------------------------
runb solo >/dev/null 2>&1 # `orchestrate backend solo` heals (== `make active`)
assert_cmp "$tmp/roles/backends/solo.md" "$tmp/roles/backends/active.md" "heal: active.md == solo.md"
set +e
runb --check >/dev/null 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "check after heal: exit 0"

# --- --check: missing pointer = drift -----------------------------------
rm "$tmp/roles/backends/active.md"
set +e
c3="$(runb --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "check missing pointer: exit 4"
assert_eq "backend-drift" "$(jq -r '.error.kind' <<<"$c3")" "check missing pointer: kind backend-drift"
cp "$tmp/roles/backends/solo.md" "$tmp/roles/backends/active.md" # restore

# --- --check: bad manifest = unknown-backend (never a false in-sync) ----
CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp" yq -i '.features.orchestration.backend = "nope"' "$tmp/.wip.yaml"
set +e
c4="$(runb --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "check bad manifest: exit 4"
assert_eq "unknown-backend" "$(jq -r '.error.kind' <<<"$c4")" "check bad manifest: kind unknown-backend"
assert_eq "false" "$(jq -r '.ok' <<<"$c4")" "check bad manifest: ok false (not a silent in-sync)"
CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmp" yq -i '.features.orchestration.backend = "solo"' "$tmp/.wip.yaml"

# --- --check <name>: usage error ----------------------------------------
set +e
c5="$(runb --check solo 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "check with name: exit 2 usage"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$c5")" "check with name: kind usage"

# --- Vendored no-op (source: vendored) ----------------------------------
# A flattened consumer has no active.md/roles/ pointer to gate → --check is a
# clean no-op (D4). No agent files needed: the no-op returns before any render.
tmpv="$(wip_mktemp)"
cat >"$tmpv/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  orchestration: { enabled: true, backend: solo, source: vendored }
current_initiative: demo
initiatives: []
YAML
set +e
v1="$(env -u WIP_ROLES_DIR CLAUDE_PLUGIN_ROOT="" WIP_ROOT="$tmpv" bin/wip-plumbing orchestrate backend --check)"
rc=$?
set -e
assert_eq "0" "$rc" "vendored --check: exit 0"
assert_eq "true" "$(jq -r '.ok' <<<"$v1")" "vendored --check: ok true"
assert_eq "vendored" "$(jq -r '.source' <<<"$v1")" "vendored --check: source vendored"
assert_eq "null" "$(jq -r '.active_in_sync' <<<"$v1")" "vendored --check: active_in_sync null"
assert_eq "[]" "$(jq -c '.drift' <<<"$v1")" "vendored --check: drift empty"
assert_absent "$tmpv/.claude" "vendored --check: writes no .claude/"
assert_absent "$tmpv/roles" "vendored --check: writes no roles/"

test_summary
