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

wip_plumbing_cmd_setup() {
  local sub=""
  if [[ $# -gt 0 ]]; then
    sub="$1"
    shift
  fi
  case "$sub" in
    deps | direnv | hygiene | release | agents) ;;
    "") wip_die 2 usage "setup: missing subcommand (deps|direnv|hygiene|release|agents)" ;;
    *) wip_die 2 usage "setup: unknown subcommand: $sub" ;;
  esac

  local force=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --force)
        force=1
        shift
        ;;
      -*) wip_die 2 usage "setup $sub: unknown flag: $1" ;;
      *) wip_die 2 usage "setup $sub: unexpected arg: $1" ;;
    esac
  done
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
  esac

  local raw rc
  set +e
  raw="$(wip_setup_walk_template_tree "$tmpl_dir" "$root")"
  rc=$?
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
    *) printf '' ;;
  esac
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
  esac
}
