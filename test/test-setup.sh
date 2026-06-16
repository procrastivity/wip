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
  "templates/setup/agents/commands/next.md" \
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

# --- 15. setup lds — template fidelity (maintenance/*.md) -------------------
# Maintenance .md files ship verbatim from the LDS distribution; any drift here
# means the templates/setup/lds/ tree needs a refresh.
for m in audit refine sync update; do
  assert_cmp "templates/setup/lds/engineering/maintenance/$m.md" \
    "layered-documentation-system/maintenance/$m.md" \
    "lds maintenance/$m.md byte-equal to LDS distribution"
done

# Seed manifest is yq-parseable + has the validator-required shape
assert_eq "1.0.0" "$(yq -r '.metadata.schema_version' \
  templates/setup/lds/engineering/.lds-manifest.yaml)" \
  "lds seed manifest schema_version=1.0.0"
assert_eq "approved" "$(yq -r '.metadata.status' \
  templates/setup/lds/engineering/.lds-manifest.yaml)" \
  "lds seed manifest status=approved"
assert_eq "0" "$(yq -r '.entries | length' \
  templates/setup/lds/engineering/.lds-manifest.yaml)" \
  "lds seed manifest entries=[]"

# --- 16. setup lds (full mode) writes 13 files; idempotent on re-run ---------
workdir="$tmp/lds-full"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[lds] ok"
assert_eq "13" "$(jq -r '.wrote | length' <<<"$out")" "[lds] wrote 13 files"
assert_eq "0" "$(jq -r '.refused | length' <<<"$out")" "[lds] no refusals"
assert_eq "engineering/.lds-manifest.yaml" "$(jq -r '.sentinel' <<<"$out")" \
  "[lds] sentinel path"
assert_eq "true" "$(jq -r '.sentinel_present' <<<"$out")" "[lds] sentinel present"

# Layer dirs all exist with .gitkeep
for layer in decisions product architecture specs reference features implementation appendices; do
  assert_file "$workdir/engineering/$layer/.gitkeep" "[lds] $layer/.gitkeep present"
done
assert_file "$workdir/engineering/maintenance/audit.md" "[lds] maintenance/audit.md present"
assert_file "$workdir/engineering/.lds-manifest.yaml" "[lds] sentinel manifest on disk"

# Re-run idempotency
out2="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
assert_eq "0" "$(jq -r '.wrote | length' <<<"$out2")" "[lds] re-run wrote 0"
assert_eq "13" "$(jq -r '.skipped_idempotent | length' <<<"$out2")" \
  "[lds] re-run skipped all 13"
assert_eq "null" "$(jq -r '.manifest_updated' <<<"$out2")" \
  "[lds] re-run manifest no-op"

# --- 17. setup lds flips features.lds.{enabled, root: engineering} ----------
assert_eq "true" "$(yq -r '.features.lds.enabled' "$workdir/.wip.yaml")" \
  "[lds] features.lds.enabled flipped"
assert_eq "engineering" "$(yq -r '.features.lds.root' "$workdir/.wip.yaml")" \
  "[lds] features.lds.root set"

# Doctor reports zero drift after setup lds
out="$(WIP_ROOT="$workdir" bin/wip-plumbing doctor 2>/dev/null)"
assert_eq "0" "$(jq -r '.drift_count' <<<"$out")" "[lds] doctor drift 0"

# --- 18. --force overwrites drifted maintenance file -------------------------
workdir="$tmp/lds-drift"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup lds >/dev/null 2>&1
echo "drift" >>"$workdir/engineering/maintenance/audit.md"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[lds] drift exit 4"
assert_eq "content-drift" "$(jq -r '.error.kind' <<<"$out")" "[lds] drift kind"
assert_eq "1" "$(jq -r '[.error.paths[] | select(. == "engineering/maintenance/audit.md")] | length' <<<"$out")" \
  "[lds] drift path listed"
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds --force 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[lds] --force ok after drift"
assert_cmp "templates/setup/lds/engineering/maintenance/audit.md" \
  "$workdir/engineering/maintenance/audit.md" \
  "[lds] post-force audit.md restored"

# --- 19. --sentinel-only writes only the manifest ----------------------------
workdir="$tmp/lds-sentinel"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds --sentinel-only 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[lds --sentinel-only] ok"
assert_eq "1" "$(jq -r '.wrote | length' <<<"$out")" \
  "[lds --sentinel-only] wrote 1 file"
assert_eq "engineering/.lds-manifest.yaml" "$(jq -r '.wrote[0]' <<<"$out")" \
  "[lds --sentinel-only] wrote the manifest"
assert_file "$workdir/engineering/.lds-manifest.yaml" \
  "[lds --sentinel-only] sentinel on disk"
assert_absent "$workdir/engineering/decisions/.gitkeep" \
  "[lds --sentinel-only] no decisions/.gitkeep"
assert_absent "$workdir/engineering/maintenance/audit.md" \
  "[lds --sentinel-only] no maintenance/audit.md"
# Flags still flip
assert_eq "true" "$(yq -r '.features.lds.enabled' "$workdir/.wip.yaml")" \
  "[lds --sentinel-only] features.lds.enabled flipped"
assert_eq "engineering" "$(yq -r '.features.lds.root' "$workdir/.wip.yaml")" \
  "[lds --sentinel-only] features.lds.root set"

# --sentinel-only after a full install: byte-equal sentinel ⇒ skipped
out="$(WIP_ROOT="$tmp/lds-full" bin/wip-plumbing setup lds --sentinel-only 2>/dev/null)"
assert_eq "1" "$(jq -r '.skipped_idempotent | length' <<<"$out")" \
  "[lds --sentinel-only] idempotent on top of full install"

# --- 20. --sentinel-only rejected for other subcommands ----------------------
set +e
WIP_ROOT="$tmp/lds-full" bin/wip-plumbing setup deps --sentinel-only >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "[deps --sentinel-only] exit 2 usage"

# --- 21. --dry-run touches nothing on lds ------------------------------------
workdir="$tmp/lds-dryrun"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing --dry-run setup lds 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[lds --dry-run] ok"
assert_eq "13" "$(jq -r '.wrote | length' <<<"$out")" "[lds --dry-run] ledger 13"
assert_absent "$workdir/engineering/.lds-manifest.yaml" \
  "[lds --dry-run] no manifest on disk"
assert_absent "$workdir/engineering/decisions/.gitkeep" \
  "[lds --dry-run] no .gitkeep on disk"
assert_eq "null" "$(yq -r '.features.lds.enabled // "null"' "$workdir/.wip.yaml")" \
  "[lds --dry-run] no manifest change"

# --- 22. setup lds refuses when features.lds.root is set elsewhere ----------
workdir="$tmp/lds-elsewhere"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
yq -i '.features.lds.root = "docs"' "$workdir/.wip.yaml"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[lds elsewhere] exit 3"
assert_eq "lds-already-installed-elsewhere" "$(jq -r '.error.kind' <<<"$out")" \
  "[lds elsewhere] kind"
assert_eq "docs" "$(jq -r '.error.path' <<<"$out")" \
  "[lds elsewhere] path = existing root"

# --- 23. End-to-end dogfood: setup lds unblocks graduate --------------------
workdir="$tmp/lds-dogfood"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup lds >/dev/null 2>&1
mkdir -p "$workdir/scratch"
cat >"$workdir/scratch/dogfood.md" <<'EOF'
---
graduate-to: decisions/auto-followup-dogfood.md
---
# Dogfood ADR

Body content proving setup lds → graduate works end-to-end.
EOF
out="$(WIP_ROOT="$workdir" bin/wip-plumbing graduate "$workdir/scratch/dogfood.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[dogfood] graduate ok"
assert_eq "engineering/decisions/0001-followup-dogfood.md" \
  "$(jq -r '.target' <<<"$out")" "[dogfood] auto-numbered target"
assert_file "$workdir/engineering/decisions/0001-followup-dogfood.md" \
  "[dogfood] graduated file on disk"
# Re-run is idempotent
out2="$(WIP_ROOT="$workdir" bin/wip-plumbing graduate "$workdir/scratch/dogfood.md" 2>/dev/null)"
assert_eq "0" "$(jq -r '.wrote | length' <<<"$out2")" "[dogfood] graduate re-run wrote 0"
assert_eq "1" "$(jq -r '.skipped_idempotent | length' <<<"$out2")" \
  "[dogfood] graduate re-run skipped"

# --- 24. glossary assemble after setup lds: lds.md skipped gracefully -------
# glossary assemble emits markdown to stdout by default; --output yields a
# JSON ledger we can inspect for the lds partial's skip-vs-include state.
out="$(WIP_ROOT="$workdir" bin/wip-plumbing glossary assemble \
  --output "$workdir/.wip/GLOSSARY.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[dogfood] glossary assemble ok"
assert_eq "1" "$(jq -r '[.partials_skipped[]? | select(.name == "lds.md")] | length' <<<"$out")" \
  "[dogfood] glossary lists lds partial as skipped"

test_summary
