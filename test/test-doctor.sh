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
export WIP_NOW="2026-06-14"

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

# --- Vendored role/command drift fan-in (ADR-0023 C5 — closes ADR-0015 Q-05.4) -
# doctor runs the shared two-axis provenance classifier ONLY on a
# `source: vendored` repo and surfaces each non-clean file as an
# orchestration/vendored-drift check (exit 4). This is the render fan-in Q-05.4
# backlogged — distinct from the pure-disk legacy-footprint scan above.

# (i) vendored + injected local drift → one vendored-drift check, right state +
#     fix + path, exit 4.
vd_dir="$tmp/vendored-drift"
mkdir -p "$vd_dir"
WIP_ROOT="$vd_dir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$vd_dir" bin/wip-plumbing setup agents >/dev/null 2>&1
printf '\n# hand edit\n' >>"$vd_dir/.claude/agents/wip/builder.md"
set +e
vd_out="$(WIP_ROOT="$vd_dir" bin/wip-plumbing doctor)"
vd_rc=$?
set -e
assert_eq "4" "$vd_rc" "[vendored-drift] doctor exits 4 on vendored drift"
assert_eq "1" "$(jq -r '[.checks[]|select(.kind=="orchestration" and .status=="vendored-drift")]|length' <<<"$vd_out")" \
  "[vendored-drift] one vendored-drift check"
assert_eq "locally-modified" "$(jq -r '.checks[]|select(.status=="vendored-drift").state' <<<"$vd_out")" \
  "[vendored-drift] state locally-modified"
assert_eq "setup agents --sync --force" "$(jq -r '.checks[]|select(.status=="vendored-drift").fix' <<<"$vd_out")" \
  "[vendored-drift] fix names --sync --force"
assert_eq ".claude/agents/wip/builder.md" "$(jq -r '.checks[]|select(.status=="vendored-drift").paths[0]' <<<"$vd_out")" \
  "[vendored-drift] paths names the drifted file"

# (ii) clean vendored install → no vendored-drift check, exit 0.
vd_clean="$tmp/vendored-clean"
mkdir -p "$vd_clean"
WIP_ROOT="$vd_clean" bin/wip-plumbing init >/dev/null
WIP_ROOT="$vd_clean" bin/wip-plumbing setup agents >/dev/null 2>&1
set +e
vd_out2="$(WIP_ROOT="$vd_clean" bin/wip-plumbing doctor)"
vd_rc2=$?
set -e
assert_eq "0" "$vd_rc2" "[vendored-clean] doctor exits 0 on a clean vendored install"
assert_eq "0" "$(jq -r '[.checks[]|select(.status=="vendored-drift")]|length' <<<"$vd_out2")" \
  "[vendored-clean] no vendored-drift check when in sync"

# (ii-b) Duo forward-port shape (unstamped-adopted → upstream-advanced/
#        indeterminate): the direction-aware fix is `setup agents --sync --force`,
#        and doctor still exits 4. Proves the fan-in surfaces the forward-port case.
vd_ind="$tmp/vendored-indeterminate"
mkdir -p "$vd_ind"
WIP_ROOT="$vd_ind" bin/wip-plumbing init >/dev/null
WIP_ROOT="$vd_ind" bin/wip-plumbing setup agents >/dev/null 2>&1
printf '\n<!-- forward port -->\n' >>"$vd_ind/.claude/agents/wip/coordinator.md"
rm "$vd_ind/.claude/agents/wip/.provenance.json"
WIP_ROOT="$vd_ind" bin/wip-plumbing setup agents --sync >/dev/null 2>&1 # adopt-in-place
set +e
vd_outi="$(WIP_ROOT="$vd_ind" bin/wip-plumbing doctor)"
vd_rci=$?
set -e
assert_eq "4" "$vd_rci" "[vendored-indeterminate] doctor exits 4"
assert_eq "upstream-advanced" "$(jq -r '.checks[]|select(.status=="vendored-drift")|.state' <<<"$vd_outi")" \
  "[vendored-indeterminate] state upstream-advanced"
assert_eq "indeterminate" "$(jq -r '.checks[]|select(.status=="vendored-drift")|.direction' <<<"$vd_outi")" \
  "[vendored-indeterminate] direction indeterminate"
assert_eq "setup agents --sync --force" "$(jq -r '.checks[]|select(.status=="vendored-drift")|.fix' <<<"$vd_outi")" \
  "[vendored-indeterminate] fix is --sync --force (direction-aware)"

# (iii) source: plugin → probe SKIPPED entirely (no render cost, no check).
vd_plugin="$tmp/vendored-plugin"
mkdir -p "$vd_plugin"
WIP_ROOT="$vd_plugin" bin/wip-plumbing init >/dev/null
WIP_ROOT="$vd_plugin" bin/wip-plumbing setup agents --source plugin >/dev/null 2>&1
set +e
vd_out3="$(WIP_ROOT="$vd_plugin" bin/wip-plumbing doctor)"
set -e
assert_eq "0" "$(jq -r '[.checks[]|select(.status=="vendored-drift")]|length' <<<"$vd_out3")" \
  "[vendored-plugin] vendored-drift probe skipped on source: plugin"

# --- Missing tracker anchor (ADR-0024 / D7) ---------------------------------
# An enabled + in-flight initiative with no tracker_anchor gets an INFORMATIONAL
# tracker-anchor check (status:"ok"), which must NOT flip ok:false / exit 4.
anc="$tmp/anchor"
mkdir -p "$anc/.wip/initiatives/demo"
cat >"$anc/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x.\n' \
  >"$anc/.wip/initiatives/demo/roadmap.md"
set +e
anc_out="$(WIP_ROOT="$anc" bin/wip-plumbing doctor)"
anc_rc=$?
set -e
assert_eq "0" "$anc_rc" "[anchor] doctor stays exit 0 (suggestion is not drift)"
anc_chk="$(jq -c '.checks[]|select(.kind=="tracker-anchor")' <<<"$anc_out")"
assert_eq "ok" "$(jq -r '.status' <<<"$anc_chk")" "[anchor] tracker-anchor status ok"
assert_eq "demo" "$(jq -r '.slug' <<<"$anc_chk")" "[anchor] names the anchor-less slug"
assert_eq "0" "$(jq -r '.drift_count' <<<"$anc_out")" "[anchor] does not raise drift_count"

# With an anchor present -> no tracker-anchor check.
yq -i '.initiatives[0].tracker_anchor = "BDS-56"' "$anc/.wip.yaml"
assert_eq "0" "$(WIP_ROOT="$anc" bin/wip-plumbing doctor 2>/dev/null | jq '[.checks[]|select(.kind=="tracker-anchor")]|length')" \
  "[anchor] silent once tracker_anchor is set"

# issue-tracker disabled -> no tracker-anchor check even when anchor-less.
yq -i 'del(.initiatives[0].tracker_anchor) | del(.features."issue-tracker")' "$anc/.wip.yaml"
assert_eq "0" "$(WIP_ROOT="$anc" bin/wip-plumbing doctor 2>/dev/null | jq '[.checks[]|select(.kind=="tracker-anchor")]|length')" \
  "[anchor] silent when issue-tracker disabled"

test_summary
