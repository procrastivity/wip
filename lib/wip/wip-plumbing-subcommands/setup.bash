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

wip_plumbing_cmd_setup() {
  local sub=""
  if [[ $# -gt 0 ]]; then
    sub="$1"
    shift
  fi
  case "$sub" in
    deps | direnv | hygiene | release | agents | lds) ;;
    "") wip_die 2 usage "setup: missing subcommand (deps|direnv|hygiene|release|agents|lds)" ;;
    *) wip_die 2 usage "setup: unknown subcommand: $sub" ;;
  esac

  local force=0 sentinel_only=0 source_mode="vendored"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --force)
        force=1
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
        shift 2
        ;;
      --source=*)
        [[ "$sub" == "agents" ]] ||
          wip_die 2 usage "setup $sub: --source is only valid for \`setup agents\`"
        source_mode="${1#--source=}"
        shift
        ;;
      -*) wip_die 2 usage "setup $sub: unknown flag: $1" ;;
      *) wip_die 2 usage "setup $sub: unexpected arg: $1" ;;
    esac
  done
  case "$source_mode" in
    plugin | vendored) ;;
    *) wip_die 2 usage "setup $sub: --source must be \`plugin\` or \`vendored\` (got: $source_mode)" ;;
  esac
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
    # plugin-tree walk (wip_setup_walk_template_tree). Returns 5 on an internal
    # render/write failure (distinct from a content-drift refusal at rc=4).
    raw="$(_wip_setup_agents_vendored "$root")"
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

# _wip_setup_agents_vendored <root> — the vendored `setup agents` write path
# (ADR-0020 D1 / D-03.2). Bypasses wip_setup_walk_template_tree (the old
# plugin-tree walk): for each role in `orchestrator coordinator researcher
# builder` (fixed order) it renders the flattened, self-contained agent file via
# `wip_flatten_render <role> <backend>` to a tmpfile, then lands it with
# `wip_setup_write_idempotent` at `$root/.claude/agents/wip/<role>.md` (the
# writer mkdir's the parent and honors WIP_DRY_RUN + WIP_SETUP_FORCE). The
# backend is read from the manifest as `.features.orchestration.backend //
# "solo"` (D-03.5; `solo` on a fresh install). Emits one
# `<status><TAB><relpath>` line per role — relpath is repo-relative
# (`.claude/agents/wip/<role>.md`), matching the generic path's ledger
# convention — so the caller folds the statuses into the same
# wrote/skipped/refused/wrote_forced arrays.
#
# Return contract: 0 normally; 4 if any role refused (content drift, recoverable
# with --force — mirrors the walk's `saw_refused` aggregation); 5 on an internal
# render/write failure (a render non-zero, an mktemp failure, or a writer I/O
# error). The caller maps rc=5 to `wip_die 1 internal`, keeping an internal
# error DISTINCT from a content-drift refusal (rc=4).
_wip_setup_agents_vendored() {
  local root="$1"
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
  [[ "$saw_refused" -eq 0 ]] || return 4
  return 0
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
        printf 'wip-plumbing: setup agents: hint: --source plugin vendored no files; agents resolve by `wip-<role>` from the globally-enabled wip plugin — restart Claude Code to load them\n' >&2
      else
        printf 'wip-plumbing: setup agents: hint: vendored the flattened wip agents to `.claude/agents/wip/`; restart Claude Code to load them\n' >&2
        printf 'wip-plumbing: setup agents: hint: in a repo that is itself a plugin, re-run with `--source plugin` to skip vendoring and use the globally-enabled wip plugin\n' >&2
      fi
      printf 'wip-plumbing: setup agents: hint: configure features.solo.agent_tier_policy in .wip.yaml if Solo is your backend\n' >&2
      ;;
    lds)
      printf 'wip-plumbing: setup lds: hint: run `wip-plumbing doctor` to verify the LDS sentinel\n' >&2
      printf 'wip-plumbing: setup lds: hint: `wip-plumbing graduate <artifact>` now works against this repo\n' >&2
      ;;
  esac
}
