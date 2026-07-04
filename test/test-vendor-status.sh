#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="vendor-status"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# `setup agents --status` (ADR-0023 C3) two-axis vendored-drift report. Drives
# each of the seven states through the WIP_ROLES_DIR source seam + on-disk edits
# (never git objects, per KV note-duo-vendored-gitignored). Mirrors how
# test-flatten-render.sh / test-setup.sh inject a roles dir.

export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-14"
ORIG_ROLES="$PWD/roles"

cmd_count="$(find templates/setup/agents/commands -maxdepth 1 -name '*.md' | wc -l | tr -d ' ')"
total=$((4 + cmd_count))

# fresh_install <workdir> — init + a clean vendored `setup agents` (stamps the
# sidecar), rendered from the canonical roles/.
fresh_install() {
  local w="$1"
  mkdir -p "$w"
  WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing init >/dev/null
  WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents >/dev/null 2>&1
}

# status_json <workdir> <roles-dir> — run --status with the given roles seam,
# echo ONLY the stdout JSON (stderr table suppressed); stash the exit code in the
# global STATUS_RC so callers can assert the reporting-not-gating contract
# without polluting the captured JSON.
STATUS_RC=0
status_json() {
  local w="$1" roles="$2" out
  set +e
  out="$(WIP_ROLES_DIR="$roles" WIP_ROOT="$w" bin/wip-plumbing setup agents --status 2>/dev/null)"
  STATUS_RC=$?
  set -e
  printf '%s' "$out"
}

state_of() { jq -r --arg p "$1" '.files[] | select(.path == $p) | .state' <<<"$2"; }
action_of() { jq -r --arg p "$1" '.files[] | select(.path == $p) | .action' <<<"$2"; }
dir_of() { jq -r --arg p "$1" '.files[] | select(.path == $p) | .direction' <<<"$2"; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

AGENT=".claude/agents/wip/builder.md"
CMD=".claude/commands/wip/next.md"

# --- 1. clean: fresh install, status via the same roles → all clean. ----------
w="$tmp/clean"
fresh_install "$w"
out="$(status_json "$w" "$ORIG_ROLES")"
assert_eq "0" "$STATUS_RC" "[clean] exit 0 (reporting, never gating)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[clean] ok"
assert_eq "$total" "$(jq -r '.files | length' <<<"$out")" "[clean] reports 4 agents + N commands"
assert_eq "$total" "$(jq -r '.summary.clean' <<<"$out")" "[clean] summary.clean == total"
assert_eq "clean" "$(state_of "$AGENT" "$out")" "[clean] agent state clean"
assert_eq "none" "$(action_of "$AGENT" "$out")" "[clean] agent action none"
# kind is derived from the path.
assert_eq "agent" "$(jq -r --arg p "$AGENT" '.files[] | select(.path==$p) | .kind' <<<"$out")" "[clean] agent kind"
assert_eq "command" "$(jq -r --arg p "$CMD" '.files[] | select(.path==$p) | .kind' <<<"$out")" "[clean] command kind"

# --- 2. locally-modified: hand-edit one installed agent + one command. --------
w="$tmp/local"
fresh_install "$w"
printf '\n# hand edit\n' >>"$w/$AGENT"
printf '\n<!-- hand edit -->\n' >>"$w/$CMD"
out="$(status_json "$w" "$ORIG_ROLES")"
assert_eq "locally-modified" "$(state_of "$AGENT" "$out")" "[local] agent locally-modified"
assert_eq "sync-force" "$(action_of "$AGENT" "$out")" "[local] agent action sync-force"
assert_eq "locally-modified" "$(state_of "$CMD" "$out")" "[local] command locally-modified (commands flow through)"
assert_eq "2" "$(jq -r '.summary.locally_modified' <<<"$out")" "[local] summary counts both"

# --- 3. upstream-advanced: install from canonical roles, status from a MUTATED
#        roles copy (a plugin-side fix moved forward). One role → one agent. ----
w="$tmp/upstream"
fresh_install "$w"
roles_ahead="$tmp/roles-ahead"
mkdir -p "$roles_ahead"
cp -R "$ORIG_ROLES/." "$roles_ahead/"
printf '\n<!-- upstream advanced -->\n' >>"$roles_ahead/builder.md"
out="$(status_json "$w" "$roles_ahead")"
assert_eq "upstream-advanced" "$(state_of "$AGENT" "$out")" "[upstream] agent upstream-advanced"
assert_eq "ahead" "$(dir_of "$AGENT" "$out")" "[upstream] direction ahead"
assert_eq "sync" "$(action_of "$AGENT" "$out")" "[upstream] agent action sync"
# The other three roles are untouched → still clean.
assert_eq "clean" "$(state_of ".claude/agents/wip/orchestrator.md" "$out")" "[upstream] sibling role still clean"
assert_eq "1" "$(jq -r '.summary.upstream_advanced' <<<"$out")" "[upstream] summary counts one"

# --- 4. upstream-behind: same upstream move, but the STAMP is newer than the
#        installed plugin (a forward-port — D2a) → must NOT auto-sync. ----------
w="$tmp/behind"
fresh_install "$w"
# Bump the stamped plugin_version above any installed version.
sc="$w/.claude/agents/wip/.provenance.json"
jq '(.files[] | select(.path == ".claude/agents/wip/builder.md") | .plugin_version) = "999.0.0"' "$sc" >"$tmp/sc.json"
cp "$tmp/sc.json" "$sc"
out="$(status_json "$w" "$roles_ahead")"
assert_eq "upstream-behind" "$(state_of "$AGENT" "$out")" "[behind] agent upstream-behind"
assert_eq "behind" "$(dir_of "$AGENT" "$out")" "[behind] direction behind"
assert_eq "upgrade-plugin" "$(action_of "$AGENT" "$out")" "[behind] action upgrade-plugin (never auto-sync)"
# Folds into the upstream_advanced summary bucket (six mutually-exclusive buckets).
assert_eq "1" "$(jq -r '.summary.upstream_advanced' <<<"$out")" "[behind] folds into upstream_advanced bucket"

# --- 5. both-diverged: upstream moved AND the file was hand-edited. -----------
w="$tmp/both"
fresh_install "$w"
printf '\n# local conflict\n' >>"$w/$AGENT"
out="$(status_json "$w" "$roles_ahead")"
assert_eq "0" "$STATUS_RC" "[both] exit 0 even under drift (never gating)"
assert_eq "both-diverged" "$(state_of "$AGENT" "$out")" "[both] agent both-diverged"
assert_eq "sync-force" "$(action_of "$AGENT" "$out")" "[both] action sync-force"
assert_eq "1" "$(jq -r '.summary.both_diverged' <<<"$out")" "[both] summary counts one"

# --- 6. unstamped: no sidecar entry (legacy vendor, e.g. Duo today). ----------
w="$tmp/unstamped"
fresh_install "$w"
rm "$w/.claude/agents/wip/.provenance.json"
out="$(status_json "$w" "$ORIG_ROLES")"
assert_eq "unstamped" "$(state_of "$AGENT" "$out")" "[unstamped] agent unstamped"
assert_eq "sync" "$(action_of "$AGENT" "$out")" "[unstamped] action sync"
assert_eq "$total" "$(jq -r '.summary.unstamped' <<<"$out")" "[unstamped] whole install unstamped"

# --- 7. missing: sidecar names a file that is gone. --------------------------
w="$tmp/missing"
fresh_install "$w"
rm "$w/.claude/agents/wip/researcher.md"
out="$(status_json "$w" "$ORIG_ROLES")"
assert_eq "missing" "$(state_of ".claude/agents/wip/researcher.md" "$out")" "[missing] agent missing"
assert_eq "sync" "$(action_of ".claude/agents/wip/researcher.md" "$out")" "[missing] action sync"
assert_eq "1" "$(jq -r '.summary.missing' <<<"$out")" "[missing] summary counts one"

# --- 8. source: plugin → nothing vendored → empty report, exit 0. ------------
w="$tmp/plugin"
mkdir -p "$w"
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing init >/dev/null
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents --source plugin >/dev/null 2>&1
out="$(status_json "$w" "$ORIG_ROLES")"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[plugin] ok"
assert_eq "0" "$(jq -r '.files | length' <<<"$out")" "[plugin] no vendored files → empty report"
assert_eq "0" "$(jq -r '.summary.clean' <<<"$out")" "[plugin] summary all zero"

# --- 9. --status writes NOTHING (read-only) even under drift. -----------------
w="$tmp/readonly"
fresh_install "$w"
printf '\n# edit\n' >>"$w/$AGENT"
pre="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
status_json "$w" "$ORIG_ROLES" >/dev/null
post="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
assert_eq "$pre" "$post" "[readonly] --status wrote nothing (byte-stable tree)"

# --- 10. --status must not combine with --check / --migrate. -----------------
w="$tmp/combo"
fresh_install "$w"
set +e
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents --status --check >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "[combo] --status --check → usage error exit 2"

test_summary
