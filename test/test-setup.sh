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

# Canonical vendored command count, DERIVED from the template glob (step-06 /
# ADR-0015 amend) — never hardcoded, so adding/removing a command can't silently
# drift the install count without a corresponding template change. The four agent
# roles stay a fixed literal (the role set is closed); only the command N derives.
cmd_count="$(find templates/setup/agents/commands -maxdepth 1 -name '*.md' | wc -l | tr -d ' ')"

# --- 1. Template fidelity vs the live repo (step-09 byte-derivation). ----------
assert_cmp templates/setup/deps/flake.nix flake.nix \
  "deps/flake.nix is byte-equal to flake.nix"
assert_cmp templates/setup/deps/flake.lock flake.lock \
  "deps/flake.lock is byte-equal to flake.lock"
assert_cmp templates/setup/direnv/.envrc .envrc \
  "direnv/.envrc is byte-equal to .envrc"
# The consumer hygiene template and the live authoring config INTENTIONALLY
# diverge by exactly one hook: `wip-active-backend` (ADR-0013 step-04 D6/OQ4)
# gates the generated roles/backends/active.md pointer, which only the authoring
# (`source: plugin`) repo owns — a vendored consumer has no active.md/roles/ to
# gate (the check no-ops there per D4), so the hook lives in the plugin config
# ONLY, never the installed template. Assert that intent directly, then keep the
# strong drift guard: the template equals the live config with that one hook
# elided (no OTHER drift permitted).
assert_not_grep 'wip-active-backend' templates/setup/hygiene/.pre-commit-config.yaml \
  "hygiene template omits the plugin-only wip-active-backend hook (OQ4/D6)"
assert_grep 'wip-active-backend' .pre-commit-config.yaml \
  "live config carries the plugin-only wip-active-backend hook (D6)"
# Live config minus the trailing plugin-only hook (and its blank separator) ==
# the consumer template, byte-for-byte. The awk stops at the hook and drops the
# one blank line preceding it (the hook is appended at EOF).
live_minus_hook="$tmp/pre-commit-live-minus-hook.yaml"
awk '
  /^[[:space:]]*- id: wip-active-backend$/ { exit }
  NR > 1 { print buf }
  { buf = $0 }
  END { if (buf !~ /^[[:space:]]*$/) print buf }
' .pre-commit-config.yaml >"$live_minus_hook"
assert_cmp templates/setup/hygiene/.pre-commit-config.yaml "$live_minus_hook" \
  "hygiene template == live config minus the plugin-only wip-active-backend hook"

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
    # vendored flattened agents (.claude/agents/wip/{4 roles}.md, ADR-0020 D1)
    # PLUS the relocated wip slash-commands (.claude/commands/wip/*.md, step-06).
    # 4 is the closed role set (literal); cmd_count derives from the template glob.
    agents) expected=$((4 + cmd_count)) ;;
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
assert_eq "vendored" "${FL[4]}" "orchestration source=vendored"
# Solo block is NOT auto-created (consumer's decision per ADR-0007)
assert_eq "null" "${FL[5]}" "no auto features.solo block"

# --- 6a. Vendored slash-commands relocated to .claude/commands/wip/ (step-06) -
# A vendored `setup agents` install (the §5 verb-loop workdir $tmp/v-agents) now
# copies every templates/setup/agents/commands/<name>.md VERBATIM into
# .claude/commands/wip/<name>.md (the wip/ subdir yields the /wip:<name> colon
# invocation, D1/D2). Pure resolver-swap (D3): same filename + bytes, only the
# destination dir differs. The expected set is DERIVED from the template glob
# (set-parity, D4) so a dropped command fails here rather than shipping silently.
# NB: the /wip:<name> INVOCATION is hand-verified against the live Claude Code
# runtime; the harness proves only the FILE LAYOUT.
cmd_workdir="$tmp/v-agents"
installed_cmds=0
for cmd_tmpl in templates/setup/agents/commands/*.md; do
  name="$(basename -- "$cmd_tmpl")"
  installed=".claude/commands/wip/$name"
  assert_file "$cmd_workdir/$installed" "[agents commands] $installed present"
  assert_cmp "$cmd_tmpl" "$cmd_workdir/$installed" \
    "[agents commands] $installed byte-equal to template (pure resolver-swap)"
  installed_cmds=$((installed_cmds + 1))
done
assert_eq "$cmd_count" "$installed_cmds" \
  "[agents commands] set-parity: installed every template command (derived count)"
# No stray files in the vendored command dir beyond the template set.
assert_eq "$cmd_count" \
  "$(find "$cmd_workdir/.claude/commands/wip" -maxdepth 1 -name '*.md' | wc -l | tr -d ' ')" \
  "[agents commands] no extra files in .claude/commands/wip/"

# --- 6b. setup agents --source plugin → vendors nothing (D-03.3) -------------
# The plugin source mode writes zero files (agents resolve by the bare
# `wip-<role>` name from the globally-enabled wip plugin); the manifest flip
# still records enabled=true and flips source to `plugin`.
workdir="$tmp/agents-plugin"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --source plugin 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, (.wrote | length), (.skipped_idempotent | length), (.refused | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[agents --source plugin] ok"
assert_eq "0" "${F[1]}" "[agents --source plugin] wrote nothing"
assert_eq "0" "${F[2]}" "[agents --source plugin] skipped nothing"
assert_eq "0" "${F[3]}" "[agents --source plugin] refused nothing"
assert_absent "$workdir/.claude/agents" "[agents --source plugin] no .claude/agents/ written"
assert_absent "$workdir/.claude/commands" "[agents --source plugin] no .claude/commands/ written"
mapfile -t FP < <(yq -o=json '.' "$workdir/.wip.yaml" |
  jq -r '.features.orchestration.enabled, .features.orchestration.source')
assert_eq "true" "${FP[0]}" "[agents --source plugin] orchestration enabled"
assert_eq "plugin" "${FP[1]}" "[agents --source plugin] source=plugin"

# --- 6c. Conservative-write guard: foreign root .claude-plugin/plugin.json ----
# (D-03.4 / Outcome 4) The vendored path refuses to install over a host plugin's
# root manifest it does not own (.name != "wip"): exit 4 with the
# foreign-plugin-manifest kind, writing NOTHING. Two controls follow: the SAME
# repo with --source plugin succeeds (the guard is vendored-path-only), and a
# wip-owned (name: wip) root manifest does NOT trip the guard.
workdir="$tmp/agents-foreign"
mkdir -p "$workdir/.claude-plugin"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
printf '{ "name": "clast", "version": "0.0.0" }\n' >"$workdir/.claude-plugin/plugin.json"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[agents foreign-manifest] vendored exit 4"
mapfile -t F < <(jq -r '.error.kind, .error.paths[0]' <<<"$out")
assert_eq "foreign-plugin-manifest" "${F[0]}" "[agents foreign-manifest] guard kind"
assert_eq ".claude-plugin/plugin.json" "${F[1]}" "[agents foreign-manifest] names the host manifest"
assert_absent "$workdir/.claude/agents" "[agents foreign-manifest] vendored wrote nothing"
assert_absent "$workdir/.claude/commands" "[agents foreign-manifest] vendored wrote no commands either"

# Control (a): the SAME foreign repo with --source plugin succeeds — the guard
# is vendored-path-only and the no-vendor path writes zero files.
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --source plugin 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, (.wrote | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[agents foreign-manifest] --source plugin succeeds (guard not fired)"
assert_eq "0" "${F[1]}" "[agents foreign-manifest] --source plugin wrote nothing"
assert_absent "$workdir/.claude/agents" "[agents foreign-manifest] --source plugin still no .claude/agents/"
assert_eq "plugin" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" \
  "[agents foreign-manifest] --source plugin recorded source=plugin"

# Control (b): a wip-owned (name: wip) root manifest does NOT trip the guard —
# the vendored write proceeds and lands the four role files.
workdir="$tmp/agents-wip-owned"
mkdir -p "$workdir/.claude-plugin"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
printf '{ "name": "wip", "version": "0.0.0" }\n' >"$workdir/.claude-plugin/plugin.json"
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, (.wrote | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[agents wip-owned manifest] vendored write proceeds"
assert_eq "$((4 + cmd_count))" "${F[1]}" "[agents wip-owned manifest] wrote 4 roles + commands"
for role in orchestrator coordinator researcher builder; do
  assert_file "$workdir/.claude/agents/wip/$role.md" \
    "[agents wip-owned manifest] .claude/agents/wip/$role.md present"
done

# --- 6d. setup agents --check — read-only drift gate (Step 5 / Chunk 1) ------
# The agent-side analog of ADR-0015's `sync-agents-commands --check`, gated by
# ADR-0020's D6 round-trip determinism: a fresh re-render of the four roles must
# equal the installed bytes (step-08's D5 disclaimer is present on BOTH sides via
# the SAME renderer — never special-cased). `--check` writes NOTHING and never
# flips the manifest, in every branch.

# (i) Clean vendored install → --check exit 0, drift:[], checked names 4 roles,
#     and the installed tree is byte-unchanged (the ADR-0020 round-trip proof).
workdir="$tmp/agents-check-clean"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1
pre_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[agents --check clean] exit 0"
mapfile -t F < <(jq -r '.ok, (.checked | length), (.drift | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[agents --check clean] ok"
assert_eq "$((4 + cmd_count))" "${F[1]}" "[agents --check clean] checked 4 roles + commands"
assert_eq "0" "${F[2]}" "[agents --check clean] drift:[]"
post_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
assert_eq "$pre_sum" "$post_sum" "[agents --check clean] wrote nothing (round-trip byte-stable)"

# (ii) Mutate one installed role → --check exit 4 agents-drift names the role,
#      and the installed tree stays byte-unchanged (read-only — no repair write).
echo "# drift injected" >>"$workdir/.claude/agents/wip/coordinator.md"
mut_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[agents --check drifted] exit 4"
mapfile -t F < <(jq -r '.error.kind, ([.error.paths[] | select(. == ".claude/agents/wip/coordinator.md")] | length)' <<<"$out")
assert_eq "agents-drift" "${F[0]}" "[agents --check drifted] kind agents-drift"
assert_eq "1" "${F[1]}" "[agents --check drifted] names the drifted role path"
post_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
assert_eq "$mut_sum" "$post_sum" "[agents --check drifted] read-only (no repair write)"

# (iii) Remove one installed role → --check exit 4 agents-drift names it missing
#       and does NOT re-create it (read-only).
rm -f "$workdir/.claude/agents/wip/builder.md"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[agents --check missing] exit 4"
mapfile -t F < <(jq -r '.error.kind, ([.error.paths[] | select(. == ".claude/agents/wip/builder.md")] | length)' <<<"$out")
assert_eq "agents-drift" "${F[0]}" "[agents --check missing] kind agents-drift"
assert_eq "1" "${F[1]}" "[agents --check missing] names the missing role path"
assert_absent "$workdir/.claude/agents/wip/builder.md" "[agents --check missing] not re-created"

# (iv) --source plugin install → --check exit 0 no-op (checked:[]; nothing
#      vendored to verify — branches on the manifest source, not the flag).
workdir="$tmp/agents-check-plugin"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup agents --source plugin >/dev/null 2>&1
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[agents --check plugin] exit 0 no-op"
mapfile -t F < <(jq -r '.ok, (.checked | length), (.drift | length)' <<<"$out")
assert_eq "true" "${F[0]}" "[agents --check plugin] ok"
assert_eq "0" "${F[1]}" "[agents --check plugin] checked:[] (nothing vendored)"
assert_eq "0" "${F[2]}" "[agents --check plugin] drift:[]"
assert_absent "$workdir/.claude/agents" "[agents --check plugin] still wrote no .claude/agents/"

# (v) Repeat --check on a clean vendored install — read-only and idempotent;
#     write-flags (--force) and --dry-run are inert under --check (Q-05.1 lean).
workdir="$tmp/agents-check-repeat"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1
pre_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
for i in 1 2; do
  set +e
  out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
  rc=$?
  set -e
  assert_eq "0" "$rc" "[agents --check repeat #$i] exit 0"
  assert_eq "0" "$(jq -r '.drift | length' <<<"$out")" "[agents --check repeat #$i] drift:[]"
done
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check --force 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[agents --check --force] exit 0 (force inert under --check)"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing --dry-run setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[agents --check --dry-run] exit 0 (dry-run inert under --check)"
post_sum="$(find "$workdir/.claude/agents/wip" -type f -exec cksum {} \; | sort)"
assert_eq "$pre_sum" "$post_sum" "[agents --check repeat] wrote nothing across repeats"

# --- 6e. setup agents --check covers the relocated commands (step-06 / D6) ---
# The unified vendored-drift gate also `cmp`s each installed .claude/commands/wip/
# <name>.md vs its template. A FRESH install isolates command drift from the
# agent drift exercised above: mutating or removing ONE command alone trips rc 4
# (kind agents-drift) and names that command path; the gate stays read-only.
workdir="$tmp/agents-check-cmds"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup agents >/dev/null 2>&1
# Pick the first command deterministically (sorted glob) as the drift target.
drift_cmd=""
for cmd_tmpl in templates/setup/agents/commands/*.md; do
  drift_cmd="$(basename -- "$cmd_tmpl")"
  break
done
drift_cmd_rel=".claude/commands/wip/$drift_cmd"

# (i) Mutate one installed command → exit 4 agents-drift names that command path.
echo "# drift injected" >>"$workdir/$drift_cmd_rel"
mut_sum="$(find "$workdir/.claude/commands/wip" -type f -exec cksum {} \; | sort)"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[agents --check cmd-drifted] exit 4"
mapfile -t F < <(jq -r --arg p "$drift_cmd_rel" '.error.kind, ([.error.paths[] | select(. == $p)] | length)' <<<"$out")
assert_eq "agents-drift" "${F[0]}" "[agents --check cmd-drifted] kind agents-drift"
assert_eq "1" "${F[1]}" "[agents --check cmd-drifted] names the drifted command path"
post_sum="$(find "$workdir/.claude/commands/wip" -type f -exec cksum {} \; | sort)"
assert_eq "$mut_sum" "$post_sum" "[agents --check cmd-drifted] read-only (no repair write)"

# (ii) Remove one installed command → exit 4 agents-drift names it missing; the
#      gate does NOT re-create it (read-only).
rm -f "$workdir/$drift_cmd_rel"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[agents --check cmd-missing] exit 4"
mapfile -t F < <(jq -r --arg p "$drift_cmd_rel" '.error.kind, ([.error.paths[] | select(. == $p)] | length)' <<<"$out")
assert_eq "agents-drift" "${F[0]}" "[agents --check cmd-missing] kind agents-drift"
assert_eq "1" "${F[1]}" "[agents --check cmd-missing] names the missing command path"
assert_absent "$workdir/$drift_cmd_rel" "[agents --check cmd-missing] not re-created"

# --- 6f. setup agents --migrate — old-footprint → clean transition ----------
# (ADR-0020 migration path, D2–D6 / Chunk 3). Seed the REAL 16-file old
# plugin-tree footprint by copying templates/setup/agents/** verbatim into the
# repo root (that IS what the old `wip_setup_walk_template_tree` walk did) plus
# the F2 `source: plugin` mislabel. Migration cleans the wip-owned files, lands
# the flattened install, and the result byte-matches a fresh flattened install.
seed_old_footprint() { # <workdir> — 16-file footprint + source:plugin mislabel
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
# .claude-plugin/{plugin.json,README} (2) + agents/README (1) + N cmds + 4 roles = 16
migrate_footprint_n=$((2 + 1 + cmd_count + 4))
migrate_write_n=$((4 + cmd_count)) # flattened .claude/agents/wip/{4} + .claude/commands/wip/{N} = 13

# (i) --dry-run: plan the transition, touch NOTHING (D6).
workdir="$tmp/migrate-dry"
seed_old_footprint "$workdir"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate --dry-run 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate --dry-run] exit 0"
mapfile -t F < <(jq -r '.dry_run, (.would_delete | length), (.would_write | length), (.would_warn | length), .source' <<<"$out")
assert_eq "true" "${F[0]}" "[migrate --dry-run] dry_run:true"
assert_eq "$migrate_footprint_n" "${F[1]}" "[migrate --dry-run] would_delete lists the 16 owned paths"
assert_eq "$migrate_write_n" "${F[2]}" "[migrate --dry-run] would_write lists the 13 flattened paths"
assert_eq "0" "${F[3]}" "[migrate --dry-run] would_warn empty (clean vendored footprint)"
assert_eq "vendored" "${F[4]}" "[migrate --dry-run] plans the vendored end-state"
assert_file "$workdir/.claude-plugin/plugin.json" "[migrate --dry-run] footprint still on disk"
assert_absent "$workdir/.claude" "[migrate --dry-run] no .claude/ written"
assert_eq "plugin" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" \
  "[migrate --dry-run] manifest unchanged (still source: plugin)"

# (ii) Real --migrate: delete the 16 owned files, rmdir empty parents, land the
#      13 flattened files, flip source: vendored.
workdir="$tmp/migrate-real"
seed_old_footprint "$workdir"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate] exit 0"
mapfile -t F < <(jq -r '.migrate, (.deleted | length), (.wrote | length), (.skipped_idempotent | length), (.warned | length), .manifest_updated, .source, .migrated' <<<"$out")
assert_eq "true" "${F[0]}" "[migrate] migrate:true"
assert_eq "$migrate_footprint_n" "${F[1]}" "[migrate] deleted the 16 owned files"
assert_eq "$migrate_write_n" "${F[2]}" "[migrate] wrote the 13 flattened files"
assert_eq "0" "${F[3]}" "[migrate] skipped nothing (fresh vendored write)"
assert_eq "0" "${F[4]}" "[migrate] warned nothing (clean footprint)"
assert_eq ".wip.yaml" "${F[5]}" "[migrate] manifest_updated"
assert_eq "vendored" "${F[6]}" "[migrate] source: vendored"
assert_eq "true" "${F[7]}" "[migrate] migrated:true"
assert_absent "$workdir/.claude-plugin" "[migrate] empty .claude-plugin/ removed"
assert_absent "$workdir/agents" "[migrate] empty root agents/ removed"
assert_absent "$workdir/commands" "[migrate] empty root commands/ removed"
for role in orchestrator coordinator researcher builder; do
  assert_file "$workdir/.claude/agents/wip/$role.md" "[migrate] .claude/agents/wip/$role.md present"
done
for cmd_tmpl in templates/setup/agents/commands/*.md; do
  name="$(basename -- "$cmd_tmpl")"
  assert_file "$workdir/.claude/commands/wip/$name" "[migrate] .claude/commands/wip/$name present"
done
assert_eq "vendored" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" \
  "[migrate] manifest source flipped to vendored"

# (ii-b) INVARIANT: the migrated .claude/ tree byte-matches a control FRESH
#        flattened install (diff the two trees → identical).
fresh="$tmp/migrate-fresh"
mkdir -p "$fresh"
WIP_ROOT="$fresh" bin/wip-plumbing init >/dev/null
WIP_ROOT="$fresh" bin/wip-plumbing setup agents >/dev/null 2>&1
set +e
diff -r "$workdir/.claude" "$fresh/.claude" >/dev/null 2>&1
dr=$?
set -e
assert_eq "0" "$dr" "[migrate] migrated .claude/ byte-matches a fresh flattened install"

# (iii) Idempotence (D6): a second --migrate deletes nothing, the vendored write
#       is all-skip, migrated:false.
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate idempotent] exit 0"
mapfile -t F < <(jq -r '(.deleted | length), (.wrote | length), (.skipped_idempotent | length), .migrated' <<<"$out")
assert_eq "0" "${F[0]}" "[migrate idempotent] deleted nothing"
assert_eq "0" "${F[1]}" "[migrate idempotent] wrote nothing"
assert_eq "$migrate_write_n" "${F[2]}" "[migrate idempotent] all 13 skipped_idempotent"
assert_eq "false" "${F[3]}" "[migrate idempotent] migrated:false (already clean)"

# (iv) --check is clean on the migrated repo (passes the NEW drift gate).
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --check 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate → --check] exit 0 clean"
assert_eq "0" "$(jq -r '.drift | length' <<<"$out")" "[migrate → --check] drift:[]"

# (v) Protection — host-plugin (F1): a foreign root plugin.json (name != wip) is
#     NEVER deleted. Migration cleans wip's owned agents/commands, LEAVES the
#     foreign manifest (warned), writes no .claude/agents, sets source: plugin.
workdir="$tmp/migrate-host-plugin"
seed_old_footprint "$workdir"
printf '{ "name": "clast", "version": "0.0.0" }\n' >"$workdir/.claude-plugin/plugin.json"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate host-plugin] exit 0"
mapfile -t F < <(jq -r '(.deleted | length), (.wrote | length), .source, ([.warned[] | select(.path == ".claude-plugin/plugin.json")] | length)' <<<"$out")
assert_eq "$((migrate_footprint_n - 1))" "${F[0]}" "[migrate host-plugin] deleted 15 owned (foreign plugin.json excluded)"
assert_eq "0" "${F[1]}" "[migrate host-plugin] wrote no vendored files"
assert_eq "plugin" "${F[2]}" "[migrate host-plugin] source: plugin end-state"
assert_eq "1" "${F[3]}" "[migrate host-plugin] foreign plugin.json is in warned"
assert_file "$workdir/.claude-plugin/plugin.json" "[migrate host-plugin] foreign manifest survives"
assert_eq "clast" "$(jq -r '.name' "$workdir/.claude-plugin/plugin.json")" \
  "[migrate host-plugin] foreign manifest untouched (name: clast)"
assert_absent "$workdir/.claude/agents" "[migrate host-plugin] no vendored .claude/agents/"
assert_absent "$workdir/agents/orchestrator.md" "[migrate host-plugin] wip role agents cleaned"
assert_eq "plugin" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" \
  "[migrate host-plugin] manifest source: plugin"

# (vi) Protection — consumer-authored files survive; their parent dirs are NOT
#      removed (non-empty). A commands/mine.md (no template match) and an
#      agents/my-agent.md (no wip-* frontmatter) are never classified as owned.
workdir="$tmp/migrate-consumer"
seed_old_footprint "$workdir"
printf '# my own command\n' >"$workdir/commands/mine.md"
printf -- '---\nname: my-agent\n---\nbody\n' >"$workdir/agents/my-agent.md"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate consumer] exit 0"
assert_eq "$migrate_footprint_n" "$(jq -r '.deleted | length' <<<"$out")" \
  "[migrate consumer] deleted only the 16 wip-owned files"
assert_file "$workdir/commands/mine.md" "[migrate consumer] commands/mine.md survives"
assert_file "$workdir/agents/my-agent.md" "[migrate consumer] agents/my-agent.md survives"
assert_eq "yes" "$([[ -d "$workdir/commands" ]] && echo yes || echo no)" \
  "[migrate consumer] commands/ dir kept (non-empty)"
assert_eq "yes" "$([[ -d "$workdir/agents" ]] && echo yes || echo no)" \
  "[migrate consumer] agents/ dir kept (non-empty)"

# (vii) Protection — deliberate plugin repo (D5): source: plugin, no footprint →
#       --migrate is a no-op (nothing deleted/written, source stays plugin).
workdir="$tmp/migrate-deliberate-plugin"
mkdir -p "$workdir"
WIP_ROOT="$workdir" bin/wip-plumbing init >/dev/null
WIP_ROOT="$workdir" bin/wip-plumbing setup agents --source plugin >/dev/null 2>&1
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate deliberate-plugin] exit 0"
mapfile -t F < <(jq -r '(.deleted | length), (.wrote | length), .manifest_updated, .source, .migrated' <<<"$out")
assert_eq "0" "${F[0]}" "[migrate deliberate-plugin] deleted nothing"
assert_eq "0" "${F[1]}" "[migrate deliberate-plugin] wrote nothing"
assert_eq "null" "${F[2]}" "[migrate deliberate-plugin] manifest untouched"
assert_eq "plugin" "${F[3]}" "[migrate deliberate-plugin] source stays plugin"
assert_eq "false" "${F[4]}" "[migrate deliberate-plugin] migrated:false (no-op)"
assert_eq "plugin" "$(yq -r '.features.orchestration.source' "$workdir/.wip.yaml")" \
  "[migrate deliberate-plugin] manifest source: plugin unchanged"
assert_absent "$workdir/.claude/agents" "[migrate deliberate-plugin] no vendored .claude/agents/"

# (viii) Protection — stray root roles/ + active.md (never part of the real
#        footprint, OQ-07.1) are warned, never deleted.
workdir="$tmp/migrate-stray"
seed_old_footprint "$workdir"
mkdir -p "$workdir/roles"
printf 'shared\n' >"$workdir/roles/shared.md"
printf 'active\n' >"$workdir/active.md"
set +e
out="$(WIP_ROOT="$workdir" bin/wip-plumbing setup agents --migrate 2>/dev/null)"
rc=$?
set -e
assert_eq "0" "$rc" "[migrate stray] exit 0"
mapfile -t F < <(jq -r '([.warned[] | select(.path == "roles")] | length), ([.warned[] | select(.path == "active.md")] | length)' <<<"$out")
assert_eq "1" "${F[0]}" "[migrate stray] roles/ is warned"
assert_eq "1" "${F[1]}" "[migrate stray] active.md is warned"
assert_file "$workdir/roles/shared.md" "[migrate stray] roles/ survives (not deleted)"
assert_file "$workdir/active.md" "[migrate stray] active.md survives (not deleted)"

# --- 7. Sentinel post-check passes; doctor on a plugin-mode tempdir is clean -
# Run against the §6b --source plugin install: doctor stays clean even though
# the no-vendor path wrote no agents (orchestration carries no sentinel).
workdir="$tmp/agents-plugin"
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
# `setup hygiene` installs the TEMPLATE verbatim; assert the round-trip against
# it (not the live config, which intentionally carries the extra plugin-only
# wip-active-backend hook — OQ4/D6, see the fidelity block near the top).
assert_cmp "$workdir/.pre-commit-config.yaml" templates/setup/hygiene/.pre-commit-config.yaml \
  "round-trip .pre-commit-config.yaml == hygiene template"
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

# --- 25. Config-echo backend verbs: solo / forge / issue-tracker (ADR-0021) ---
# Pure .wip.yaml feature writers — no template files, no sentinel. The feature
# key is the ledger unit: a write reports it under `wrote`, an idempotent re-run
# under `skipped_idempotent`.
cfg="$tmp/cfg"
mkdir -p "$cfg"
WIP_ROOT="$cfg" bin/wip-plumbing init >/dev/null

# setup solo (bare) → solo:{enabled:true}, no agent_tier_policy (never defaulted).
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup solo 2>/dev/null)"
mapfile -t F < <(jq -r '.ok, .verb, .feature, (.wrote | join(",")), (.skipped_idempotent | length), .manifest_updated, .sentinel' <<<"$out")
assert_eq "true" "${F[0]}" "[solo] ok"
assert_eq "setup solo" "${F[1]}" "[solo] verb"
assert_eq "solo" "${F[2]}" "[solo] feature"
assert_eq "features.solo" "${F[3]}" "[solo] wrote lists the feature key"
assert_eq "0" "${F[4]}" "[solo] nothing skipped on first write"
assert_eq ".wip.yaml" "${F[5]}" "[solo] manifest_updated"
assert_eq "null" "${F[6]}" "[solo] no sentinel"
assert_eq "true" "$(yq -r '.features.solo.enabled' "$cfg/.wip.yaml")" "[solo] enabled:true written"
assert_eq "null" "$(yq -r '.features.solo.agent_tier_policy' "$cfg/.wip.yaml")" "[solo] no tier policy defaulted"

# setup solo (re-run) → idempotent: skipped_idempotent, manifest_updated null.
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup solo 2>/dev/null)"
mapfile -t F < <(jq -r '(.wrote | length), (.skipped_idempotent | join(",")), .manifest_updated' <<<"$out")
assert_eq "0" "${F[0]}" "[solo] re-run wrote nothing"
assert_eq "features.solo" "${F[1]}" "[solo] re-run skipped_idempotent lists the feature"
assert_eq "null" "${F[2]}" "[solo] re-run manifest noop"

# setup solo --force-tier / --fallback-tool → nested agent_tier_policy written.
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup solo --force-tier large --fallback-tool Claude 2>/dev/null)"
assert_eq "features.solo" "$(jq -r '.wrote | join(",")' <<<"$out")" "[solo] tier flags write the feature"
assert_eq "large" "$(yq -r '.features.solo.agent_tier_policy.force_tier' "$cfg/.wip.yaml")" "[solo] force_tier written"
assert_eq "Claude" "$(yq -r '.features.solo.agent_tier_policy.fallback_tool' "$cfg/.wip.yaml")" "[solo] fallback_tool written"
# A bare re-run PRESERVES the existing tier policy (merge, not clobber).
WIP_ROOT="$cfg" bin/wip-plumbing setup solo >/dev/null 2>&1
assert_eq "large" "$(yq -r '.features.solo.agent_tier_policy.force_tier' "$cfg/.wip.yaml")" "[solo] bare re-run preserves tier policy"
# Policy values are string fields, even when a tool name happens to be numeric.
WIP_ROOT="$cfg" bin/wip-plumbing setup solo --fallback-tool 456 >/dev/null 2>&1
assert_eq "!!str" "$(yq -r '.features.solo.agent_tier_policy.fallback_tool | tag' "$cfg/.wip.yaml")" "[solo] fallback_tool remains a string"
assert_eq "456" "$(yq -r '.features.solo.agent_tier_policy.fallback_tool' "$cfg/.wip.yaml")" "[solo] numeric-looking fallback_tool preserved"

# setup forge (bare) → forge:{enabled:true}, NO backend (regression guard, D3).
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup forge 2>/dev/null)"
assert_eq "features.forge" "$(jq -r '.wrote | join(",")' <<<"$out")" "[forge] wrote the feature"
assert_eq "true" "$(yq -r '.features.forge.enabled' "$cfg/.wip.yaml")" "[forge] enabled:true written"
assert_eq "null" "$(yq -r '.features.forge.backend' "$cfg/.wip.yaml")" "[forge] bare setup writes no backend"

# setup forge glab → optional positional pins the backend (D3/D4).
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup forge glab 2>/dev/null)"
assert_eq "features.forge" "$(jq -r '.wrote | join(",")' <<<"$out")" "[forge] glab pin wrote the feature"
assert_eq "glab" "$(yq -r '.features.forge.backend' "$cfg/.wip.yaml")" "[forge] backend:glab written"
assert_eq "true" "$(yq -r '.features.forge.enabled' "$cfg/.wip.yaml")" "[forge] enabled stays true with backend"

# setup forge gh → both CLI names accepted (D4).
WIP_ROOT="$cfg" bin/wip-plumbing setup forge gh >/dev/null 2>&1
assert_eq "gh" "$(yq -r '.features.forge.backend' "$cfg/.wip.yaml")" "[forge] backend:gh written"

# Re-set to glab, then re-run identically → idempotent.
WIP_ROOT="$cfg" bin/wip-plumbing setup forge glab >/dev/null 2>&1
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup forge glab 2>/dev/null)"
mapfile -t F < <(jq -r '(.wrote | length), (.skipped_idempotent | join(",")), .manifest_updated' <<<"$out")
assert_eq "0" "${F[0]}" "[forge] idempotent re-run wrote nothing"
assert_eq "features.forge" "${F[1]}" "[forge] idempotent re-run skipped_idempotent"
assert_eq "null" "${F[2]}" "[forge] idempotent re-run manifest noop"

# Bare `setup forge` after a pin PRESERVES the backend (D5 merge, not reset).
WIP_ROOT="$cfg" bin/wip-plumbing setup forge >/dev/null 2>&1
assert_eq "glab" "$(yq -r '.features.forge.backend' "$cfg/.wip.yaml")" "[forge] bare re-run preserves pinned backend"

# setup issue-tracker <backend> → issue-tracker:{enabled:true, backend:...}.
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup issue-tracker linear 2>/dev/null)"
mapfile -t F < <(jq -r '.feature, (.wrote | join(","))' <<<"$out")
assert_eq "issue-tracker" "${F[0]}" "[tracker] hyphenated feature key"
assert_eq "features.issue-tracker" "${F[1]}" "[tracker] wrote the feature"
assert_eq "true" "$(yq -r '.features["issue-tracker"].enabled' "$cfg/.wip.yaml")" "[tracker] enabled:true"
assert_eq "linear" "$(yq -r '.features["issue-tracker"].backend' "$cfg/.wip.yaml")" "[tracker] backend:linear"
# Switch backend → idempotent update to github.
WIP_ROOT="$cfg" bin/wip-plumbing setup issue-tracker github >/dev/null 2>&1
assert_eq "github" "$(yq -r '.features["issue-tracker"].backend' "$cfg/.wip.yaml")" "[tracker] backend switches to github"

# detect + doctor stay drift-free with all three declared (config-echo, no sentinel).
assert_eq "true" "$(WIP_ROOT="$cfg" bin/wip-plumbing detect 2>/dev/null | jq -r '[.features[] | select(.name=="solo" or .name=="forge" or .name=="issue-tracker") | .active] | all')" \
  "[cfg] all three features active in detect"
out="$(WIP_ROOT="$cfg" bin/wip-plumbing doctor 2>/dev/null)"
assert_eq "0" "$(jq -r '.drift_count' <<<"$out")" "[cfg] doctor drift_count 0"

# --- 26. Config-echo verb error + dry-run guards (ADR-0021) -----------------
# issue-tracker requires a backend; unknown backend rejected.
set +e
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup issue-tracker 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "[tracker] missing backend exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "[tracker] missing backend usage"
set +e
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup issue-tracker gitlab 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "[tracker] unknown backend exit 2"

# Tier flags are solo-only; a stray positional is rejected.
set +e
WIP_ROOT="$cfg" bin/wip-plumbing setup forge --force-tier large >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "[forge] --force-tier rejected (solo-only)"
# forge backend is optional, but an unknown positional is rejected (D4); writes nothing.
set +e
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup forge bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "[forge] unknown backend exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "[forge] unknown backend usage"
assert_eq "glab" "$(yq -r '.features.forge.backend' "$cfg/.wip.yaml")" "[forge] bogus backend wrote nothing (pin intact)"
set +e
out="$(WIP_ROOT="$cfg" bin/wip-plumbing setup solo --force-tier banana 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "[solo] invalid --force-tier rejected"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "[solo] invalid --force-tier usage"
set +e
WIP_ROOT="$cfg" bin/wip-plumbing setup solo extra >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "[solo] stray positional rejected"

# --dry-run reports the plan but writes nothing.
dry="$tmp/cfg-dry"
mkdir -p "$dry"
WIP_ROOT="$dry" bin/wip-plumbing init >/dev/null
out="$(WIP_ROOT="$dry" bin/wip-plumbing --dry-run setup forge glab 2>/dev/null)"
assert_eq ".wip.yaml" "$(jq -r '.manifest_updated' <<<"$out")" "[forge] dry-run reports the plan"
assert_eq "null" "$(yq -r '.features.forge' "$dry/.wip.yaml")" "[forge] dry-run wrote nothing"

test_summary
