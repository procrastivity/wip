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
# Vendored `setup agents` renders the flattened agents via wip_flatten_render,
# which resolves roles/ from $root/roles | $CLAUDE_PLUGIN_ROOT/roles | the
# WIP_ROLES_DIR seam. These cases drive setup against consumer tempdirs
# (WIP_ROOT=$workdir, no roles/), so point the renderer at the install's roles/
# via the documented seam — mirrors test-flatten-render.sh.
export WIP_ROLES_DIR="$PWD/roles"

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
mapfile -t F < <(jq -r '.error.kind, .error.path' <<<"$out")
assert_eq "missing-prereq" "${F[0]}" "missing prereq kind"
assert_eq "flake.nix" "${F[1]}" "missing prereq path"

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
  mapfile -t F < <(jq -r '.ok, (.wrote | length), (.refused | length)' <<<"$out")
  assert_eq "true" "${F[0]}" "[$verb] ok"
  wrote_n="${F[1]}"
  case "$verb" in
    deps) expected=2 ;;    # flake.nix + flake.lock
    direnv) expected=1 ;;  # .envrc
    hygiene) expected=1 ;; # .pre-commit-config.yaml
    release) expected=2 ;; # cliff.toml + CHANGELOG.md
    agents) expected=4 ;;  # vendored flattened agents: .claude/agents/wip/{4 roles}.md (ADR-0020 D1)
  esac
  assert_eq "$expected" "$wrote_n" "[$verb] wrote $expected files"
  assert_eq "0" "${F[2]}" "[$verb] no refusals"

  # Re-run idempotency
  out2="$(WIP_ROOT="$workdir" bin/wip-plumbing setup "$verb" 2>/dev/null)"
  mapfile -t F2 < <(jq -r '(.wrote | length), (.skipped_idempotent | length), .manifest_updated' <<<"$out2")
  assert_eq "0" "${F2[0]}" "[$verb] re-run wrote 0"
  assert_eq "$expected" "${F2[1]}" "[$verb] re-run skipped all"
  assert_eq "null" "${F2[2]}" "[$verb] re-run manifest no-op"
done

# --- 6. Feature flag flipping ------------------------------------------------
workdir="$tmp/flags"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup deps >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup direnv >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup release >/dev/null 2>&1
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1
# One manifest read after all flag-flipping setups — every flag is cumulative,
# so the end-state assert is equivalent to (stricter than) reading after each.
mapfile -t FL < <(yq -o=json '.' "$workdir/.wip.yaml" | jq -r '
  .features.direnv.enabled,
  .features.changelog.enabled,
  .features.orchestration.enabled,
  .features.orchestration.backend,
  .features.orchestration.source,
  (.features.solo // "null")')
assert_eq "true" "${FL[0]}" "direnv flag flipped"
assert_eq "true" "${FL[1]}" "changelog flag flipped"
assert_eq "true" "${FL[2]}" "orchestration enabled"
assert_eq "solo" "${FL[3]}" "orchestration backend=solo"
assert_eq "plugin" "${FL[4]}" "orchestration source=plugin"
# Solo block is NOT auto-created (consumer's decision per ADR-0007)
assert_eq "null" "${FL[5]}" "no auto features.solo block"

# --- 7. Sentinel post-check passes; doctor on tempdir is clean ---------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing doctor 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, .drift_count' <<<"$out")
assert_eq "true" "${F[0]}" "doctor ok after all setups"
assert_eq "0" "${F[1]}" "doctor drift 0"

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
mapfile -t F < <(jq -r '.error.kind, .error.paths[0]' <<<"$out")
assert_eq "content-drift" "${F[0]}" "content drift kind"
assert_eq "flake.nix" "${F[1]}" "content drift path"

# --- 9. --force overwrites drift; subsequent run is clean --------------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup deps --force 2>/dev/null)"
# wrote_forced contains flake.nix at least
mapfile -t F < <(jq -r '.ok, ([.wrote_forced[] | select(. == "flake.nix")] | length)' <<<"$out")
assert_eq "true" "${F[0]}" "force overwrite ok"
assert_eq "1" "${F[1]}" "force overwrote flake.nix"
# After force, byte-equal again
assert_cmp templates/setup/deps/flake.nix "$workdir/flake.nix" \
  "post-force flake.nix matches template"

# --- 10. flake.lock skip-if-present (never compare without --force) ----------
echo "drift_to_lock" >>"$workdir/flake.lock"
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup deps 2>/dev/null)"
# Lock should be in skipped, NOT refused (proves never-compare semantics)
mapfile -t F < <(jq -r '.ok, ([.skipped_idempotent[] | select(. == "flake.lock")] | length), ([.refused[] | select(. == "flake.lock")] | length)' <<<"$out")
assert_eq "true" "${F[0]}" "lock-style ok despite drift"
assert_eq "1" "${F[1]}" "flake.lock skipped on drift"
assert_eq "0" "${F[2]}" "flake.lock not refused"

# --- 11. --dry-run touches nothing ------------------------------------------
workdir="$tmp/dryrun"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing --dry-run setup deps 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, (.wrote | length)' <<<"$out")
assert_eq "true" "${F[0]}" "dry-run ok"
assert_eq "2" "${F[1]}" "dry-run ledger wrote=2"
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
# vendored flattened agents landed (ADR-0020 D1): the four role files under
# .claude/agents/wip/, and NO plugin tree (.claude-plugin/) or roles/ copied
# into the consumer.
for role in orchestrator coordinator researcher builder; do
  assert_file "$workdir/.claude/agents/wip/$role.md" \
    "round-trip .claude/agents/wip/$role.md present"
done
assert_absent "$workdir/.claude-plugin" "round-trip no .claude-plugin/ in consumer"
assert_absent "$workdir/roles" "round-trip no roles/ in consumer"

# --- 13. doctor on the round-trip tempdir → clean ---------------------------
out="$(WIP_ROOT="$workdir" bin/wip-plumbing doctor 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, .drift_count' <<<"$out")
assert_eq "true" "${F[0]}" "round-trip doctor ok"
assert_eq "0" "${F[1]}" "round-trip doctor drift 0"

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
# means the templates/setup/lds/ tree needs a refresh. The upstream
# distribution (layered-documentation-system/) is gitignored — absent on a
# fresh CI checkout, and there is no tracked second copy to diff against — so
# guard-skip when it is absent.
if [[ -d layered-documentation-system/maintenance ]]; then
  for m in audit refine sync update; do
    assert_cmp "templates/setup/lds/engineering/maintenance/$m.md" \
      "layered-documentation-system/maintenance/$m.md" \
      "lds maintenance/$m.md byte-equal to LDS distribution"
  done
else
  printf '  skip (CI: gitignored layered-documentation-system/ absent) — LDS-distribution fidelity (4 asserts)\n'
fi

# Seed manifest is yq-parseable + has the validator-required shape
mapfile -t M < <(yq -o=json '.' templates/setup/lds/engineering/.lds-manifest.yaml |
  jq -r '.metadata.schema_version, .metadata.status, (.entries | length)')
assert_eq "1.0.0" "${M[0]}" "lds seed manifest schema_version=1.0.0"
assert_eq "approved" "${M[1]}" "lds seed manifest status=approved"
assert_eq "0" "${M[2]}" "lds seed manifest entries=[]"

# --- 16. setup lds (full mode) writes 13 files; idempotent on re-run ---------
workdir="$tmp/lds-full"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, (.wrote | length), (.refused | length), .sentinel, .sentinel_present' <<<"$out")
assert_eq "true" "${F[0]}" "[lds] ok"
assert_eq "13" "${F[1]}" "[lds] wrote 13 files"
assert_eq "0" "${F[2]}" "[lds] no refusals"
assert_eq "engineering/.lds-manifest.yaml" "${F[3]}" "[lds] sentinel path"
assert_eq "true" "${F[4]}" "[lds] sentinel present"

# Layer dirs all exist with .gitkeep
for layer in decisions product architecture specs reference behaviors implementation appendices; do
  assert_file "$workdir/engineering/$layer/.gitkeep" "[lds] $layer/.gitkeep present"
done
assert_file "$workdir/engineering/maintenance/audit.md" "[lds] maintenance/audit.md present"
assert_file "$workdir/engineering/.lds-manifest.yaml" "[lds] sentinel manifest on disk"

# Re-run idempotency
out2="$(WIP_ROOT="$workdir" bin/wip-plumbing setup lds 2>/dev/null)"
mapfile -t F2 < <(jq -r '(.wrote | length), (.skipped_idempotent | length), .manifest_updated' <<<"$out2")
assert_eq "0" "${F2[0]}" "[lds] re-run wrote 0"
assert_eq "13" "${F2[1]}" "[lds] re-run skipped all 13"
assert_eq "null" "${F2[2]}" "[lds] re-run manifest no-op"

# --- 17. setup lds flips features.lds.{enabled, root: engineering} ----------
mapfile -t FL < <(yq -o=json '.' "$workdir/.wip.yaml" |
  jq -r '.features.lds.enabled, .features.lds.root')
assert_eq "true" "${FL[0]}" "[lds] features.lds.enabled flipped"
assert_eq "engineering" "${FL[1]}" "[lds] features.lds.root set"

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
mapfile -t F < <(jq -r '.error.kind, ([.error.paths[] | select(. == "engineering/maintenance/audit.md")] | length)' <<<"$out")
assert_eq "content-drift" "${F[0]}" "[lds] drift kind"
assert_eq "1" "${F[1]}" "[lds] drift path listed"
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
mapfile -t F < <(jq -r '.ok, (.wrote | length), .wrote[0]' <<<"$out")
assert_eq "true" "${F[0]}" "[lds --sentinel-only] ok"
assert_eq "1" "${F[1]}" "[lds --sentinel-only] wrote 1 file"
assert_eq "engineering/.lds-manifest.yaml" "${F[2]}" \
  "[lds --sentinel-only] wrote the manifest"
assert_file "$workdir/engineering/.lds-manifest.yaml" \
  "[lds --sentinel-only] sentinel on disk"
assert_absent "$workdir/engineering/decisions/.gitkeep" \
  "[lds --sentinel-only] no decisions/.gitkeep"
assert_absent "$workdir/engineering/maintenance/audit.md" \
  "[lds --sentinel-only] no maintenance/audit.md"
# Flags still flip
mapfile -t FL < <(yq -o=json '.' "$workdir/.wip.yaml" |
  jq -r '.features.lds.enabled, .features.lds.root')
assert_eq "true" "${FL[0]}" "[lds --sentinel-only] features.lds.enabled flipped"
assert_eq "engineering" "${FL[1]}" "[lds --sentinel-only] features.lds.root set"

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
mapfile -t F < <(jq -r '.ok, (.wrote | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[lds --dry-run] ok"
assert_eq "13" "${F[1]}" "[lds --dry-run] ledger 13"
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
mapfile -t F < <(jq -r '.error.kind, .error.path' <<<"$out")
assert_eq "lds-already-installed-elsewhere" "${F[0]}" "[lds elsewhere] kind"
assert_eq "docs" "${F[1]}" "[lds elsewhere] path = existing root"

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
mapfile -t F < <(jq -r '.ok, .target' <<<"$out")
assert_eq "true" "${F[0]}" "[dogfood] graduate ok"
assert_eq "engineering/decisions/0001-followup-dogfood.md" "${F[1]}" \
  "[dogfood] auto-numbered target"
assert_file "$workdir/engineering/decisions/0001-followup-dogfood.md" \
  "[dogfood] graduated file on disk"
# Re-run is idempotent
out2="$(WIP_ROOT="$workdir" bin/wip-plumbing graduate "$workdir/scratch/dogfood.md" 2>/dev/null)"
mapfile -t F2 < <(jq -r '(.wrote | length), (.skipped_idempotent | length)' <<<"$out2")
assert_eq "0" "${F2[0]}" "[dogfood] graduate re-run wrote 0"
assert_eq "1" "${F2[1]}" "[dogfood] graduate re-run skipped"

# --- 24. glossary assemble after setup lds: lds.md included -----------------
# glossary assemble emits markdown to stdout by default; --output yields a
# JSON ledger we can inspect for the lds partial's skip-vs-include state.
# lds.md now ships (step-16), so an lds install includes it rather than
# skipping it as a future-row.
out="$(WIP_ROOT="$workdir" bin/wip-plumbing glossary assemble \
  --output "$workdir/.wip/GLOSSARY.md" 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, ([.partials_included[]? | select(.name == "lds.md")] | length), ([.partials_skipped[]? | select(.name == "lds.md")] | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[dogfood] glossary assemble ok"
assert_eq "1" "${F[1]}" "[dogfood] glossary lists lds partial as included"
assert_eq "0" "${F[2]}" "[dogfood] lds partial not in skipped"

test_summary
