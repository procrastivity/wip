# graduate — promote a single wip-internal planning artifact to its LDS
# canon slot (step-15). The LDS seam per ADR-0006: this verb invokes the
# deterministic core of LDS's extract workflow for the one-artifact case.
#
# Usage: graduate <artifact-path> [--to <eng-docs-relative-path>] [--force]
#
# The target slot comes from the artifact's `graduate-to:` front-matter
# directive, with `--to` overriding when present. `decisions/auto-<slug>.md`
# resolves to the next 4-digit ADR number in <eng-docs>/decisions/.
# shellcheck shell=bash

# shellcheck source=lib/wip/wip-plumbing-graduate-lib.bash
source "$WIP_LIB/wip-plumbing-graduate-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-setup-lib.bash
source "$WIP_LIB/wip-plumbing-setup-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-intake-lib.bash
source "$WIP_LIB/wip-plumbing-intake-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-extract-lib.bash
source "$WIP_LIB/wip-plumbing-extract-lib.bash"

wip_plumbing_cmd_graduate() {
  local artifact="" to="" force=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --to)
        [[ $# -ge 2 ]] || wip_die 2 usage "graduate: --to requires a value"
        to="$2"
        shift 2
        ;;
      --to=*)
        to="${1#--to=}"
        shift
        ;;
      --force)
        force=1
        shift
        ;;
      -*) wip_die 2 usage "graduate: unknown flag: $1" ;;
      *)
        if [[ -z "$artifact" ]]; then
          artifact="$1"
        else
          wip_die 2 usage "graduate: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done
  [[ -n "$artifact" ]] || wip_die 2 usage "graduate: missing <artifact-path>"
  [[ -f "$artifact" ]] || wip_die 4 bad-artifact "graduate: artifact not found: $artifact" "$artifact"

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
    wip_die 3 missing-manifest "graduate: no .wip.yaml found; run \`init\` first"

  # LDS preconditions: enabled + sentinel exists.
  local mj features lds_enabled lds_sentinel_exists eng
  mj="$(wip_manifest_json "$root")"
  features="$(wip_features_json "$root" "$mj")"
  lds_enabled="$(printf '%s' "$features" | jq -r '.[] | select(.name=="lds") | .enabled // false')"
  lds_sentinel_exists="$(printf '%s' "$features" | jq -r '.[] | select(.name=="lds") | .sentinel_exists // false')"
  if [[ "$lds_enabled" != "true" ]]; then
    wip_die 3 lds-not-enabled \
      "graduate: features.lds.enabled is false; LDS install is not in step-15's scope (backlog: setup-lds-verb)"
  fi
  eng="$(wip_extract_lds_root "$mj")"
  if [[ "$lds_sentinel_exists" != "true" ]]; then
    wip_die 3 lds-sentinel-missing \
      "graduate: $eng/.lds-manifest.yaml missing; run the LDS install workflow (backlog: setup-lds-verb)" \
      "$eng/.lds-manifest.yaml"
  fi

  # Resolve the directive (CLI > front-matter).
  local directive="$to"
  if [[ -z "$directive" ]]; then
    directive="$(wip_graduate_target_from_artifact "$artifact")"
  fi

  local decisions_abs="$root/$eng/decisions"
  local target rc target_msg
  set +e
  target="$(wip_graduate_resolve_target "$eng" "$directive" "$decisions_abs" 2>/tmp/wip-graduate-$$.err)"
  rc=$?
  target_msg="$(cat /tmp/wip-graduate-$$.err 2>/dev/null)"
  rm -f /tmp/wip-graduate-$$.err
  set -e
  if [[ "$rc" != "0" ]]; then
    local kind="no-target"
    case "$target_msg" in
      *"unknown layer"*) kind="unknown-layer" ;;
      *"auto-numbering is decisions-only"* | *"auto- shorthand needs a slug"*) kind="bad-auto-slot" ;;
      *"must be eng-docs-relative"* | *"contains \"..\""* | *"must be <layer>/<file>"*) kind="bad-target" ;;
    esac
    # Re-emit stderr message for the user.
    [[ -n "$target_msg" ]] && printf '%s\n' "$target_msg" >&2
    wip_die 4 "$kind" "graduate: ${target_msg:-target resolution failed}"
  fi

  local dest="$root/$target"

  # Render artifact body to a tmp file, then use the three-way helper.
  local tmp
  tmp="$(mktemp -t wip-graduate.XXXXXX)" ||
    wip_die 1 internal "graduate: mktemp failed"
  # shellcheck disable=SC2064
  trap "rm -f -- '$tmp'" EXIT
  wip_graduate_render_body "$artifact" >"$tmp" ||
    wip_die 1 internal "graduate: render failed"

  local status
  set +e
  status="$(wip_setup_write_idempotent "$tmp" "$dest")"
  rc=$?
  set -e
  if [[ "$rc" != "0" && "$rc" != "4" ]]; then
    wip_die 1 internal "graduate: write helper failed (rc=$rc) for $target"
  fi

  local wrote=() skipped=() wrote_forced=() refused=()
  case "$status" in
    wrote) wrote+=("$target") ;;
    skipped) skipped+=("$target") ;;
    wrote_forced) wrote_forced+=("$target") ;;
    refused) refused+=("$target") ;;
  esac

  if [[ "$rc" == "4" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg verb "graduate" --arg path "$target" '
        {ok:false, verb:$verb,
         error:{code:4, kind:"content-drift",
                message:"target differs from artifact; re-run with --force to overwrite",
                path:$path}}'
    fi
    printf 'wip-plumbing: graduate: content drift on: %s\n' "$target" >&2
    exit 4
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc \
      --arg verb "graduate" \
      --arg artifact "$artifact" \
      --arg target "$target" \
      --argjson wrote "$(wip_json_string_array "${wrote[@]+"${wrote[@]}"}")" \
      --argjson skipped "$(wip_json_string_array "${skipped[@]+"${skipped[@]}"}")" \
      --argjson wrote_forced "$(wip_json_string_array "${wrote_forced[@]+"${wrote_forced[@]}"}")" \
      --argjson refused "$(wip_json_string_array "${refused[@]+"${refused[@]}"}")" '
      {ok:true, verb:$verb, artifact:$artifact, target:$target,
       wrote:$wrote, skipped_idempotent:$skipped,
       wrote_forced:$wrote_forced, refused:$refused}'
  fi

  [[ "${WIP_QUIET:-0}" == "1" ]] ||
    printf 'wip-plumbing: graduate: hint: review %s and commit\n' "$target" >&2
}
