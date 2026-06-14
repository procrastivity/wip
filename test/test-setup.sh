#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="setup"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-14"

# --- 1. Template fidelity vs the live repo (step-09 byte-derivation). ----------
assert_cmp templates/setup/deps/flake.nix flake.nix \
  "deps/flake.nix is byte-equal to flake.nix"
assert_cmp templates/setup/deps/flake.lock flake.lock \
  "deps/flake.lock is byte-equal to flake.lock"
assert_cmp templates/setup/direnv/.envrc .envrc \
  "direnv/.envrc is byte-equal to .envrc"
assert_cmp templates/setup/hygiene/.pre-commit-config.yaml .pre-commit-config.yaml \
  "hygiene/.pre-commit-config.yaml is byte-equal to .pre-commit-config.yaml"

# --- 2. Plugin substitution check on the agents/ template subtree. ------------
# The template's plugin must reference `wip-plumbing` (no `bin/` prefix), since
# consumers are expected to have wip on PATH. The live repo's own .claude-plugin
# legitimately keeps `bin/wip-plumbing` (dogfood-local) — that's the divergence.
assert_eq "0" "$(grep -rl 'bin/wip-plumbing' templates/setup/agents/ 2>/dev/null | wc -l | tr -d ' ')" \
  "no bin/wip-plumbing references in agents/ template"
assert_grep 'wip-plumbing' \
  "templates/setup/agents/.claude-plugin/commands/next.md" \
  "agents/ template references wip-plumbing"

# --- 3. Missing manifest → exit 3 missing-manifest. ----------------------------
mkdir -p "$tmp/no-manifest"
set +e
out="$(WIP_ROOT="$tmp/no-manifest" bin/wip-plumbing setup deps 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "missing manifest exit 3"
assert_eq "missing-manifest" "$(jq -r '.error.kind' <<<"$out")" "missing manifest kind"

# --- 4. setup direnv without flake.nix → exit 3 missing-prereq. ---------------
mkdir -p "$tmp/prereq"
WIP_ROOT="$tmp/prereq" bin/wip-plumbing init >/dev/null
set +e
out="$(WIP_ROOT="$tmp/prereq" bin/wip-plumbing setup direnv 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "missing prereq exit 3"
assert_eq "missing-prereq" "$(jq -r '.error.kind' <<<"$out")" "missing prereq kind"
assert_eq "flake.nix" "$(jq -r '.error.path' <<<"$out")" "missing prereq path"

# --- 5. Each verb writes its expected file set; idempotent on 2nd run. --------
for verb in deps direnv hygiene release agents; do
  workdir="$tmp/v-$verb"
  mkdir -p "$workdir"
  WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
  # deps prereq for direnv
  if [[ "$verb" == "direnv" ]]; then
    WIP_ROOT="$workdir" bin/wip-plumbing setup deps >/dev/null 2>&1
  fi

  out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup "$verb" 2>/dev/null)"
  assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[$verb] ok"
  wrote_n="$(jq -r '.wrote | length' <<<"$out")"
  case "$verb" in
    deps) expected=2 ;;    # flake.nix + flake.lock
    direnv) expected=1 ;;  # .envrc
    hygiene) expected=1 ;; # .pre-commit-config.yaml
    release) expected=2 ;; # cliff.toml + CHANGELOG.md
    agents) expected=10 ;; # 4 agents + 3 commands + agents/README + plugin/README + plugin.json
  esac
  assert_eq "$expected" "$wrote_n" "[$verb] wrote $expected files"
  assert_eq "0" "$(jq -r '.refused | length' <<<"$out")" "[$verb] no refusals"

  # Re-run idempotency
  out2="$(WIP_ROOT="$workdir" bin/wip-plumbing setup "$verb" 2>/dev/null)"
  assert_eq "0" "$(jq -r '.wrote | length' <<<"$out2")" "[$verb] re-run wrote 0"
  assert_eq "$expected" "$(jq -r '.skipped_idempotent | length' <<<"$out2")" "[$verb] re-run skipped all"
  assert_eq "null" "$(jq -r '.manifest_updated' <<<"$out2")" "[$verb] re-run manifest no-op"
done

# --- 6. Feature flag flipping ------------------------------------------------
workdir="$tmp/flags"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup deps >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup direnv >/dev/null 2>&1
assert_eq "true" "$(yq -r '.features.direnv.enabled' "$workdir/.wip.yaml")" "direnv flag flipped"
WIP_ROOT="$workdir" bin/wip-plumbing setup release >/dev/null 2>&1
assert_eq "true" "$(yq -r '.features.changelog.enabled' "$workdir/.wip.yaml")" "changelog flag flipped"
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1
assert_eq "true" "$(yq -r '.features.orchestration.enabled' "$workdir/.wip.yaml")" "orchestration enabled"
assert_eq "solo" "$(yq -r '.features.orchestration.backend' "$workdir/.wip.yaml")" "orchestration backend=solo"
assert_eq "plugin" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" "orchestration source=plugin"

# Solo block is NOT auto-created (consumer's decision per ADR-0007)
assert_eq "null" "$(yq -r '.features.solo // "null"' "$workdir/.wip.yaml")" "no auto features.solo block"

# --- 7. Sentinel post-check passes; doctor on tempdir is clean ---------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing doctor 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "doctor ok after all setups"
assert_eq "0" "$(jq -r '.drift_count' <<<"$out")" "doctor drift 0"

# --- 8. Content drift → exit 4 content-drift, refused list non-empty ---------
workdir="$tmp/drift"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup deps >/dev/null 2>&1
echo "# drift line" >>"$workdir/flake.nix"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup deps 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "content drift exit 4"
assert_eq "content-drift" "$(jq -r '.error.kind' <<<"$out")" "content drift kind"
assert_eq "flake.nix" "$(jq -r '.error.paths[0]' <<<"$out")" "content drift path"

# --- 9. --force overwrites drift; subsequent run is clean --------------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup deps --force 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "force overwrite ok"
# wrote_forced contains flake.nix at least
assert_eq "1" "$(jq -r '[.wrote_forced[] | select(. == "flake.nix")] | length' <<<"$out")" \
  "force overwrote flake.nix"
# After force, byte-equal again
assert_cmp templates/setup/deps/flake.nix "$workdir/flake.nix" \
  "post-force flake.nix matches template"

# --- 10. flake.lock skip-if-present (never compare without --force) ----------
echo "drift_to_lock" >>"$workdir/flake.lock"
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup deps 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "lock-style ok despite drift"
# Lock should be in skipped, NOT refused (proves never-compare semantics)
assert_eq "1" "$(jq -r '[.skipped_idempotent[] | select(. == "flake.lock")] | length' <<<"$out")" \
  "flake.lock skipped on drift"
assert_eq "0" "$(jq -r '[.refused[] | select(. == "flake.lock")] | length' <<<"$out")" \
  "flake.lock not refused"

# --- 11. --dry-run touches nothing ------------------------------------------
workdir="$tmp/dryrun"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing --dry-run setup deps 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "dry-run ok"
assert_eq "2" "$(jq -r '.wrote | length' <<<"$out")" "dry-run ledger wrote=2"
assert_absent "$workdir/flake.nix" "dry-run no flake.nix on disk"
assert_absent "$workdir/flake.lock" "dry-run no flake.lock on disk"
assert_eq "null" "$(yq -r '.features.direnv.enabled // "null"' "$workdir/.wip.yaml")" \
  "dry-run no manifest change (no direnv block)"

# --- 12. Full round-trip dogfood: tempdir → all five verbs → cmp vs live ----
workdir="$tmp/roundtrip"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup deps >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup direnv >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup hygiene >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup release >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1

assert_cmp "$workdir/flake.nix" flake.nix "round-trip flake.nix == live"
assert_cmp "$workdir/.envrc" .envrc "round-trip .envrc == live"
assert_cmp "$workdir/.pre-commit-config.yaml" .pre-commit-config.yaml \
  "round-trip .pre-commit-config.yaml == live"
# CHANGELOG.md + cliff.toml don't have live equivalents — just assert they landed
assert_file "$workdir/CHANGELOG.md" "round-trip CHANGELOG.md present"
assert_file "$workdir/cliff.toml" "round-trip cliff.toml present"
# agents tree landed with the substituted form
assert_file "$workdir/.claude-plugin/plugin.json" "round-trip plugin.json present"
assert_not_grep 'bin/wip-plumbing' "$workdir/.claude-plugin/README.md" \
  "round-trip agents/ has no bin/wip-plumbing"

# --- 13. doctor on the round-trip tempdir → clean ---------------------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing doctor 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "round-trip doctor ok"
assert_eq "0" "$(jq -r '.drift_count' <<<"$out")" "round-trip doctor drift 0"

# --- 14. Bad subcommand → exit 2 usage --------------------------------------
workdir="$tmp/badsub"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
set +e
WIP_ROOT="$workdir" bin/wip-plumbing setup bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "bad subcommand exit 2"
set +e
WIP_ROOT="$workdir" bin/wip-plumbing setup >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing subcommand exit 2"

test_summary
