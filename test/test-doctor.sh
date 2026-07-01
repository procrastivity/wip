#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Isolate from the live registry; the legacy-footprint cases (below) drive
# `setup agents --migrate` / `setup agents`, whose flatten renderer resolves
# roles/ via the WIP_ROLES_DIR seam (matches test-setup.sh).
export WIP_NO_REGISTRY=1
export WIP_ROLES_DIR="$PWD/roles"

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

# --- Orchestration legacy-footprint (pure-disk; ADR-0020 / step-07 Chunk 4) ----
# doctor reuses the setup-family classifier to detect the OLD plugin-tree
# `setup agents` footprint and steer the operator to `--migrate`. Seed the REAL
# 16-file footprint by copying templates/setup/agents/** verbatim into a repo
# root (that IS what the old walk did) + the F2 `source: plugin` mislabel — the
# same seed the migrate suite uses. Gate keys on ≥1 `owned` line, so a
# foreign-only or stray-only footprint stays quiet (D5).
cmd_count="$(find templates/setup/agents/commands -maxdepth 1 -name '*.md' | wc -l | tr -d ' ')"
lf_owned_n=$((2 + 1 + cmd_count + 4)) # .claude-plugin/{plugin.json,README} + agents/README + N cmds + 4 roles = 16
seed_old_footprint() {                # <workdir> — 16-file footprint + source:plugin mislabel
  local d="$1"
  mkdir -p "$d"
  WIP_ROOT="$d" bin/wip-plumbing init >/dev/null
  cp -R templates/setup/agents/. "$d/"
  # The old walk wrote an owned (name: wip) root plugin.json, but the template
  # no longer ships one (removed as dead footprint post-ADR-0020) — synthesize
  # it so the seed still mirrors a REAL legacy install (detector keys on .name).
  printf '{ "name": "wip", "version": "0.0.0" }\n' >"$d/.claude-plugin/plugin.json"
  yq -i '.features.orchestration = {"enabled": true, "backend": "solo", "source": "plugin"}' \
    "$d/.wip.yaml"
}

# (i) Detect — old footprint on disk → exit 4, one orchestration/legacy-footprint
#     check naming --migrate, with the 16 owned paths.
lf_dir="$tmp/legacy-detect"
seed_old_footprint "$lf_dir"
set +e
lf_out="$(WIP_ROOT="$lf_dir" bin/wip-plumbing doctor)"
lf_rc=$?
set -e
assert_eq "4" "$lf_rc" "[legacy] doctor exits 4 on old footprint"
mapfile -t LF < <(jq -r '
  ([.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint")] | length),
  (.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint").fix),
  ([.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint").paths|length]|add // 0),
  ([.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint").paths[]|select(.==".claude-plugin/plugin.json")]|length)
' <<<"$lf_out")
assert_eq "1" "${LF[0]}" "[legacy] one legacy-footprint check"
assert_eq "run wip-plumbing setup agents --migrate" "${LF[1]}" "[legacy] fix names --migrate"
assert_eq "$lf_owned_n" "${LF[2]}" "[legacy] paths lists the 16 owned footprint files"
assert_eq "1" "${LF[3]}" "[legacy] paths include the owned root plugin.json"

# (ii) Clean after --migrate — the migrated repo trips no legacy-footprint check.
WIP_ROOT="$lf_dir" bin/wip-plumbing setup agents --migrate >/dev/null 2>&1
set +e
lf_out2="$(WIP_ROOT="$lf_dir" bin/wip-plumbing doctor)"
set -e
assert_eq "0" "$(jq -r '[.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint")]|length' <<<"$lf_out2")" \
  "[legacy] quiet after --migrate"

# (iii) Quiet on a deliberate plugin repo (D5): a foreign host plugin.json (name
#       != wip) + no owned wip files → NOT a wip footprint → no legacy check.
lf_plugin="$tmp/legacy-plugin"
mkdir -p "$lf_plugin/.claude-plugin"
WIP_ROOT="$lf_plugin" bin/wip-plumbing init >/dev/null
printf '{ "name": "clast", "version": "0.0.0" }\n' >"$lf_plugin/.claude-plugin/plugin.json"
yq -i '.features.orchestration = {"enabled": true, "backend": "solo", "source": "plugin"}' \
  "$lf_plugin/.wip.yaml"
set +e
lf_out3="$(WIP_ROOT="$lf_plugin" bin/wip-plumbing doctor)"
set -e
assert_eq "0" "$(jq -r '[.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint")]|length' <<<"$lf_out3")" \
  "[legacy] quiet on foreign-only plugin repo"

# (iv) Quiet on a fresh flattened install (no root footprint at all).
lf_fresh="$tmp/legacy-fresh"
mkdir -p "$lf_fresh"
WIP_ROOT="$lf_fresh" bin/wip-plumbing init >/dev/null
WIP_ROOT="$lf_fresh" bin/wip-plumbing setup agents >/dev/null 2>&1
set +e
lf_out4="$(WIP_ROOT="$lf_fresh" bin/wip-plumbing doctor)"
set -e
assert_eq "0" "$(jq -r '[.checks[]|select(.kind=="orchestration" and .status=="legacy-footprint")]|length' <<<"$lf_out4")" \
  "[legacy] quiet on fresh flattened repo"

test_summary
