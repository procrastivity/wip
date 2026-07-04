#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="vendor-sync"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# `setup agents --sync [--force] [--dry-run]` (ADR-0023 C4) per-state actor.
# Drives each action through the WIP_ROLES_DIR source seam + on-disk edits.

export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-14"
ORIG_ROLES="$PWD/roles"
AGENT=".claude/agents/wip/builder.md"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- "$1" | awk '{print $1}'
  else
    shasum -a 256 -- "$1" | awk '{print $1}'
  fi
}

fresh_install() {
  local w="$1"
  mkdir -p "$w"
  WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing init >/dev/null
  WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents >/dev/null 2>&1
}

# run_sync <workdir> <roles-dir> [extra-flags...] — run --sync and set the
# globals `out` (stdout JSON) + `SYNC_RC` (exit code). Called DIRECTLY (never in
# a command substitution) so both globals propagate to the caller — capturing rc
# inside a `$(...)` subshell would strand it there (rc varies: 0 vs 4).
out=""
SYNC_RC=0
run_sync() {
  local w="$1" roles="$2"
  shift 2
  set +e
  out="$(WIP_ROLES_DIR="$roles" WIP_ROOT="$w" bin/wip-plumbing setup agents --sync "$@" 2>/dev/null)"
  SYNC_RC=$?
  set -e
}

baseline_of() { jq -r --arg p "$1" '.files[] | select(.path == $p) | .baseline_hash' "$2"; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# roles_ahead — a mutated copy of roles/ (one role moved forward → that agent is
# upstream-advanced when rendered through this seam).
roles_ahead="$tmp/roles-ahead"
mkdir -p "$roles_ahead"
cp -R "$ORIG_ROLES/." "$roles_ahead/"
printf '\n<!-- upstream advanced -->\n' >>"$roles_ahead/builder.md"

# --- 1. clean → skip everything, restamp nothing, byte-stable tree. ----------
w="$tmp/clean"
fresh_install "$w"
pre="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
run_sync "$w" "$ORIG_ROLES"
assert_eq "0" "$SYNC_RC" "[clean] exit 0"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[clean] ok"
assert_eq "0" "$(jq -r '.synced | length' <<<"$out")" "[clean] synced nothing"
assert_eq "13" "$(jq -r '.skipped_clean | length' <<<"$out")" "[clean] skipped all clean"
assert_eq "false" "$(jq -r '.restamped' <<<"$out")" "[clean] no restamp"
post="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
assert_eq "$pre" "$post" "[clean] tree byte-stable"

# --- 2. upstream-advanced → re-render, overwrite, restamp. -------------------
w="$tmp/upstream"
fresh_install "$w"
run_sync "$w" "$roles_ahead"
assert_eq "0" "$SYNC_RC" "[upstream] exit 0"
assert_eq "$AGENT" "$(jq -r '.synced[0]' <<<"$out")" "[upstream] synced the advanced agent"
assert_eq "true" "$(jq -r '.restamped' <<<"$out")" "[upstream] restamped"
# The on-disk file now byte-matches the NEW render — proven by --check (which
# re-renders from the same mutated seam and diffs) exiting 0.
set +e
WIP_ROLES_DIR="$roles_ahead" WIP_ROOT="$w" bin/wip-plumbing setup agents --check >/dev/null 2>&1
chk=$?
set -e
assert_eq "0" "$chk" "[upstream] post-sync file == fresh re-render (render-and-diff clean)"
# Sidecar baseline restamped to the new render's hash.
assert_eq "$(sha256_of "$w/$AGENT")" "$(baseline_of "$AGENT" "$w/.claude/agents/wip/.provenance.json")" \
  "[upstream] sidecar baseline == new on-disk hash"

# --- 3. locally-modified WITHOUT --force → refuse rc4, no .orig, untouched. --
w="$tmp/local"
fresh_install "$w"
printf '\n# hand edit\n' >>"$w/$AGENT"
before="$(cksum "$w/$AGENT")"
run_sync "$w" "$ORIG_ROLES"
assert_eq "4" "$SYNC_RC" "[local] exit 4 (refused without --force)"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "[local] ok:false"
assert_eq "$AGENT" "$(jq -r '.refused_local[0]' <<<"$out")" "[local] names the refused file"
assert_eq "0" "$(jq -r '.synced | length' <<<"$out")" "[local] synced nothing"
assert_absent "$w/$AGENT.orig" "[local] no .orig backup without --force"
assert_eq "$before" "$(cksum "$w/$AGENT")" "[local] file untouched"

# --- 4. locally-modified WITH --force → backup .orig, take upstream, restamp. -
run_sync "$w" "$ORIG_ROLES" --force
assert_eq "0" "$SYNC_RC" "[local --force] exit 0"
assert_eq "$AGENT" "$(jq -r '.backed_up[0]' <<<"$out")" "[local --force] backed up"
assert_eq "$AGENT" "$(jq -r '.synced[0]' <<<"$out")" "[local --force] synced"
assert_file "$w/$AGENT.orig" "[local --force] .orig backup written"
assert_grep "# hand edit" "$w/$AGENT.orig" "[local --force] .orig holds the local edit"
assert_not_grep "# hand edit" "$w/$AGENT" "[local --force] live file took upstream (edit gone)"
set +e
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents --check >/dev/null 2>&1
chk=$?
set -e
assert_eq "0" "$chk" "[local --force] live file == fresh re-render after force"

# --- 5. upstream-behind → NEVER auto-sync, even with --force (D2a). ----------
w="$tmp/behind"
fresh_install "$w"
sc="$w/.claude/agents/wip/.provenance.json"
jq '(.files[] | select(.path == ".claude/agents/wip/builder.md") | .plugin_version) = "999.0.0"' "$sc" >"$tmp/sc.json"
cp "$tmp/sc.json" "$sc"
before="$(cksum "$w/$AGENT")"
run_sync "$w" "$roles_ahead"
assert_eq "0" "$SYNC_RC" "[behind] exit 0"
assert_eq "$AGENT" "$(jq -r '.skipped_regressive[0]' <<<"$out")" "[behind] skipped_regressive"
assert_eq "0" "$(jq -r '.synced | length' <<<"$out")" "[behind] synced nothing"
assert_eq "$before" "$(cksum "$w/$AGENT")" "[behind] file untouched"
# Even --force must not regress a forward-ported copy.
run_sync "$w" "$roles_ahead" --force
assert_eq "$AGENT" "$(jq -r '.skipped_regressive[0]' <<<"$out")" "[behind --force] still skipped_regressive"
assert_eq "$before" "$(cksum "$w/$AGENT")" "[behind --force] still untouched (forward-port protected)"

# --- 6. unstamped → establish baseline from CURRENT bytes, non-destructive. --
w="$tmp/unstamped"
fresh_install "$w"
printf '\n<!-- forward port -->\n' >>"$w/.claude/agents/wip/coordinator.md"
cbefore="$(cksum "$w/.claude/agents/wip/coordinator.md")"
rm "$w/.claude/agents/wip/.provenance.json"
run_sync "$w" "$ORIG_ROLES"
assert_eq "0" "$SYNC_RC" "[unstamped] exit 0"
assert_eq "true" "$(jq -r '.restamped' <<<"$out")" "[unstamped] restamped"
assert_eq "$cbefore" "$(cksum "$w/.claude/agents/wip/coordinator.md")" \
  "[unstamped] current bytes preserved (no overwrite)"
assert_eq "$(sha256_of "$w/.claude/agents/wip/coordinator.md")" \
  "$(baseline_of ".claude/agents/wip/coordinator.md" "$w/.claude/agents/wip/.provenance.json")" \
  "[unstamped] baseline established from the on-disk bytes"

# --- 7. missing → re-render the gone file + stamp. --------------------------
w="$tmp/missing"
fresh_install "$w"
rm "$w/.claude/agents/wip/researcher.md"
run_sync "$w" "$ORIG_ROLES"
assert_eq "0" "$SYNC_RC" "[missing] exit 0"
assert_file "$w/.claude/agents/wip/researcher.md" "[missing] re-vendored the gone file"
assert_eq ".claude/agents/wip/researcher.md" \
  "$(jq -r '.synced[] | select(. == ".claude/agents/wip/researcher.md")' <<<"$out")" \
  "[missing] reported synced"

# --- 8. --dry-run → plan only, touch nothing. --------------------------------
w="$tmp/dry"
fresh_install "$w"
pre="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
run_sync "$w" "$roles_ahead" --dry-run
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "[dry-run] dry_run:true"
assert_eq "$AGENT" "$(jq -r '.synced[0]' <<<"$out")" "[dry-run] would-sync reported"
assert_eq "false" "$(jq -r '.restamped' <<<"$out")" "[dry-run] no restamp"
post="$(find "$w/.claude" -type f -exec cksum {} \; | sort)"
assert_eq "$pre" "$post" "[dry-run] tree byte-stable"

# --- 9. source: plugin → nothing vendored → empty ledger, exit 0. ------------
w="$tmp/plugin"
mkdir -p "$w"
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing init >/dev/null
WIP_ROLES_DIR="$ORIG_ROLES" WIP_ROOT="$w" bin/wip-plumbing setup agents --source plugin >/dev/null 2>&1
run_sync "$w" "$ORIG_ROLES"
assert_eq "0" "$SYNC_RC" "[plugin] exit 0"
assert_eq "0" "$(jq -r '.synced | length' <<<"$out")" "[plugin] synced nothing"
assert_eq "false" "$(jq -r '.restamped' <<<"$out")" "[plugin] no restamp"

test_summary
