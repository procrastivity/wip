# setup — install-time deterministic scaffold verbs (step-14).
#
# Five subcommands: deps / direnv / hygiene / release / agents. Each verb
# writes verbatim files from templates/setup/<verb>/ into the consumer
# repo, flips its feature flag in .wip.yaml (where applicable), and
# verifies the sentinel post-write.
#
# Per-file contract is the three-way write-or-skip-or-refuse from
# wip-plumbing-setup-lib.bash. Stdout is a JSON write ledger.
# shellcheck shell=bash

# shellcheck source=lib/wip/wip-plumbing-setup-lib.bash
source "$WIP_LIB/wip-plumbing-setup-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-flatten-lib.bash
source "$WIP_LIB/wip-plumbing-flatten-lib.bash"

# _wip_sha256 <file> — echo the lowercase hex sha256 of <file> (no trailing
# newline, no filename). Prefers `sha256sum` (Linux/coreutils), falls back to
# `shasum -a 256` (macOS) — the same cross-platform parity the rest of the repo
# assumes. The provenance sidecar's `baseline_hash` (ADR-0023 D1) is this over
# the bytes written at vendor time. Returns non-zero + a stderr diagnostic if no
# sha256 tool is available or the hash fails.
_wip_sha256() {
  local f="$1" out
  if command -v sha256sum >/dev/null 2>&1; then
    out="$(sha256sum -- "$f")" || return 1
  elif command -v shasum >/dev/null 2>&1; then
    out="$(shasum -a 256 -- "$f")" || return 1
  else
    printf 'wip-plumbing: setup agents: no sha256 tool found (need sha256sum or shasum)\n' >&2
    return 1
  fi
  printf '%s' "${out%% *}"
}

# _wip_plugin_version — echo the installed wip plugin `.version` (ADR-0023 D1),
# resolved through a fallback ladder so the stamp records a real version wherever
# the vendor path runs, and `"unknown"` rather than failing the vendor if none
# resolves:
#   1. $CLAUDE_PLUGIN_ROOT/.claude-plugin/plugin.json   [plugin runtime sets this]
#   2. <resolved roles dir>/../.claude-plugin/plugin.json
#      (the roles/ the renderer actually reads — its parent is the plugin/install
#      root; also the test seam, where WIP_ROLES_DIR points at the repo's roles/)
#   3. $WIP_LIB/../../.claude-plugin/plugin.json         [self-locating install]
# The stamped version is later compared (numeric-component, `_wip_setup_version_lt`)
# against a fresh read of this same oracle to name the divergence DIRECTION (D2a:
# upstream-behind never auto-syncs).
_wip_plugin_version() {
  local v manifest roles_dir
  if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" ]]; then
    manifest="$CLAUDE_PLUGIN_ROOT/.claude-plugin/plugin.json"
    if [[ -f "$manifest" ]]; then
      v="$(jq -r '.version // ""' "$manifest" 2>/dev/null || printf '')"
      [[ -n "$v" ]] && {
        printf '%s' "$v"
        return 0
      }
    fi
  fi
  if roles_dir="$(_wip_flatten_roles_dir 2>/dev/null)" && [[ -n "$roles_dir" ]]; then
    manifest="$roles_dir/../.claude-plugin/plugin.json"
    if [[ -f "$manifest" ]]; then
      v="$(jq -r '.version // ""' "$manifest" 2>/dev/null || printf '')"
      [[ -n "$v" ]] && {
        printf '%s' "$v"
        return 0
      }
    fi
  fi
  if [[ -n "${WIP_LIB:-}" ]]; then
    manifest="$WIP_LIB/../../.claude-plugin/plugin.json"
    if [[ -f "$manifest" ]]; then
      v="$(jq -r '.version // ""' "$manifest" 2>/dev/null || printf '')"
      [[ -n "$v" ]] && {
        printf '%s' "$v"
        return 0
      }
    fi
  fi
  printf 'unknown'
}

wip_plumbing_cmd_setup() {
  local sub=""
  if [[ $# -gt 0 ]]; then
    sub="$1"
    shift
  fi
  case "$sub" in
    deps | direnv | hygiene | release | agents | lds | solo | forge | issue-tracker) ;;
    "") wip_die 2 usage "setup: missing subcommand (deps|direnv|hygiene|release|agents|lds|solo|forge|issue-tracker)" ;;
    *) wip_die 2 usage "setup: unknown subcommand: $sub" ;;
  esac

  local force=0 sentinel_only=0 source_mode="vendored" check=0 migrate=0 source_set=0 status=0 sync=0
  # Config-echo verbs (ADR-0021): solo/forge/issue-tracker write only a .wip.yaml
  # feature stanza — no template files, no sentinel. Their verb-specific args.
  local force_tier="" fallback_tool="" tier_set=0 tracker_backend="" tracker_backend_set=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --force)
        force=1
        shift
        ;;
      --check)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --check is only valid for \`setup agents\`"
        check=1
        shift
        ;;
      --migrate)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --migrate is only valid for \`setup agents\`"
        migrate=1
        shift
        ;;
      --status)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --status is only valid for \`setup agents\`"
        status=1
        shift
        ;;
      --sync)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --sync is only valid for \`setup agents\`"
        sync=1
        shift
        ;;
      --dry-run)
        # Subcommand-level dry-run for `setup agents` (chiefly `--migrate
        # --dry-run`, D6): wire straight to the existing WIP_DRY_RUN seam the
        # writer/manifest-flip helpers already honor. The global `--dry-run`
        # (before the subcommand) still works for every verb; this scopes the
        # seam onto the agents verb so `--migrate --dry-run` plans without
        # touching disk.
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --dry-run is only valid for \`setup agents\` (use the global \`--dry-run\` before the subcommand otherwise)"
        WIP_DRY_RUN=1
        export WIP_DRY_RUN
        shift
        ;;
      --sentinel-only)
        [[ "$sub" == "lds" ]] ||
          wip_die 2 usage "setup $sub: --sentinel-only is only valid for \`setup lds\`"
        sentinel_only=1
        shift
        ;;
      --source)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --source is only valid for \`setup agents\`"
        [[ $# -ge 2 ]] || wip_die 2 usage "setup $sub: --source requires an argument"
        source_mode="$2"
        source_set=1
        shift 2
        ;;
      --source=*)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --source is only valid for \`setup agents\`"
        source_mode="${1#--source=}"
        source_set=1
        shift
        ;;
      --force-tier)
        [[ "$sub" == "solo" ]] ||
          wip_die 2 usage "setup $sub: --force-tier is only valid for \`setup solo\`"
        [[ $# -ge 2 ]] || wip_die 2 usage "setup solo: --force-tier requires an argument"
        force_tier="$2"
        case "$force_tier" in
          small | medium | large) ;;
          *) wip_die 2 usage "setup solo: --force-tier must be small, medium, or large (got: $force_tier)" ;;
        esac
        tier_set=1
        shift 2
        ;;
      --force-tier=*)
        [[ "$sub" == "solo" ]] ||
          wip_die 2 usage "setup $sub: --force-tier is only valid for \`setup solo\`"
        force_tier="${1#--force-tier=}"
        case "$force_tier" in
          small | medium | large) ;;
          *) wip_die 2 usage "setup solo: --force-tier must be small, medium, or large (got: $force_tier)" ;;
        esac
        tier_set=1
        shift
        ;;
      --fallback-tool)
        [[ "$sub" == "solo" ]] ||
          wip_die 2 usage "setup $sub: --fallback-tool is only valid for \`setup solo\`"
        [[ $# -ge 2 ]] || wip_die 2 usage "setup solo: --fallback-tool requires an argument"
        fallback_tool="$2"
        tier_set=1
        shift 2
        ;;
      --fallback-tool=*)
        [[ "$sub" == "solo" ]] ||
          wip_die 2 usage "setup $sub: --fallback-tool is only valid for \`setup solo\`"
        fallback_tool="${1#--fallback-tool=}"
        tier_set=1
        shift
        ;;
      -*) wip_die 2 usage "setup $sub: unknown flag: $1" ;;
      *)
        # `setup issue-tracker <backend>` (linear|github) and `setup forge
        # [gh|glab]` each take one positional backend; every other verb rejects
        # positionals.
        if [[ ("$sub" == "issue-tracker" || "$sub" == "forge") && "$tracker_backend_set" == "0" ]]; then
          tracker_backend="$1"
          tracker_backend_set=1
          shift
        else
          wip_die 2 usage "setup $sub: unexpected arg: $1"
        fi
        ;;
    esac
  done
  case "$source_mode" in
    plugin | vendored) ;;
    *) wip_die 2 usage "setup $sub: --source must be \`plugin\` or \`vendored\` (got: $source_mode)" ;;
  esac
  # --migrate combo rejection (Chunk 1). `--check` is the read-only NEW-layout
  # drift gate; `--migrate` is the cleanup actor — mutually exclusive. And the
  # migrate end-state is derived from the ON-DISK footprint (D2/D4), never the
  # `--source` flag (OQ-07.5 lean: reject), so an explicit `--source` alongside
  # `--migrate` is a contradiction rather than a hint.
  if [[ "$migrate" == "1" && "$check" == "1" ]]; then
    wip_die 2 usage "setup $sub: --migrate and --check are mutually exclusive"
  fi
  # --status is a read-only report; it must not be combined with the other
  # agents actors/gates (--check, --migrate). --source/--force are inert under it.
  if [[ "$status" == "1" && ("$check" == "1" || "$migrate" == "1") ]]; then
    wip_die 2 usage "setup $sub: --status is read-only; do not combine it with --check or --migrate"
  fi
  # --sync is the provenance actor; it stands alone (not with --check/--migrate/
  # --status). --force / --dry-run are its own modifiers (parsed above), not a
  # conflict.
  if [[ "$sync" == "1" && ("$check" == "1" || "$migrate" == "1" || "$status" == "1") ]]; then
    wip_die 2 usage "setup $sub: --sync must not be combined with --check, --migrate, or --status"
  fi
  if [[ "$sync" == "1" && "$source_set" == "1" ]]; then
    wip_die 2 usage "setup $sub: --sync acts on the recorded vendored install, not --source; drop --source"
  fi
  if [[ "$migrate" == "1" && "$source_set" == "1" ]]; then
    wip_die 2 usage "setup $sub: --migrate derives the end-state from the on-disk footprint, not --source; drop --source"
  fi
  if [[ "$force" == "1" ]]; then
    WIP_SETUP_FORCE=1
    export WIP_SETUP_FORCE
  fi

  local root
  root="${WIP_ROOT:-}"
  if [[ -z "$root" ]]; then
    set +e
    root="$(wip_find_root)"
    set -e
  fi
  [[ -n "$root" && -f "$root/.wip.yaml" ]] ||
    wip_die 3 missing-manifest "setup $sub: no .wip.yaml found; run \`init\` first"

  # Config-echo verbs (ADR-0021): solo/forge/issue-tracker write only a
  # .wip.yaml feature stanza — no template files, no sentinel — so they skip the
  # template-dir resolution + walk below entirely.
  case "$sub" in
    solo | forge | issue-tracker)
      _wip_setup_config_verb "$root" "$sub" \
        "$tracker_backend" "$tracker_backend_set" \
        "$tier_set" "$force_tier" "$fallback_tool"
      return 0
      ;;
  esac

  local td tmpl_dir
  td="$(wip_templates_dir)"
  [[ -n "$td" && -d "$td" ]] ||
    wip_die 4 no-templates "setup $sub: templates dir not found"
  tmpl_dir="$td/setup/$sub"
  [[ -d "$tmpl_dir" ]] ||
    wip_die 1 internal "setup $sub: template subdir missing: $tmpl_dir"

  case "$sub" in
    direnv)
      [[ -f "$root/flake.nix" ]] ||
        wip_die 3 missing-prereq \
          "setup direnv: flake.nix not found; hint: run \`wip-plumbing setup deps\` first" \
          "flake.nix"
      ;;
    lds)
      local existing_lds_root
      existing_lds_root="$(yq -r '.features.lds.root // ""' "$root/.wip.yaml" 2>/dev/null || printf '')"
      if [[ -n "$existing_lds_root" && "$existing_lds_root" != "engineering" ]]; then
        wip_die 3 lds-already-installed-elsewhere \
          "setup lds: features.lds.root is already set to \"$existing_lds_root\"; v1 hardcodes \`engineering/\` (backlog: configurable --root)" \
          "$existing_lds_root"
      fi
      ;;
  esac

  # Read-only drift gate (D-05.1/2/3): `setup agents --check` re-renders the
  # vendored agents and diffs them against the installed files, writing NOTHING
  # and never flipping the manifest. Dispatched BEFORE the write path AND before
  # the foreign-plugin guard below — a `--check` writes nothing, so the guard
  # (which refuses a vendored WRITE over a foreign root manifest) must not fire.
  # It branches on the manifest's recorded `.features.orchestration.source`, not
  # the `--source` flag (D-05.2); `--source`/`--force`/`--dry-run` are inert here
  # (Q-05.1 lean: write-flags are no-ops under the read-only check). A render
  # failure surfaces as rc 5 → `wip_die 1 internal`, kept DISTINCT from a drift
  # exit (rc 4, kind `agents-drift`).
  if [[ "$sub" == "agents" && "$check" == "1" ]]; then
    local check_rc
    set +e
    _wip_setup_agents_check "$root" "$td"
    check_rc=$?
    set -e
    if [[ "$check_rc" == "5" ]]; then
      wip_die 1 internal "setup agents: --check render failed (see stderr for the offending role)"
    fi
    exit "$check_rc"
  fi

  # Read-only two-axis status report (ADR-0023 D3). Like --check it writes NOTHING
  # and never flips the manifest; unlike --check it always exits 0 (reporting, not
  # gating). Dispatched here before the write path AND the foreign-plugin guard for
  # the same reason --check is. A classifier failure surfaces as rc 5 → internal.
  if [[ "$sub" == "agents" && "$status" == "1" ]]; then
    local status_rc
    set +e
    _wip_setup_agents_status "$root" "$td"
    status_rc=$?
    set -e
    if [[ "$status_rc" == "5" ]]; then
      wip_die 1 internal "setup agents: --status render failed (see stderr for the offending role)"
    fi
    exit 0
  fi

  # Provenance actor (ADR-0023 D3): `setup agents --sync [--force] [--dry-run]`
  # refreshes drifted vendored files per their two-axis state. Dispatched here
  # (before the foreign-plugin guard) like --check/--status; it is source-gated on
  # `vendored` internally and a genuinely-vendored repo carries no foreign root
  # manifest, so the guard is moot. Returns 4 when it refused locally-modified
  # files without --force; 5 (render/classifier failure) → internal.
  if [[ "$sub" == "agents" && "$sync" == "1" ]]; then
    local sync_rc
    set +e
    _wip_setup_agents_sync "$root" "$td"
    sync_rc=$?
    set -e
    if [[ "$sync_rc" == "5" ]]; then
      wip_die 1 internal "setup agents: --sync render failed (see stderr for the offending role)"
    fi
    exit "$sync_rc"
  fi

  # Migration actor (ADR-0020 migration path / D2–D6): `setup agents --migrate`
  # cleans the leftover OLD plugin-tree footprint a pre-flatten `setup agents`
  # left in a consumer repo, then lands (or, for a host-plugin repo, declares)
  # the flattened end-state. Dispatched BEFORE the foreign-plugin guard below —
  # migration keys on the on-disk footprint (D2) and handles a foreign root
  # manifest itself (D4: leave it, host-plugin end-state), so it must not be
  # refused by the vendored-write guard. The actor honors WIP_DRY_RUN (D6).
  if [[ "$sub" == "agents" && "$migrate" == "1" ]]; then
    local migrate_rc
    set +e
    _wip_setup_agents_migrate "$root" "$td"
    migrate_rc=$?
    set -e
    exit "$migrate_rc"
  fi

  # Conservative-write guard (ADR-0020 / D-03.4): the vendored `setup agents`
  # path must never clobber or be installed alongside a foreign host plugin's
  # root `.claude-plugin/plugin.json` (one wip does not own). On a foreign hit,
  # refuse with a drift-style exit (rc=4, write NOTHING) and steer the operator
  # to `--source plugin` — the path for repos that are themselves plugins, which
  # writes no root manifest and relies on the globally-enabled wip plugin. This
  # is VENDORED-PATH-ONLY: `--source plugin` legitimately owns a foreign root
  # manifest and must never be refused here. Wired at the seam where Chunk 3's
  # vendored render+write loop lands, so the guard fires before any write.
  if [[ "$sub" == "agents" && "$source_mode" == "vendored" ]] &&
    _wip_setup_agents_foreign_plugin "$root"; then
    local guard_path=".claude-plugin/plugin.json"
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "setup $sub" --arg path "$guard_path" '
        {ok:false, verb:$verb,
         error:{code:4, kind:"foreign-plugin-manifest",
                message:"refusing to vendor agents over a foreign root .claude-plugin/plugin.json (wip does not own it); re-run with `--source plugin` to rely on the globally-enabled wip plugin instead",
                paths:[$path]}}'
    fi
    printf 'wip-plumbing: setup %s: foreign root .claude-plugin/plugin.json present; re-run with --source plugin\n' "$sub" >&2
    exit 4
  fi

  local raw rc
  set +e
  if [[ "$sub" == "lds" && "$sentinel_only" == "1" ]]; then
    raw="$(_wip_setup_lds_sentinel_only "$tmpl_dir" "$root")"
    rc=$?
  elif [[ "$sub" == "agents" && "$source_mode" == "vendored" ]]; then
    # Vendored agents (ADR-0020 D1 / D-03.2): render the four flattened agent
    # files and land them under .claude/agents/wip/, BYPASSING the old
    # plugin-tree walk (wip_setup_walk_template_tree). Then (step-06 / ADR-0015
    # amend) relocate the canonical wip slash-commands verbatim into
    # .claude/commands/wip/ — `$td` carries the templates root for that copy.
    # Returns 5 on an internal render/write failure (distinct from a
    # content-drift refusal at rc=4).
    raw="$(_wip_setup_agents_vendored "$root" "$td")"
    rc=$?
  elif [[ "$sub" == "agents" && "$source_mode" == "plugin" ]]; then
    # --source plugin no-vendor path (ADR-0020 D4 / D-03.3): vendor NOTHING.
    # Write zero files (ledger wrote: []); the agents resolve by the same bare
    # `wip-<role>` name from the globally-enabled wip plugin. The manifest flip
    # below records source=plugin. The foreign-plugin guard above is gated on
    # `source_mode == vendored`, so it never fires here (a foreign root manifest
    # is the EXPECTED state for a plugin repo — this is the steering target).
    raw=""
    rc=0
  else
    raw="$(wip_setup_walk_template_tree "$tmpl_dir" "$root")"
    rc=$?
  fi
  set -e

  local wrote=() skipped=() wrote_forced=() refused=()
  local status path
  while IFS=$'\t' read -r status path; do
    [[ -n "$path" ]] || continue
    case "$status" in
      wrote) wrote+=("$path") ;;
      skipped) skipped+=("$path") ;;
      wrote_forced) wrote_forced+=("$path") ;;
      refused) refused+=("$path") ;;
    esac
  done <<<"$raw"

  if [[ "$sub" == "agents" && "$source_mode" == "vendored" && "$rc" == "5" ]]; then
    wip_die 1 internal "setup $sub: agent render/write failed (see stderr for the offending role)"
  fi
  if [[ "$rc" != "0" && "$rc" != "4" ]]; then
    wip_die 1 internal "setup $sub: template walk failed (rc=$rc)"
  fi

  if [[ "$rc" == "4" ]]; then
    local refused_json
    refused_json="$(_wip_setup_arr_json "${refused[@]+"${refused[@]}"}")"
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "setup $sub" --argjson paths "$refused_json" '
        {ok:false, verb:$verb,
         error:{code:4, kind:"content-drift",
                message:"infrastructure files differ from template; re-run with --force to overwrite",
                paths:$paths}}'
    fi
    printf 'wip-plumbing: setup %s: content drift on: %s\n' "$sub" "${refused[*]}" >&2
    exit 4
  fi

  local manifest="$root/.wip.yaml"
  local manifest_status="noop"
  case "$sub" in
    direnv)
      manifest_status="$(wip_setup_set_feature_flag "$manifest" "direnv" "enabled=true")" ||
        wip_die 1 internal "setup $sub: manifest update failed"
      ;;
    release)
      manifest_status="$(wip_setup_set_feature_flag "$manifest" "changelog" "enabled=true")" ||
        wip_die 1 internal "setup $sub: manifest update failed"
      ;;
    agents)
      # Record the selected source mode (D-03.1): default `vendored` (the bare
      # verb writes a vendored flattened install) or `plugin` (--source plugin,
      # no-vendor). enabled/backend stay fixed.
      manifest_status="$(wip_setup_set_feature_flag "$manifest" "orchestration" \
        "enabled=true" "backend=solo" "source=$source_mode")" ||
        wip_die 1 internal "setup $sub: manifest update failed"
      ;;
    lds)
      manifest_status="$(wip_setup_set_feature_flag "$manifest" "lds" \
        "enabled=true" "root=engineering")" ||
        wip_die 1 internal "setup $sub: manifest update failed"
      ;;
  esac

  local sentinel sentinel_present_json="null"
  sentinel="$(wip_setup_sentinel_for_verb "$sub")"
  if [[ -n "$sentinel" && "${WIP_DRY_RUN:-0}" != "1" ]]; then
    if [[ -e "$root/$sentinel" ]]; then
      sentinel_present_json="true"
    else
      wip_die 1 internal "setup $sub: sentinel $sentinel missing post-write"
    fi
  elif [[ -n "$sentinel" ]]; then
    sentinel_present_json="false"
  fi

  local manifest_updated_json
  if [[ "$manifest_status" == "updated" ]]; then
    manifest_updated_json='".wip.yaml"'
  else
    manifest_updated_json="null"
  fi
  local sentinel_json
  if [[ -n "$sentinel" ]]; then
    sentinel_json="$(jq -nc --arg s "$sentinel" '$s')"
  else
    sentinel_json="null"
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc \
      --arg verb "setup $sub" \
      --argjson wrote "$(_wip_setup_arr_json "${wrote[@]+"${wrote[@]}"}")" \
      --argjson skipped "$(_wip_setup_arr_json "${skipped[@]+"${skipped[@]}"}")" \
      --argjson wrote_forced "$(_wip_setup_arr_json "${wrote_forced[@]+"${wrote_forced[@]}"}")" \
      --argjson refused "$(_wip_setup_arr_json "${refused[@]+"${refused[@]}"}")" \
      --argjson manifest_updated "$manifest_updated_json" \
      --argjson sentinel "$sentinel_json" \
      --argjson sentinel_present "$sentinel_present_json" '
      {ok:true, verb:$verb,
       wrote:$wrote, skipped_idempotent:$skipped,
       wrote_forced:$wrote_forced, refused:$refused,
       manifest_updated:$manifest_updated,
       sentinel:$sentinel, sentinel_present:$sentinel_present}'
  fi

  _wip_setup_hint "$sub" "$source_mode"
}

# wip_setup_sentinel_for_verb <verb> — echo the sentinel path the verb's
# feature is mapped to, or empty when the verb has no manifest-tracked
# feature. Distinct from wip_setup_sentinel_for (which takes a feature
# name) — verbs and features don't always share a name (agents →
# orchestration).
wip_setup_sentinel_for_verb() {
  case "$1" in
    direnv) printf '.envrc' ;;
    release) printf 'CHANGELOG.md' ;;
    lds) printf 'engineering/.lds-manifest.yaml' ;;
    *) printf '' ;;
  esac
}

# _wip_setup_agents_foreign_plugin <root> — true (rc 0) iff a root
# `.claude-plugin/plugin.json` exists whose `.name` is NOT `wip` (a host plugin
# wip does not own); false (rc 1) when the manifest is absent or its `.name` is
# `wip`. Drives the vendored `setup agents` conservative-write guard (ADR-0020
# / D-03.4): a foreign root manifest signals the repo is itself a plugin, so the
# vendored path must refuse and steer to `--source plugin` rather than clobber
# the host manifest. Reads JSON via `jq` (the repo idiom — cf. doctor/setup);
# an unparseable or nameless manifest reads as a non-`wip` name and is therefore
# treated as foreign (conservative: refuse rather than risk a clobber).
_wip_setup_agents_foreign_plugin() {
  local root="$1"
  local manifest="$root/.claude-plugin/plugin.json"
  [[ -f "$manifest" ]] || return 1
  local name
  name="$(jq -r '.name // ""' "$manifest" 2>/dev/null || printf '')"
  [[ "$name" != "wip" ]]
}

# _wip_setup_agents_vendored <root> <templates-dir> — the vendored `setup
# agents` write path (ADR-0020 D1 / D-03.2; step-06 command relocation). Bypasses
# wip_setup_walk_template_tree (the old plugin-tree walk).
#
# Two write phases, both folded into one `<status><TAB><relpath>` line stream the
# caller aggregates into the wrote/skipped/refused/wrote_forced arrays:
#
#   1. Agents — for each role in `orchestrator coordinator researcher builder`
#      (fixed order) render the flattened, self-contained agent file via
#      `wip_flatten_render <role> <backend>` to a tmpfile, then land it with
#      `wip_setup_write_idempotent` at `$root/.claude/agents/wip/<role>.md`. The
#      backend is read from the manifest as `.features.orchestration.backend //
#      "solo"` (D-03.5; `solo` on a fresh install).
#   2. Commands (step-06 / ADR-0015 amend) — relocate each canonical wip
#      slash-command VERBATIM (pure resolver-swap, D3 — no flatten / no
#      `@`-include resolution; the resolver transform is already baked into the
#      committed templates) from `<templates-dir>/setup/agents/commands/<name>.md`
#      to `$root/.claude/commands/wip/<name>.md`. The `wip/` subdir is what yields
#      the `/wip:<name>` colon invocation (D1/D2). The set is iterated by GLOB
#      (set-parity, D4) — never a hardcoded count — so a future command addition
#      installs automatically. `$root/.claude/commands/wip/<name>.md` keeps the
#      same filename + bytes as its template; only the destination dir differs.
#
# `wip_setup_write_idempotent` mkdir's each parent and honors WIP_DRY_RUN +
# WIP_SETUP_FORCE. relpaths are repo-relative, matching the generic path's ledger
# convention. The foreign-plugin guard upstream fences BOTH phases (D5) — it runs
# before this function, so `--source plugin` and a foreign root manifest never
# reach here.
#
# Return contract: 0 normally; 4 if any agent OR command refused (content drift,
# recoverable with --force — mirrors the walk's `saw_refused` aggregation); 5 on
# an internal render/write failure (a render non-zero, an mktemp failure, or a
# writer I/O error). The caller maps rc=5 to `wip_die 1 internal`, keeping an
# internal error DISTINCT from a content-drift refusal (rc=4).
_wip_setup_agents_vendored() {
  local root="$1" td="$2"
  local backend
  backend="$(yq -r '.features.orchestration.backend // "solo"' "$root/.wip.yaml" 2>/dev/null || printf 'solo')"
  [[ -n "$backend" && "$backend" != "null" ]] || backend="solo"

  local saw_refused=0
  local role rel dest tmp status rc
  for role in orchestrator coordinator researcher builder; do
    rel=".claude/agents/wip/$role.md"
    dest="$root/$rel"
    tmp="$(mktemp)" || {
      printf 'wip-plumbing: setup agents: mktemp failed\n' >&2
      return 5
    }
    set +e
    wip_flatten_render "$role" "$backend" >"$tmp"
    rc=$?
    set -e
    if [[ "$rc" != "0" ]]; then
      rm -f -- "$tmp"
      printf 'wip-plumbing: setup agents: render failed for role %s (backend %s, rc=%d)\n' "$role" "$backend" "$rc" >&2
      return 5
    fi
    set +e
    status="$(wip_setup_write_idempotent "$tmp" "$dest")"
    rc=$?
    set -e
    rm -f -- "$tmp"
    case "$rc" in
      0) ;;
      4) saw_refused=1 ;;
      *)
        printf 'wip-plumbing: setup agents: write helper failed (%d) for %s\n' "$rc" "$rel" >&2
        return 5
        ;;
    esac
    printf '%s\t%s\n' "$status" "$rel"
  done

  # Phase 2: relocate the canonical wip slash-commands verbatim (set-parity by
  # glob, D4). A direct idempotent copy — no render — because the resolver swap
  # is already baked into the committed templates (D3).
  local cmd_dir="$td/setup/agents/commands"
  local cmd_tmpl name
  for cmd_tmpl in "$cmd_dir"/*.md; do
    [[ -e "$cmd_tmpl" ]] || continue # empty/absent glob → nothing to vendor
    name="$(basename -- "$cmd_tmpl")"
    rel=".claude/commands/wip/$name"
    dest="$root/$rel"
    set +e
    status="$(wip_setup_write_idempotent "$cmd_tmpl" "$dest")"
    rc=$?
    set -e
    case "$rc" in
      0) ;;
      4) saw_refused=1 ;;
      *)
        printf 'wip-plumbing: setup agents: command write helper failed (%d) for %s\n' "$rc" "$rel" >&2
        return 5
        ;;
    esac
    printf '%s\t%s\n' "$status" "$rel"
  done

  [[ "$saw_refused" -eq 0 ]] || return 4

  # Provenance sidecar (ADR-0023 D1). Stamp AFTER the agent/command files land —
  # one entry per file, `baseline_hash = sha256(the bytes just written)`. This is
  # a SIDE artifact: it emits NO ledger line (no `wrote`/`skipped` TSV), so the
  # install counts the caller aggregates stay exactly `4 + N`. Skipped under
  # WIP_DRY_RUN (plan, write nothing) — including the migrate dry-run reuse, which
  # calls this writer with WIP_DRY_RUN=1. A stamp failure maps to rc 5 (internal),
  # mirroring the render/write failure contract above.
  if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
    _wip_setup_agents_stamp_provenance "$root" "$td" "$backend" || {
      printf 'wip-plumbing: setup agents: provenance stamp failed\n' >&2
      return 5
    }
  fi
  return 0
}

# _wip_setup_agents_stamp_provenance <root> <templates-dir> <backend> — write (or
# refresh) the provenance sidecar `.claude/agents/wip/.provenance.json` (ADR-0023
# D1) AFTER a successful vendored write. Re-derives the vendored file set exactly
# as `_wip_setup_agents_vendored` does — the four fixed roles plus the command
# GLOB (set-parity, never a hardcoded count) — and for each ON-DISK file records
# `{path, kind, source_ref, plugin_version, baseline_hash, vendored_at}`:
#   - source_ref: agent → `roles/<role>.md @ backend <b>`; command →
#     `templates/setup/agents/commands/<name>.md` (the vendor input, D1).
#   - baseline_hash: sha256 of the on-disk bytes (== bytes just written, whether
#     the writer reported wrote / wrote_forced / skipped-idempotent).
#   - plugin_version: `_wip_plugin_version` (the pivot for the D2a direction).
#   - vendored_at: today's date, honoring the WIP_NOW test seam.
# Envelope `{schema:1, files:[…]}`. Written atomically via a tmpfile + mv, so a
# crash never leaves a half-written sidecar. The tmpfile is dot-prefixed and not
# `*.md`, so it is never globbed as a subagent and is gitignore-safe like the
# sidecar itself. Returns 0 on success; 5 on any hash/mkdir/mktemp/jq/mv failure.
_wip_setup_agents_stamp_provenance() {
  local root="$1" td="$2" backend="$3"
  local pv when sidecar_dir sidecar
  pv="$(_wip_plugin_version)"
  when="${WIP_NOW:-$(date +%F)}"
  sidecar_dir="$root/.claude/agents/wip"
  sidecar="$sidecar_dir/.provenance.json"
  mkdir -p -- "$sidecar_dir" || return 5

  local files_json="[]"
  local role rel dest h
  for role in orchestrator coordinator researcher builder; do
    rel=".claude/agents/wip/$role.md"
    dest="$root/$rel"
    [[ -f "$dest" ]] || continue
    h="$(_wip_sha256 "$dest")" || return 5
    files_json="$(jq -nc --argjson a "$files_json" \
      --arg path "$rel" --arg kind "agent" \
      --arg ref "roles/$role.md @ backend $backend" \
      --arg pv "$pv" --arg bh "$h" --arg at "$when" \
      '$a + [{path:$path, kind:$kind, source_ref:$ref,
              plugin_version:$pv, baseline_hash:$bh, vendored_at:$at}]')" || return 5
  done

  local cmd_dir="$td/setup/agents/commands" cmd_tmpl name
  for cmd_tmpl in "$cmd_dir"/*.md; do
    [[ -e "$cmd_tmpl" ]] || continue # empty/absent glob → no commands to stamp
    name="$(basename -- "$cmd_tmpl")"
    rel=".claude/commands/wip/$name"
    dest="$root/$rel"
    [[ -f "$dest" ]] || continue
    h="$(_wip_sha256 "$dest")" || return 5
    files_json="$(jq -nc --argjson a "$files_json" \
      --arg path "$rel" --arg kind "command" \
      --arg ref "templates/setup/agents/commands/$name" \
      --arg pv "$pv" --arg bh "$h" --arg at "$when" \
      '$a + [{path:$path, kind:$kind, source_ref:$ref,
              plugin_version:$pv, baseline_hash:$bh, vendored_at:$at}]')" || return 5
  done

  local tmp
  tmp="$(mktemp -- "$sidecar.XXXXXX")" || return 5
  if ! jq -nc --argjson files "$files_json" '{schema:1, files:$files}' >"$tmp"; then
    rm -f -- "$tmp"
    return 5
  fi
  mv -f -- "$tmp" "$sidecar" || {
    rm -f -- "$tmp"
    return 5
  }
  return 0
}

# _wip_setup_version_lt <a> <b> — true (rc 0) iff version <a> is strictly less
# than version <b>, compared component-wise on `.`-split numeric fields (pure
# bash — no `sort -V`, so it is portable to a stock macOS `sort`). A trailing
# non-numeric suffix on a component (e.g. `18-dev`) is truncated to its numeric
# prefix. `"unknown"` on either side, or equality, is NOT-less-than (rc 1) — so a
# missing/unknown version can never be mistaken for "behind" (D2a: only a
# positively-older installed plugin protects a forward-port from regression).
_wip_setup_version_lt() {
  local a="$1" b="$2"
  [[ "$a" == "unknown" || "$b" == "unknown" ]] && return 1
  [[ "$a" == "$b" ]] && return 1
  local IFS=.
  # shellcheck disable=SC2206  # deliberate word-split on the dot IFS
  local -a av=($a) bv=($b)
  local i max="${#av[@]}"
  [[ "${#bv[@]}" -gt "$max" ]] && max="${#bv[@]}"
  local ai bi
  for ((i = 0; i < max; i++)); do
    ai="${av[i]:-0}"
    bi="${bv[i]:-0}"
    ai="${ai%%[!0-9]*}"
    bi="${bi%%[!0-9]*}"
    ai="${ai:-0}"
    bi="${bi:-0}"
    if ((10#$ai < 10#$bi)); then return 0; fi
    if ((10#$ai > 10#$bi)); then return 1; fi
  done
  return 1
}

# _wip_setup_provenance_state <has_entry> <exists> <B> <R_now> <D_now> <installed>
#                             <stamped>
# The pure two-axis + direction state machine (ADR-0023 D2/D2a). Given a file's
# sidecar-entry presence, on-disk presence, stamped baseline `B`, current render
# hash `R_now`, on-disk hash `D_now`, and the installed vs stamped plugin
# versions, echo `<state><TAB><direction>` where state ∈ {clean, upstream-advanced,
# upstream-behind, locally-modified, both-diverged, unstamped, missing} and
# direction ∈ {none, ahead, behind}. Kept side-effect-free so the classifier and
# its tests share one oracle.
#
#   upstream_advanced := R_now != B   (the plugin render moved off the baseline)
#   locally_modified  := D_now != B   (the on-disk file moved off the baseline)
#
# Degenerate first: no sidecar entry → unstamped (file present) / missing (gone);
# entry but file gone → missing. Then the 4 quadrants; for the upstream-only
# quadrant the version compare splits ahead (auto-syncable) from behind
# (`upstream-behind`, never auto-sync — protects a forward-ported copy).
_wip_setup_provenance_state() {
  local has_entry="$1" exists="$2" B="$3" R_now="$4" D_now="$5" installed="$6" stamped="$7"
  if [[ "$has_entry" != "1" ]]; then
    if [[ "$exists" == "1" ]]; then printf 'unstamped\tnone'; else printf 'missing\tnone'; fi
    return 0
  fi
  if [[ "$exists" != "1" ]]; then
    printf 'missing\tnone'
    return 0
  fi
  local ua=0 lm=0
  [[ "$R_now" != "$B" ]] && ua=1
  [[ "$D_now" != "$B" ]] && lm=1
  if [[ "$ua" == "0" && "$lm" == "0" ]]; then
    printf 'clean\tnone'
    return 0
  fi
  if [[ "$ua" == "1" && "$lm" == "0" ]]; then
    if _wip_setup_version_lt "$installed" "$stamped"; then
      printf 'upstream-behind\tbehind'
    else
      printf 'upstream-advanced\tahead'
    fi
    return 0
  fi
  if [[ "$ua" == "0" && "$lm" == "1" ]]; then
    printf 'locally-modified\tnone'
    return 0
  fi
  # both-diverged — direction still recorded (a behind + hand-edited file is a
  # genuine conflict; --sync --force takes upstream but the direction is surfaced).
  local dir="ahead"
  _wip_setup_version_lt "$installed" "$stamped" && dir="behind"
  printf 'both-diverged\t%s' "$dir"
}

# _wip_setup_agents_provenance_classify <root> <templates-dir> — the SHARED
# two-axis vendored-drift classifier (ADR-0023 D3 — the single oracle reused by
# `--status`, `--sync`, and the `doctor` fan-in). Re-derives the vendored file
# set exactly as the writer/`--check` do (four fixed roles + the command GLOB),
# and for each emits one `<state><TAB><path><TAB><direction>` line on stdout.
#
# Per file: read its sidecar entry (baseline_hash `B`, stamped plugin_version)
# from `.claude/agents/wip/.provenance.json`; compute `D_now` (sha256 of the
# on-disk file) and `R_now` (agent: sha256 of a fresh `wip_flatten_render`;
# command: sha256 of the current template); read the installed plugin version
# once; then hand all of it to `_wip_setup_provenance_state`. Returns 0 normally,
# 5 on an internal render/hash failure (the caller maps rc 5 → internal error).
_wip_setup_agents_provenance_classify() {
  local root="$1" td="$2"
  local backend installed sidecar sidecar_json
  backend="$(yq -r '.features.orchestration.backend // "solo"' "$root/.wip.yaml" 2>/dev/null || printf 'solo')"
  [[ -n "$backend" && "$backend" != "null" ]] || backend="solo"
  installed="$(_wip_plugin_version)"
  sidecar="$root/.claude/agents/wip/.provenance.json"
  sidecar_json=""
  [[ -f "$sidecar" ]] && sidecar_json="$(cat -- "$sidecar" 2>/dev/null || printf '')"

  local role rel dest entry has_entry B stamped exists D_now R_now tmp sd state direction
  for role in orchestrator coordinator researcher builder; do
    rel=".claude/agents/wip/$role.md"
    dest="$root/$rel"
    has_entry=0
    B=""
    stamped="unknown"
    if [[ -n "$sidecar_json" ]]; then
      entry="$(jq -c --arg p "$rel" '.files[]? | select(.path == $p)' <<<"$sidecar_json" 2>/dev/null || printf '')"
      if [[ -n "$entry" ]]; then
        has_entry=1
        B="$(jq -r '.baseline_hash // ""' <<<"$entry")"
        stamped="$(jq -r '.plugin_version // "unknown"' <<<"$entry")"
      fi
    fi
    exists=0
    D_now=""
    if [[ -f "$dest" ]]; then
      exists=1
      D_now="$(_wip_sha256 "$dest")" || return 5
    fi
    tmp="$(mktemp)" || return 5
    if ! wip_flatten_render "$role" "$backend" >"$tmp" 2>/dev/null; then
      rm -f -- "$tmp"
      return 5
    fi
    R_now="$(_wip_sha256 "$tmp")" || {
      rm -f -- "$tmp"
      return 5
    }
    rm -f -- "$tmp"
    sd="$(_wip_setup_provenance_state "$has_entry" "$exists" "$B" "$R_now" "$D_now" "$installed" "$stamped")"
    state="${sd%%$'\t'*}"
    direction="${sd##*$'\t'}"
    printf '%s\t%s\t%s\n' "$state" "$rel" "$direction"
  done

  local cmd_dir="$td/setup/agents/commands" cmd_tmpl name
  for cmd_tmpl in "$cmd_dir"/*.md; do
    [[ -e "$cmd_tmpl" ]] || continue
    name="$(basename -- "$cmd_tmpl")"
    rel=".claude/commands/wip/$name"
    dest="$root/$rel"
    has_entry=0
    B=""
    stamped="unknown"
    if [[ -n "$sidecar_json" ]]; then
      entry="$(jq -c --arg p "$rel" '.files[]? | select(.path == $p)' <<<"$sidecar_json" 2>/dev/null || printf '')"
      if [[ -n "$entry" ]]; then
        has_entry=1
        B="$(jq -r '.baseline_hash // ""' <<<"$entry")"
        stamped="$(jq -r '.plugin_version // "unknown"' <<<"$entry")"
      fi
    fi
    exists=0
    D_now=""
    if [[ -f "$dest" ]]; then
      exists=1
      D_now="$(_wip_sha256 "$dest")" || return 5
    fi
    R_now="$(_wip_sha256 "$cmd_tmpl")" || return 5
    sd="$(_wip_setup_provenance_state "$has_entry" "$exists" "$B" "$R_now" "$D_now" "$installed" "$stamped")"
    state="${sd%%$'\t'*}"
    direction="${sd##*$'\t'}"
    printf '%s\t%s\t%s\n' "$state" "$rel" "$direction"
  done
  return 0
}

# _wip_setup_agents_action_for <state> — map a classifier state to the `--status`
# ACTION token (a stable machine word; the human table + JSON both use it). The
# `doctor` fan-in (Chunk 5) maps the same states to its `fix` sentence.
_wip_setup_agents_action_for() {
  case "$1" in
    clean) printf 'none' ;;
    upstream-advanced) printf 'sync' ;;
    upstream-behind) printf 'upgrade-plugin' ;;
    locally-modified) printf 'sync-force' ;;
    both-diverged) printf 'sync-force' ;;
    unstamped) printf 'sync' ;;
    missing) printf 'sync' ;;
    *) printf 'unknown' ;;
  esac
}

# _wip_setup_agents_status <root> <templates-dir> — the read-only `setup agents
# --status` two-axis report (ADR-0023 D3). Runs the shared classifier and emits a
# JSON envelope on stdout + a human table on stderr; **exits 0 always** (reporting,
# not gating — the blunt CI gate stays `--check`, D4). Branches on the recorded
# `.features.orchestration.source`: only `vendored` has vendored files; anything
# else reports an empty set. Returns 5 on an internal classifier failure (caller
# maps to `wip_die 1 internal`).
_wip_setup_agents_status() {
  local root="$1" td="$2"
  local verb="setup agents"
  local source_recorded
  source_recorded="$(yq -r '.features.orchestration.source // ""' "$root/.wip.yaml" 2>/dev/null || printf '')"
  if [[ "$source_recorded" != "vendored" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "$verb" '
        {ok:true, verb:$verb, files:[],
         summary:{clean:0, upstream_advanced:0, locally_modified:0,
                  both_diverged:0, unstamped:0, missing:0}}'
    fi
    return 0
  fi

  local installed
  installed="$(_wip_plugin_version)"
  local sidecar="$root/.claude/agents/wip/.provenance.json" sidecar_json=""
  [[ -f "$sidecar" ]] && sidecar_json="$(cat -- "$sidecar" 2>/dev/null || printf '')"

  local raw rc
  set +e
  raw="$(_wip_setup_agents_provenance_classify "$root" "$td")"
  rc=$?
  set -e
  [[ "$rc" == "0" ]] || return 5

  local files_json="[]"
  local c_clean=0 c_ua=0 c_lm=0 c_both=0 c_uns=0 c_mis=0
  local state path direction kind stamped action
  while IFS=$'\t' read -r state path direction; do
    [[ -n "$path" ]] || continue
    case "$path" in
      .claude/agents/wip/*) kind="agent" ;;
      .claude/commands/wip/*) kind="command" ;;
      *) kind="unknown" ;;
    esac
    stamped="unknown"
    if [[ -n "$sidecar_json" ]]; then
      stamped="$(jq -r --arg p "$path" '(.files[]? | select(.path == $p) | .plugin_version) // "unknown"' <<<"$sidecar_json" 2>/dev/null || printf 'unknown')"
      [[ -n "$stamped" ]] || stamped="unknown"
    fi
    action="$(_wip_setup_agents_action_for "$state")"
    case "$state" in
      clean) c_clean=$((c_clean + 1)) ;;
      upstream-advanced | upstream-behind) c_ua=$((c_ua + 1)) ;;
      locally-modified) c_lm=$((c_lm + 1)) ;;
      both-diverged) c_both=$((c_both + 1)) ;;
      unstamped) c_uns=$((c_uns + 1)) ;;
      missing) c_mis=$((c_mis + 1)) ;;
    esac
    files_json="$(jq -nc --argjson a "$files_json" \
      --arg path "$path" --arg kind "$kind" --arg state "$state" \
      --arg dir "$direction" --arg stamped "$stamped" --arg installed "$installed" \
      --arg action "$action" '
      $a + [{path:$path, kind:$kind, state:$state, direction:$dir,
             stamped_version:$stamped, installed_version:$installed, action:$action}]')"
    printf 'wip-plumbing: setup agents --status: %-40s %-8s %-16s stamped=%-8s installed=%-8s → %s\n' \
      "$path" "$kind" "$state" "$stamped" "$installed" "$action" >&2
  done <<<"$raw"

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc --arg verb "$verb" --argjson files "$files_json" \
      --argjson clean "$c_clean" --argjson ua "$c_ua" --argjson lm "$c_lm" \
      --argjson both "$c_both" --argjson uns "$c_uns" --argjson mis "$c_mis" '
      {ok:true, verb:$verb, files:$files,
       summary:{clean:$clean, upstream_advanced:$ua, locally_modified:$lm,
                both_diverged:$both, unstamped:$uns, missing:$mis}}'
  fi
  return 0
}

# _wip_setup_agents_sync_write <root> <templates-dir> <backend> <path> <kind> —
# overwrite one vendored file with its upstream bytes: an agent is re-rendered
# (`wip_flatten_render <role> <backend>`), a command is re-copied verbatim from
# its template. Atomic (tmpfile + mv via `_wip_setup_copy_atomic`). A no-op under
# WIP_DRY_RUN (plan only). Returns 0 on success, 1 on render/copy failure.
_wip_setup_agents_sync_write() {
  local root="$1" td="$2" backend="$3" path="$4" kind="$5"
  local dest="$root/$path"
  [[ "${WIP_DRY_RUN:-0}" == "1" ]] && return 0
  if [[ "$kind" == "agent" ]]; then
    local role="${path##*/}"
    role="${role%.md}"
    local tmp
    tmp="$(mktemp)" || return 1
    if ! wip_flatten_render "$role" "$backend" >"$tmp" 2>/dev/null; then
      rm -f -- "$tmp"
      return 1
    fi
    _wip_setup_copy_atomic "$tmp" "$dest" || {
      rm -f -- "$tmp"
      return 1
    }
    rm -f -- "$tmp"
  else
    local name="${path##*/}"
    local tmpl="$td/setup/agents/commands/$name"
    [[ -f "$tmpl" ]] || return 1
    _wip_setup_copy_atomic "$tmpl" "$dest" || return 1
  fi
  return 0
}

# _wip_setup_agents_restamp_files <root> <templates-dir> <backend> <paths...> —
# UPSERT the sidecar entries for exactly <paths...> (baseline_hash = sha256 of
# their CURRENT on-disk bytes, plugin_version = installed, vendored_at = now),
# PRESERVING every other existing entry verbatim. This targeted merge — never a
# wholesale rewrite — is what keeps a skipped `upstream-behind` file's newer
# stamped version intact across a `--sync` that touches its siblings (D2a). For
# an `unstamped` file it establishes the baseline from the current bytes (no
# overwrite); for a re-rendered file it records the fresh render's hash. Atomic
# (tmpfile + mv). Returns 0 on success, 1 on any hash/jq/mv failure.
_wip_setup_agents_restamp_files() {
  local root="$1" td="$2" backend="$3"
  shift 3
  local sidecar="$root/.claude/agents/wip/.provenance.json"
  local pv when files_json
  pv="$(_wip_plugin_version)"
  when="${WIP_NOW:-$(date +%F)}"
  if [[ -f "$sidecar" ]]; then
    files_json="$(jq -c '.files // []' "$sidecar" 2>/dev/null || printf '[]')"
  else
    files_json="[]"
  fi
  [[ -n "$files_json" ]] || files_json="[]"

  local path dest kind ref role name h
  for path in "$@"; do
    dest="$root/$path"
    [[ -f "$dest" ]] || continue
    h="$(_wip_sha256 "$dest")" || return 1
    case "$path" in
      .claude/agents/wip/*)
        kind="agent"
        role="${path##*/}"
        role="${role%.md}"
        ref="roles/$role.md @ backend $backend"
        ;;
      .claude/commands/wip/*)
        kind="command"
        name="${path##*/}"
        ref="templates/setup/agents/commands/$name"
        ;;
      *)
        kind="unknown"
        ref=""
        ;;
    esac
    files_json="$(jq -c --arg p "$path" 'map(select(.path != $p))' <<<"$files_json")" || return 1
    files_json="$(jq -nc --argjson a "$files_json" \
      --arg path "$path" --arg kind "$kind" --arg ref "$ref" \
      --arg pv "$pv" --arg bh "$h" --arg at "$when" \
      '$a + [{path:$path, kind:$kind, source_ref:$ref,
              plugin_version:$pv, baseline_hash:$bh, vendored_at:$at}]')" || return 1
  done

  local dir="$root/.claude/agents/wip" tmp
  mkdir -p -- "$dir" || return 1
  tmp="$(mktemp -- "$sidecar.XXXXXX")" || return 1
  if ! jq -nc --argjson files "$files_json" '{schema:1, files:$files}' >"$tmp"; then
    rm -f -- "$tmp"
    return 1
  fi
  mv -f -- "$tmp" "$sidecar" || {
    rm -f -- "$tmp"
    return 1
  }
  return 0
}

# _wip_setup_agents_sync <root> <templates-dir> — the `setup agents --sync
# [--force] [--dry-run]` per-state actor (ADR-0023 D3, actions per D2). Runs the
# shared classifier and acts on each vendored file:
#   clean              → skip (skipped_clean)
#   upstream-advanced  → re-render + overwrite + restamp (synced)
#   upstream-behind    → SKIP with a version warning (skipped_regressive); NEVER
#                        auto-sync — protects a forward-ported copy from regressing
#   unstamped          → establish the baseline from current bytes + stamp (synced;
#                        non-destructive — no overwrite)
#   missing            → re-render/re-copy the gone file + stamp (synced)
#   locally-modified / both-diverged →
#       without --force → REFUSE (refused_local; the run exits 4)
#       with --force    → back up `<file>.orig` (no git undo — gitignored), take
#                         upstream, restamp (backed_up + synced)
# Reuses `_wip_setup_agents_provenance_classify` (C3), `_wip_setup_agents_sync_write`,
# and `_wip_setup_agents_restamp_files`. Honors WIP_DRY_RUN (plan; write nothing,
# no restamp) and WIP_SETUP_FORCE (the --force gate). Source-gated on
# `vendored`. Emits the D3 JSON ledger; returns 0, 4 (any refused_local), or 5
# (internal classifier/render failure → caller maps to internal).
_wip_setup_agents_sync() {
  local root="$1" td="$2"
  local verb="setup agents"
  local dry="${WIP_DRY_RUN:-0}" force="${WIP_SETUP_FORCE:-0}"

  local source_recorded
  source_recorded="$(yq -r '.features.orchestration.source // ""' "$root/.wip.yaml" 2>/dev/null || printf '')"
  if [[ "$source_recorded" != "vendored" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "$verb" '
        {ok:true, verb:$verb, synced:[], skipped_clean:[], skipped_regressive:[],
         refused_local:[], backed_up:[], restamped:false}'
    fi
    return 0
  fi

  local backend
  backend="$(yq -r '.features.orchestration.backend // "solo"' "$root/.wip.yaml" 2>/dev/null || printf 'solo')"
  [[ -n "$backend" && "$backend" != "null" ]] || backend="solo"

  local raw rc
  set +e
  raw="$(_wip_setup_agents_provenance_classify "$root" "$td")"
  rc=$?
  set -e
  [[ "$rc" == "0" ]] || return 5

  local synced=() skipped_clean=() skipped_regressive=() refused_local=() backed_up=()
  local state path direction kind
  while IFS=$'\t' read -r state path direction; do
    [[ -n "$path" ]] || continue
    case "$path" in
      .claude/agents/wip/*) kind="agent" ;;
      .claude/commands/wip/*) kind="command" ;;
      *) kind="unknown" ;;
    esac
    case "$state" in
      clean)
        skipped_clean+=("$path")
        ;;
      upstream-behind)
        skipped_regressive+=("$path")
        printf 'wip-plumbing: setup agents --sync: %s: installed plugin is older than the stamp (upstream-behind); skipped — upgrade the plugin first\n' "$path" >&2
        ;;
      upstream-advanced | missing)
        _wip_setup_agents_sync_write "$root" "$td" "$backend" "$path" "$kind" || return 5
        synced+=("$path")
        ;;
      unstamped)
        # Establish the baseline from the CURRENT bytes — non-destructive (no
        # overwrite); the restamp below records the on-disk hash.
        synced+=("$path")
        ;;
      locally-modified | both-diverged)
        if [[ "$force" == "1" ]]; then
          if [[ "$dry" != "1" && -f "$root/$path" ]]; then
            cp -- "$root/$path" "$root/$path.orig" || return 5
          fi
          backed_up+=("$path")
          _wip_setup_agents_sync_write "$root" "$td" "$backend" "$path" "$kind" || return 5
          synced+=("$path")
        else
          refused_local+=("$path")
        fi
        ;;
    esac
  done <<<"$raw"

  local restamped="false"
  if [[ "${#synced[@]}" -gt 0 && "$dry" != "1" ]]; then
    _wip_setup_agents_restamp_files "$root" "$td" "$backend" "${synced[@]}" || return 5
    restamped="true"
  fi

  local ok="true"
  [[ "${#refused_local[@]}" -gt 0 ]] && ok="false"
  if [[ "${#refused_local[@]}" -gt 0 ]]; then
    printf 'wip-plumbing: setup agents --sync: refused %s locally-modified file(s) without --force: %s\n' \
      "${#refused_local[@]}" "${refused_local[*]}" >&2
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    local dry_json="false"
    [[ "$dry" == "1" ]] && dry_json="true"
    jq -nc --arg verb "$verb" --argjson ok "$ok" --argjson dry_run "$dry_json" \
      --argjson synced "$(_wip_setup_arr_json "${synced[@]+"${synced[@]}"}")" \
      --argjson skipped_clean "$(_wip_setup_arr_json "${skipped_clean[@]+"${skipped_clean[@]}"}")" \
      --argjson skipped_regressive "$(_wip_setup_arr_json "${skipped_regressive[@]+"${skipped_regressive[@]}"}")" \
      --argjson refused_local "$(_wip_setup_arr_json "${refused_local[@]+"${refused_local[@]}"}")" \
      --argjson backed_up "$(_wip_setup_arr_json "${backed_up[@]+"${backed_up[@]}"}")" \
      --argjson restamped "$restamped" '
      {ok:$ok, verb:$verb, dry_run:$dry_run,
       synced:$synced, skipped_clean:$skipped_clean,
       skipped_regressive:$skipped_regressive, refused_local:$refused_local,
       backed_up:$backed_up, restamped:$restamped}'
  fi

  [[ "${#refused_local[@]}" -gt 0 ]] && return 4
  return 0
}

# _wip_setup_agents_check <root> <templates-dir> — the read-only `setup agents
# --check` drift gate (D-05.1/2/3), the agent-side analog of ADR-0015's
# `sync-agents-commands --check`. Branches on the manifest's recorded
# `.features.orchestration.source`: only `vendored` has installed files to
# verify; anything else (`plugin`, absent) vendored nothing, so `--check` is a
# clean no-op.
#
# It mirrors `_wip_setup_agents_vendored`'s two write phases as two verify phases
# — a single unified vendored-drift gate (kind `agents-drift`, OQ2 lean):
#
#   1. Agents — for each role in `orchestrator coordinator researcher builder`
#      (the vendored write path's fixed order) re-render the flattened agent via
#      `wip_flatten_render <role> <backend>` — backend read as
#      `.features.orchestration.backend // "solo"`, the SAME read as
#      `_wip_setup_agents_vendored` — to a tmpfile and `cmp -s` it against the
#      installed `$root/.claude/agents/wip/<role>.md`. The clean re-render ===
#      installed comparison IS the ADR-0020 round-trip proof (D6): step-08's D5
#      disclaimer is present on BOTH sides via the SAME renderer — never
#      special-cased.
#   2. Commands (step-06 / D6) — for each `<templates-dir>/setup/agents/commands/
#      <name>.md` (set-parity by GLOB, D4) `cmp -s` it DIRECTLY against the
#      installed `$root/.claude/commands/wip/<name>.md` (no re-render — the
#      resolver swap is already baked into the template, D3). A missing or
#      drifted command is the same drift exit. NB (D1a): this proves the FILE
#      LAYOUT, not the `/wip:<name>` runtime invocation.
#
# This path NEVER writes and NEVER flips the manifest, in every branch.
#
# Emits the D-05.3 JSON ledger on stdout (when WIP_JSON) and returns the exit:
#   - source != vendored → clean no-op: return 0, `{ok, verb, checked:[], drift:[]}`.
#   - source == vendored, all in-sync → return 0, `{…, checked:[paths], drift:[]}`.
#   - any drifted/missing agent OR command → return 4, kind `agents-drift`
#     (distinct from `content-drift` / `foreign-plugin-manifest`), `error.paths` =
#     the offending repo-relative paths.
# Returns 5 on an internal render failure (render non-zero or mktemp) so the
# caller maps it to `wip_die 1 internal`, keeping an internal error DISTINCT from
# the drift exit (rc 4). Mirrors `_wip_setup_agents_vendored`'s rc contract.
_wip_setup_agents_check() {
  local root="$1" td="$2"
  local verb="setup agents"
  local source_recorded
  source_recorded="$(yq -r '.features.orchestration.source // ""' "$root/.wip.yaml" 2>/dev/null || printf '')"
  if [[ "$source_recorded" != "vendored" ]]; then
    # Nothing was vendored (source: plugin, or absent) → clean read-only no-op.
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "$verb" '{ok:true, verb:$verb, checked:[], drift:[]}'
    fi
    return 0
  fi

  local backend
  backend="$(yq -r '.features.orchestration.backend // "solo"' "$root/.wip.yaml" 2>/dev/null || printf 'solo')"
  [[ -n "$backend" && "$backend" != "null" ]] || backend="solo"

  local role rel dest tmp rc
  local checked=() drift=()
  for role in orchestrator coordinator researcher builder; do
    rel=".claude/agents/wip/$role.md"
    dest="$root/$rel"
    checked+=("$rel")
    tmp="$(mktemp)" || {
      printf 'wip-plumbing: setup agents: --check mktemp failed\n' >&2
      return 5
    }
    set +e
    wip_flatten_render "$role" "$backend" >"$tmp"
    rc=$?
    set -e
    if [[ "$rc" != "0" ]]; then
      rm -f -- "$tmp"
      printf 'wip-plumbing: setup agents: --check render failed for role %s (backend %s, rc=%d)\n' "$role" "$backend" "$rc" >&2
      return 5
    fi
    if [[ ! -f "$dest" ]]; then
      drift+=("$rel") # missing
    elif ! cmp -s -- "$tmp" "$dest"; then
      drift+=("$rel") # drifted
    fi
    rm -f -- "$tmp"
  done

  # Phase 2: verify the relocated slash-commands (set-parity by glob, D4) by a
  # direct template `cmp` — no re-render (D3). Missing/drifted → same drift exit.
  local cmd_dir="$td/setup/agents/commands"
  local cmd_tmpl name cmd_rel cmd_dest
  for cmd_tmpl in "$cmd_dir"/*.md; do
    [[ -e "$cmd_tmpl" ]] || continue # empty/absent glob → nothing to verify
    name="$(basename -- "$cmd_tmpl")"
    cmd_rel=".claude/commands/wip/$name"
    cmd_dest="$root/$cmd_rel"
    checked+=("$cmd_rel")
    if [[ ! -f "$cmd_dest" ]]; then
      drift+=("$cmd_rel") # missing
    elif ! cmp -s -- "$cmd_tmpl" "$cmd_dest"; then
      drift+=("$cmd_rel") # drifted
    fi
  done

  if [[ "${#drift[@]}" -gt 0 ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "$verb" \
        --argjson paths "$(_wip_setup_arr_json "${drift[@]}")" '
        {ok:false, verb:$verb,
         error:{code:4, kind:"agents-drift",
                message:"installed agent files differ from a fresh re-render; re-run `setup agents` (or `orchestrate backend <name>`) to re-flatten",
                paths:$paths}}'
    fi
    printf 'wip-plumbing: setup agents: --check drift on: %s\n' "${drift[*]}" >&2
    return 4
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc --arg verb "$verb" \
      --argjson checked "$(_wip_setup_arr_json "${checked[@]}")" '
      {ok:true, verb:$verb, checked:$checked, drift:[]}'
  fi
  return 0
}

# _wip_setup_agents_frontmatter_name <file> — echo the `name:` value from a
# markdown YAML frontmatter block (the leading `---`…`---` fence), or empty when
# the file has no frontmatter or no `name:` key. The legacy-footprint detector
# uses it to recognize a thin-pointer wip agent by its version-robust
# `name: wip-<role>` signature (D3) rather than a fragile byte-match. Only the
# leading fence is scanned, so a stray `name:` in the body never false-matches.
_wip_setup_agents_frontmatter_name() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  awk '
    NR == 1 && $0 !~ /^---[[:space:]]*$/ { exit }
    NR == 1 { infm = 1; next }
    infm && /^---[[:space:]]*$/ { exit }
    infm && /^name:[[:space:]]/ {
      sub(/^name:[[:space:]]*/, "")
      sub(/[[:space:]]+$/, "")
      print
      exit
    }
  ' "$file" 2>/dev/null
}

# _wip_setup_agents_legacy_footprint <root> <templates-dir> — pure-disk scan of a
# consumer repo for the OLD plugin-tree `setup agents` footprint (the 16-file
# write set a pre-flatten install left at $root: `.claude-plugin/{plugin.json,
# README.md}`, `agents/README.md`, `agents/{orchestrator,coordinator,researcher,
# builder}.md`, `commands/<name>.md` ×N). It CLASSIFIES each footprint path it
# finds and emits one `<class><TAB><relpath><TAB><reason>` line per EXISTING path
# on stdout; it never writes, never renders, never deletes. Shared verbatim by the
# `--migrate` actor (Chunk 3) and the doctor legacy-footprint check (Chunk 4).
#
# Classes (D3 conservative-delete signals):
#   owned        — positively identified as a wip-installed file → the actor may
#                  delete it. Signals: plugin.json `.name == "wip"`;
#                  `agents/<role>.md` frontmatter `name: wip-<role>` (version-
#                  robust); README / command byte-equal to its surviving template
#                  (the same oracle `--check` uses, D8/D9).
#   foreign      — a host plugin's own root `.claude-plugin/plugin.json` (`.name`
#                  != wip, the F1 case) → NEVER delete; the actor warns + leaves it
#                  and derives the host-plugin end-state (D4). Reuses
#                  `_wip_setup_agents_foreign_plugin` (inverted: true when NOT
#                  wip-owned, so an unparseable/nameless manifest reads foreign —
#                  conservative: warn rather than risk a clobber).
#   unrecognized — a drifted / consumer-authored / version-skewed file at a
#                  footprint path, plus the warn-only-if-present stray root
#                  `roles/` and `active.md` (never part of the real footprint,
#                  OQ-07.1) → warn, never delete. The operator decides.
#
# Emits nothing when no footprint path exists (a fresh flattened install or a
# clean deliberate `source: plugin` repo → callers treat empty as "clean"). Only
# wip footprint paths are scanned; a consumer's own non-wip file (e.g.
# `commands/mine.md`, `agents/my-agent.md`) is simply never visited — it survives
# untouched and keeps its parent dir non-empty (so the actor's rmdir leaves it).
# Returns 0 always (absence of a footprint is not an error).
_wip_setup_agents_legacy_footprint() {
  local root="$1" td="$2"

  # 1. Root plugin manifest — owned iff wip-owned, else the host's own (foreign).
  local pj_rel=".claude-plugin/plugin.json"
  if [[ -f "$root/$pj_rel" ]]; then
    if _wip_setup_agents_foreign_plugin "$root"; then
      printf 'foreign\t%s\t%s\n' "$pj_rel" "foreign-plugin-json"
    else
      printf 'owned\t%s\t%s\n' "$pj_rel" "plugin-json-wip"
    fi
  fi

  # 2. READMEs — owned iff byte-equal to the surviving template oracle (D9).
  local readme_rel readme_tmpl
  for readme_rel in ".claude-plugin/README.md" "agents/README.md"; do
    [[ -f "$root/$readme_rel" ]] || continue
    readme_tmpl="$td/setup/agents/$readme_rel"
    if [[ -f "$readme_tmpl" ]] && cmp -s -- "$readme_tmpl" "$root/$readme_rel"; then
      printf 'owned\t%s\t%s\n' "$readme_rel" "readme-bytematch"
    else
      printf 'unrecognized\t%s\t%s\n' "$readme_rel" "readme-drift"
    fi
  done

  # 3. Thin-pointer role agents — owned iff frontmatter name: wip-<role> (D3,
  #    version-robust). A consumer's own agents/<role>.md is unrecognized/warn.
  local role role_rel fname
  for role in orchestrator coordinator researcher builder; do
    role_rel="agents/$role.md"
    [[ -f "$root/$role_rel" ]] || continue
    fname="$(_wip_setup_agents_frontmatter_name "$root/$role_rel")"
    if [[ "$fname" == "wip-$role" ]]; then
      printf 'owned\t%s\t%s\n' "$role_rel" "agent-frontmatter"
    else
      printf 'unrecognized\t%s\t%s\n' "$role_rel" "agent-drift"
    fi
  done

  # 4. Old plugin-tree slash-commands — owned iff byte-equal to the command
  #    template (set-parity by GLOB over templates, never a hardcoded list).
  #    Version skew → unrecognized/warn (OQ-07.3: warn > wrongful delete).
  local cmd_tmpl name cmd_rel
  for cmd_tmpl in "$td/setup/agents/commands"/*.md; do
    [[ -e "$cmd_tmpl" ]] || continue # empty/absent glob → no commands to scan
    name="$(basename -- "$cmd_tmpl")"
    cmd_rel="commands/$name"
    [[ -f "$root/$cmd_rel" ]] || continue
    if cmp -s -- "$cmd_tmpl" "$root/$cmd_rel"; then
      printf 'owned\t%s\t%s\n' "$cmd_rel" "command-bytematch"
    else
      printf 'unrecognized\t%s\t%s\n' "$cmd_rel" "command-drift"
    fi
  done

  # 5. Defensive stray paths — never written by the old install (OQ-07.1); if a
  #    consumer / hand-vendored copy is present, warn-only, never auto-delete (D3).
  if [[ -e "$root/roles" ]]; then
    printf 'unrecognized\t%s\t%s\n' "roles" "stray-roles"
  fi
  if [[ -e "$root/active.md" ]]; then
    printf 'unrecognized\t%s\t%s\n' "active.md" "stray-active-md"
  fi

  return 0
}

# _wip_setup_agents_migrate <root> <templates-dir> — the `setup agents --migrate`
# cleanup actor (ADR-0020 migration path, D2–D6). Consumes the Chunk-2 detector
# and moves a repo carrying the OLD plugin-tree footprint to the flattened
# end-state (or, for a host-plugin repo, declares the correct source:plugin
# end-state) — surgically, conservatively, idempotently.
#
# End-state decision (keys on the ON-DISK footprint, never the manifest flag — D2):
#   - foreign root manifest present (host plugin, F1) → HOST-PLUGIN end-state (D4):
#     delete wip's owned old files, LEAVE the foreign manifest (warned), write no
#     `.claude/agents|commands`, set `source: plugin`. Correct for a repo that is
#     itself a plugin (relies on the global wip plugin).
#   - else, owned footprint present → VENDORED end-state (D4): delete owned files,
#     reuse `_wip_setup_agents_vendored` VERBATIM to land the flattened install,
#     set `source: vendored`. End state byte-matches a fresh flattened install.
#   - else (no owned footprint, no foreign manifest): keyed on the recorded source
#     — `plugin` → NO-OP (D5: a deliberate plugin repo is never converted; source
#     untouched, nothing written); otherwise → the vendored write (a fresh/already-
#     migrated repo; idempotent all-skip when already flattened, D6).
#
# Conservative delete (D3): only `owned` files are removed; `foreign`/`unrecognized`
# are collected into `warned` and left in place. Empty parent dirs (.claude-plugin/,
# agents/, commands/) are rmdir'd only when empty — a dir still holding a consumer
# file survives. Honors WIP_DRY_RUN: the dry-run branch plans and touches NOTHING
# (no delete, no write, no manifest flip). `migrated` is false when nothing changed
# (already-clean / no-op).
#
# Emits the migrate JSON ledger on stdout (when WIP_JSON):
#   real:    {ok, verb, migrate:true, deleted:[], wrote:[], skipped_idempotent:[],
#             warned:[{path,reason}], manifest_updated, source, migrated}
#   dry-run: {ok, verb, migrate:true, dry_run:true, would_delete:[], would_write:[],
#             would_warn:[{path,reason}], source}
# Returns 0 normally; maps a vendored render/write failure to `wip_die 1 internal`
# (rc 5) and a content-drift refusal on installed `.claude` files to exit 4
# (kind content-drift), mirroring the normal write path's contract.
_wip_setup_agents_migrate() {
  local root="$1" td="$2"
  local verb="setup agents"

  # Classify the on-disk footprint (pure disk, Chunk 2).
  local owned=() warned_paths=() warned_reasons=() foreign_present=0
  local class rel reason
  while IFS=$'\t' read -r class rel reason; do
    [[ -n "$class" ]] || continue
    case "$class" in
      owned) owned+=("$rel") ;;
      foreign)
        foreign_present=1
        warned_paths+=("$rel")
        warned_reasons+=("$reason")
        ;;
      unrecognized)
        warned_paths+=("$rel")
        warned_reasons+=("$reason")
        ;;
    esac
  done < <(_wip_setup_agents_legacy_footprint "$root" "$td")

  local source_recorded
  source_recorded="$(yq -r '.features.orchestration.source // ""' "$root/.wip.yaml" 2>/dev/null || printf '')"

  # Decide the end-state (see header).
  local end_state
  if [[ "$foreign_present" == "1" ]]; then
    end_state="plugin"
  elif [[ "${#owned[@]}" -gt 0 ]]; then
    end_state="vendored"
  elif [[ "$source_recorded" == "plugin" ]]; then
    end_state="noop-plugin"
  else
    end_state="vendored"
  fi

  # Reported/target source: the host-plugin and deliberate-plugin end-states both
  # land at `plugin`; everything else at `vendored`.
  local target_source="vendored"
  [[ "$end_state" == "vendored" ]] || target_source="plugin"

  # Build the warned:[{path,reason}] JSON once (shared by both branches).
  local warned_json="[]" i
  for ((i = 0; i < ${#warned_paths[@]}; i++)); do
    warned_json="$(jq -nc --argjson a "$warned_json" \
      --arg p "${warned_paths[i]}" --arg r "${warned_reasons[i]}" \
      '$a + [{path:$p, reason:$r}]')"
  done

  # --- Dry-run branch (D6): plan only, touch nothing. ------------------------
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    local would_write=()
    if [[ "$end_state" == "vendored" ]]; then
      # WIP_DRY_RUN is set, so the reused vendored writer plans without writing;
      # collect the paths it WOULD create/overwrite (skipped == already-present).
      local vraw vstatus vpath
      set +e
      vraw="$(_wip_setup_agents_vendored "$root" "$td")"
      set -e
      while IFS=$'\t' read -r vstatus vpath; do
        [[ -n "$vpath" ]] || continue
        case "$vstatus" in
          wrote | wrote_forced) would_write+=("$vpath") ;;
        esac
      done <<<"$vraw"
    fi
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "$verb" \
        --argjson would_delete "$(_wip_setup_arr_json "${owned[@]+"${owned[@]}"}")" \
        --argjson would_write "$(_wip_setup_arr_json "${would_write[@]+"${would_write[@]}"}")" \
        --argjson would_warn "$warned_json" \
        --arg source "$target_source" '
        {ok:true, verb:$verb, migrate:true, dry_run:true,
         would_delete:$would_delete, would_write:$would_write,
         would_warn:$would_warn, source:$source}'
    fi
    return 0
  fi

  # --- Real branch: delete owned, rmdir empty parents, land the end-state. ---
  local deleted=() p
  for p in "${owned[@]+"${owned[@]}"}"; do
    if rm -f -- "$root/$p" 2>/dev/null && [[ ! -e "$root/$p" ]]; then
      deleted+=("$p")
    fi
  done
  # rmdir the old footprint parent dirs iff now empty (a dir still holding a
  # consumer file is non-empty → rmdir fails → left in place, D3).
  local d
  for d in ".claude-plugin" "agents" "commands"; do
    [[ -d "$root/$d" ]] || continue
    rmdir "$root/$d" 2>/dev/null || true
  done

  local wrote=() skipped=() wrote_forced=() refused=()
  if [[ "$end_state" == "vendored" ]]; then
    local vraw rc vstatus vpath
    set +e
    vraw="$(_wip_setup_agents_vendored "$root" "$td")"
    rc=$?
    set -e
    if [[ "$rc" == "5" ]]; then
      wip_die 1 internal "setup agents --migrate: vendored write failed (see stderr for the offending role)"
    fi
    while IFS=$'\t' read -r vstatus vpath; do
      [[ -n "$vpath" ]] || continue
      case "$vstatus" in
        wrote) wrote+=("$vpath") ;;
        skipped) skipped+=("$vpath") ;;
        wrote_forced) wrote_forced+=("$vpath") ;;
        refused) refused+=("$vpath") ;;
      esac
    done <<<"$vraw"
    if [[ "$rc" == "4" ]]; then
      # Content drift on already-installed .claude files (recoverable with
      # --force via a plain `setup agents`) — mirror the normal path's refusal.
      if [[ "${WIP_JSON:-1}" == "1" ]]; then
        jq -nc --arg verb "$verb" \
          --argjson paths "$(_wip_setup_arr_json "${refused[@]+"${refused[@]}"}")" '
          {ok:false, verb:$verb,
           error:{code:4, kind:"content-drift",
                  message:"installed .claude agent/command files differ from template; re-run `setup agents --force` to overwrite",
                  paths:$paths}}'
      fi
      printf 'wip-plumbing: setup agents --migrate: content drift on: %s\n' "${refused[*]}" >&2
      exit 4
    fi
  fi

  # Manifest flip. Skip entirely for the deliberate-plugin no-op (D5: source
  # untouched). The host-plugin and vendored end-states set the correct source
  # (idempotent — a no-op when already correct).
  local manifest="$root/.wip.yaml" manifest_status="noop"
  if [[ "$end_state" != "noop-plugin" ]]; then
    manifest_status="$(wip_setup_set_feature_flag "$manifest" "orchestration" \
      "enabled=true" "backend=solo" "source=$target_source")" ||
      wip_die 1 internal "setup agents --migrate: manifest update failed"
  fi

  # migrated: did anything actually change?
  local migrated="false"
  if [[ "${#deleted[@]}" -gt 0 || "${#wrote[@]}" -gt 0 || "${#wrote_forced[@]}" -gt 0 || "$manifest_status" == "updated" ]]; then
    migrated="true"
  fi
  local manifest_updated_json="null"
  [[ "$manifest_status" == "updated" ]] && manifest_updated_json='".wip.yaml"'

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc --arg verb "$verb" \
      --argjson deleted "$(_wip_setup_arr_json "${deleted[@]+"${deleted[@]}"}")" \
      --argjson wrote "$(_wip_setup_arr_json "${wrote[@]+"${wrote[@]}"}")" \
      --argjson skipped "$(_wip_setup_arr_json "${skipped[@]+"${skipped[@]}"}")" \
      --argjson warned "$warned_json" \
      --argjson manifest_updated "$manifest_updated_json" \
      --arg source "$target_source" \
      --argjson migrated "$migrated" '
      {ok:true, verb:$verb, migrate:true,
       deleted:$deleted, wrote:$wrote, skipped_idempotent:$skipped,
       warned:$warned, manifest_updated:$manifest_updated,
       source:$source, migrated:$migrated}'
  fi

  _wip_setup_migrate_hint "$end_state" "${#deleted[@]}" "${#warned_paths[@]}"
  return 0
}

# _wip_setup_migrate_hint <end-state> <deleted-count> <warned-count> — stderr hint
# on a successful migrate (suppressed by -q). Steers on leftover warned paths and
# names the end-state reached.
# shellcheck disable=SC2016 # backticks in hint strings are literal markdown for users
_wip_setup_migrate_hint() {
  [[ "${WIP_QUIET:-0}" == "1" ]] && return 0
  local end_state="$1" deleted_n="$2" warned_n="$3"
  case "$end_state" in
    vendored)
      printf 'wip-plumbing: setup agents --migrate: cleaned %s legacy file(s); vendored the flattened wip agents/commands to `.claude/` (source: vendored); restart Claude Code to load them\n' "$deleted_n" >&2
      ;;
    plugin)
      printf 'wip-plumbing: setup agents --migrate: cleaned %s legacy file(s); left the host plugin manifest in place and set source: plugin (relies on the globally-enabled wip plugin)\n' "$deleted_n" >&2
      ;;
    noop-plugin)
      printf 'wip-plumbing: setup agents --migrate: no legacy footprint found on a deliberate `source: plugin` repo; nothing to do\n' >&2
      ;;
  esac
  if [[ "$warned_n" -gt 0 ]]; then
    printf 'wip-plumbing: setup agents --migrate: %s path(s) left in place (warned) — foreign/unrecognized/consumer-authored; review and remove manually if intended\n' "$warned_n" >&2
  fi
}

# _wip_setup_lds_sentinel_only <tmpl-root> <dest-root> — write ONLY the LDS
# sentinel (.lds-manifest.yaml) and emit a single `<status><TAB><relpath>`
# line. Skips layer dirs and maintenance/ files. Used by `setup lds
# --sentinel-only` for repos that already have an authored `engineering/`
# tree and just need the manifest binding.
_wip_setup_lds_sentinel_only() {
  local tmpl_root="$1" dest_root="$2"
  local rel="engineering/.lds-manifest.yaml"
  local tmpl="$tmpl_root/$rel" dest="$dest_root/$rel"
  [[ -f "$tmpl" ]] || {
    printf 'wip-plumbing: setup lds: sentinel template missing: %s\n' "$tmpl" >&2
    return 1
  }
  local status rc
  set +e
  status="$(wip_setup_write_idempotent "$tmpl" "$dest")"
  rc=$?
  set -e
  case "$rc" in
    0) ;;
    4) ;;
    *)
      printf 'wip-plumbing: setup lds: sentinel write failed (%d)\n' "$rc" >&2
      return 1
      ;;
  esac
  printf '%s\t%s\n' "$status" "$rel"
  return "$rc"
}

# _wip_setup_arr_json <items...> — emit a JSON array of the args, sorted in
# original order. Empty when no args.
_wip_setup_arr_json() {
  local out="[]" p
  for p in "$@"; do
    out="$(jq -nc --argjson a "$out" --arg p "$p" '$a + [$p]')"
  done
  printf '%s' "$out"
}

# _wip_setup_config_verb <root> <verb> <backend> <backend-set> <tier-set>
#                        <force-tier> <fallback-tool>
#
# The config-echo setup path (ADR-0021): solo / forge / issue-tracker are pure
# .wip.yaml feature writers. Validate the verb's args, flip the feature stanza
# idempotently (wip_setup_set_feature_flag, plus the nested agent_tier_policy
# for `setup solo` when tier flags are given), then emit the standard setup JSON
# envelope + the per-verb hint. No template files, no sentinel. Honors
# WIP_DRY_RUN via the shared setters. The feature key is the ledger unit: an
# actual write reports it under `wrote`, an idempotent re-run under
# `skipped_idempotent`.
_wip_setup_config_verb() {
  local root="$1" verb="$2" backend="$3" backend_set="$4"
  local tier_set="$5" force_tier="$6" fallback_tool="$7"
  local manifest="$root/.wip.yaml"

  local feature status sub_status="noop"
  case "$verb" in
    solo)
      feature="solo"
      status="$(wip_setup_set_feature_flag "$manifest" "solo" "enabled=true")" ||
        wip_die 1 internal "setup solo: manifest update failed"
      # Optional agent_tier_policy (ADR-0021 §3): NEVER defaulted — written only
      # when --force-tier / --fallback-tool are supplied. Merged into any
      # existing policy so a bare re-run preserves it.
      if [[ "$tier_set" == "1" ]]; then
        local -a tier_kv=()
        [[ -n "$force_tier" ]] && tier_kv+=("force_tier=$force_tier")
        [[ -n "$fallback_tool" ]] && tier_kv+=("fallback_tool=$fallback_tool")
        if [[ ${#tier_kv[@]} -gt 0 ]]; then
          sub_status="$(wip_setup_set_feature_subblock "$manifest" "solo" "agent_tier_policy" \
            "${tier_kv[@]}")" ||
            wip_die 1 internal "setup solo: agent_tier_policy update failed"
        fi
      fi
      ;;
    forge)
      feature="forge"
      # Backend is OPTIONAL here (D3), the one intentional divergence from the
      # issue-tracker mirror below. Bare `setup forge` is a pure enable flip:
      # the forge kind (gh/glab) is probe-detected at `status --probe-forge`
      # time (ADR-0018) when unpinned. A `setup forge [gh|glab]` positional
      # validates against the CLI names (D4) and pins features.forge.backend.
      # No "missing backend" hard-fail (unlike issue-tracker) — bare is valid.
      if [[ "$backend_set" == "1" ]]; then
        case "$backend" in
          gh | glab) ;;
          *) wip_die 2 usage "setup forge: unknown backend: $backend (expected gh|glab)" ;;
        esac
        status="$(wip_setup_set_feature_flag "$manifest" "forge" \
          "enabled=true" "backend=$backend")" ||
          wip_die 1 internal "setup forge: manifest update failed"
      else
        status="$(wip_setup_set_feature_flag "$manifest" "forge" "enabled=true")" ||
          wip_die 1 internal "setup forge: manifest update failed"
      fi
      ;;
    issue-tracker)
      feature="issue-tracker"
      [[ "$backend_set" == "1" && -n "$backend" ]] ||
        wip_die 2 usage "setup issue-tracker: missing backend (linear|github)"
      case "$backend" in
        linear | github) ;;
        *) wip_die 2 usage "setup issue-tracker: unknown backend: $backend (expected linear|github)" ;;
      esac
      status="$(wip_setup_set_feature_flag "$manifest" "issue-tracker" \
        "enabled=true" "backend=$backend")" ||
        wip_die 1 internal "setup issue-tracker: manifest update failed"
      ;;
  esac

  local changed="false"
  [[ "$status" == "updated" || "$sub_status" == "updated" ]] && changed="true"
  local wrote_json="[]" skipped_json="[]" manifest_updated_json="null"
  if [[ "$changed" == "true" ]]; then
    wrote_json="$(wip_json_string_array "features.$feature")"
    manifest_updated_json='".wip.yaml"'
  else
    skipped_json="$(wip_json_string_array "features.$feature")"
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc \
      --arg verb "setup $verb" \
      --arg feature "$feature" \
      --argjson wrote "$wrote_json" \
      --argjson skipped "$skipped_json" \
      --argjson manifest_updated "$manifest_updated_json" '
      {ok:true, verb:$verb, feature:$feature,
       wrote:$wrote, skipped_idempotent:$skipped,
       wrote_forced:[], refused:[],
       manifest_updated:$manifest_updated,
       sentinel:null, sentinel_present:null}'
  fi

  _wip_setup_hint "$verb"
}

# _wip_setup_hint <verb> — per-verb stderr hint on success (suppressed by -q).
# shellcheck disable=SC2016 # backticks in hint strings are literal markdown for users
_wip_setup_hint() {
  [[ "${WIP_QUIET:-0}" == "1" ]] && return 0
  local source_mode="${2:-vendored}"
  case "$1" in
    deps)
      printf 'wip-plumbing: setup deps: hint: run `direnv allow` then `make check`\n' >&2
      ;;
    direnv)
      printf 'wip-plumbing: setup direnv: hint: run `direnv allow` to activate the devShell\n' >&2
      ;;
    hygiene)
      printf 'wip-plumbing: setup hygiene: hint: add `make hooks` to install the pre-commit hooks\n' >&2
      ;;
    release)
      printf 'wip-plumbing: setup release: hint: edit `cliff.toml` then `git cliff -o CHANGELOG.md` on tag\n' >&2
      ;;
    agents)
      if [[ "$source_mode" == "plugin" ]]; then
        printf 'wip-plumbing: setup agents: hint: --source plugin vendored no files; the agents (`wip-<role>`) and the `/wip:*` slash-commands both resolve from the globally-enabled wip plugin — restart Claude Code to load them\n' >&2
      else
        printf 'wip-plumbing: setup agents: hint: vendored the flattened wip agents to `.claude/agents/wip/` and the wip slash-commands to `.claude/commands/wip/` (invoked as `/wip:<name>`); restart Claude Code to load them\n' >&2
        printf 'wip-plumbing: setup agents: hint: in a repo that is itself a plugin, re-run with `--source plugin` to skip vendoring and use the globally-enabled wip plugin (its global `/wip:*`)\n' >&2
      fi
      printf 'wip-plumbing: setup agents: hint: on a repo that ran the OLD plugin-tree `setup agents` (leftover root `.claude-plugin/`, `agents/`, `commands/`)? run `setup agents --migrate` to clean the legacy footprint safely (`--dry-run` previews; `wip-plumbing doctor` flags it)\n' >&2
      printf 'wip-plumbing: setup agents: hint: configure features.solo.agent_tier_policy in .wip.yaml if Solo is your backend\n' >&2
      ;;
    lds)
      printf 'wip-plumbing: setup lds: hint: run `wip-plumbing doctor` to verify the LDS sentinel\n' >&2
      printf 'wip-plumbing: setup lds: hint: `wip-plumbing graduate <artifact>` now works against this repo\n' >&2
      ;;
    solo)
      printf 'wip-plumbing: setup solo: hint: verify liveness with `wip-plumbing status --probe-solo`\n' >&2
      printf 'wip-plumbing: setup solo: hint: pin a tier policy with `--force-tier <tier>` / `--fallback-tool <name>` (writes features.solo.agent_tier_policy)\n' >&2
      printf 'wip-plumbing: setup solo: hint: this wires the control plane (features.solo); `setup agents` picks the orchestration backend (features.orchestration.backend)\n' >&2
      ;;
    forge)
      printf 'wip-plumbing: setup forge: hint: verify liveness with `wip-plumbing status --probe-forge` (auto-detects gh/glab)\n' >&2
      printf 'wip-plumbing: setup forge: hint: `setup forge [gh|glab]` pins features.forge.backend (otherwise probe-detected)\n' >&2
      ;;
    issue-tracker)
      printf 'wip-plumbing: setup issue-tracker: hint: `wip-plumbing sync` reconciles the wip lifecycle with the tracker\n' >&2
      ;;
  esac
}
