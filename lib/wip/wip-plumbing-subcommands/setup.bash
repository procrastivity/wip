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

  local raw rc
  set +e
  if [[ "$sub" == "lds" && "$sentinel_only" == "1" ]]; then
    raw="$(_wip_setup_lds_sentinel_only "$tmpl_dir" "$root")"
    rc=$?
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
      manifest_status="$(wip_setup_set_feature_flag "$manifest" "orchestration" \
        "enabled=true" "backend=solo" "source=plugin")" ||
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

  _wip_setup_hint "$sub"
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
      printf 'wip-plumbing: setup agents: hint: restart Claude Code to load the wip plugin\n' >&2
      printf 'wip-plumbing: setup agents: hint: configure features.solo.agent_tier_policy in .wip.yaml if Solo is your backend\n' >&2
      ;;
    lds)
      printf 'wip-plumbing: setup lds: hint: run `wip-plumbing doctor` to verify the LDS sentinel\n' >&2
      printf 'wip-plumbing: setup lds: hint: `wip-plumbing graduate <artifact>` now works against this repo\n' >&2
      ;;
  esac
}
