# intake — classify / validate / apply per ADR-0009 + intake-kinds.md.
# Plumbing only: never asks the user, never makes a judgment call.
# shellcheck shell=bash

wip_plumbing_cmd_intake() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    classify) _wip_intake_cmd_classify "$@" ;;
    validate) _wip_intake_cmd_validate "$@" ;;
    apply) _wip_intake_cmd_apply "$@" ;;
    "") wip_die 2 usage "intake: missing subcommand (classify|validate|apply)" ;;
    *) wip_die 2 usage "intake: unknown subcommand: $sub" ;;
  esac
}

_wip_intake_require_file() {
  local file="$1"
  [[ -n "$file" ]] || wip_die 2 usage "intake: missing <file>"
  [[ -f "$file" && -r "$file" ]] || wip_die 2 not-found "intake: file not readable: $file"
}

_wip_intake_cmd_classify() {
  local file=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -*) wip_die 2 usage "intake classify: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_die 2 usage "intake classify: unexpected arg: $1"
        fi
        ;;
    esac
  done
  _wip_intake_require_file "$file"

  local payload
  set +e
  payload="$(wip_intake_classify_payload "$file")"
  local rc=$?
  set -e
  if [[ "$rc" != "0" ]]; then
    wip_die 4 unparseable "intake classify: no H1 title in $file" "$file"
  fi

  jq -nc --arg file "$file" --argjson p "$payload" '
    { ok: true, file: $file, kind: $p.kind, confidence: $p.confidence, signals: $p.signals }'
}

_wip_intake_cmd_validate() {
  local file="" kind=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --kind)
        [[ $# -ge 2 ]] || wip_die 2 usage "intake validate: --kind requires an argument"
        kind="$2"
        shift 2
        ;;
      --kind=*)
        kind="${1#--kind=}"
        shift
        ;;
      -*) wip_die 2 usage "intake validate: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_die 2 usage "intake validate: unexpected arg: $1"
        fi
        ;;
    esac
  done
  _wip_intake_require_file "$file"

  if [[ -z "$kind" ]]; then
    # No --kind: use classify's guess.
    local payload
    set +e
    payload="$(wip_intake_classify_payload "$file")"
    local rc=$?
    set -e
    if [[ "$rc" != "0" ]]; then
      wip_die 4 unparseable "intake validate: no H1 title in $file" "$file"
    fi
    kind="$(jq -r '.kind' <<<"$payload")"
  else
    wip_intake_kind_valid "$kind" ||
      wip_die 2 usage "intake validate: --kind must be one of: $WIP_INTAKE_KINDS"
  fi

  local result
  result="$(wip_intake_validate_kind "$file" "$kind")"
  local valid
  valid="$(jq -r '.valid' <<<"$result")"

  jq -nc \
    --arg file "$file" --arg kind "$kind" \
    --argjson valid "$valid" --argjson r "$result" '
    { ok: $valid, file: $file, kind: $kind, valid: $valid,
      missing: $r.missing, signals: $r.signals }'

  [[ "$valid" == "true" ]] || exit 4
}

_wip_intake_cmd_apply() {
  local file="" kind="" target=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --kind)
        [[ $# -ge 2 ]] || wip_die 2 usage "intake apply: --kind requires an argument"
        kind="$2"
        shift 2
        ;;
      --kind=*)
        kind="${1#--kind=}"
        shift
        ;;
      --target)
        [[ $# -ge 2 ]] || wip_die 2 usage "intake apply: --target requires an argument"
        target="$2"
        shift 2
        ;;
      --target=*)
        target="${1#--target=}"
        shift
        ;;
      -*) wip_die 2 usage "intake apply: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_die 2 usage "intake apply: unexpected arg: $1"
        fi
        ;;
    esac
  done
  _wip_intake_require_file "$file"
  [[ -n "$kind" ]] || wip_die 2 usage "intake apply: --kind is required"
  wip_intake_kind_valid "$kind" ||
    wip_die 2 usage "intake apply: --kind must be one of: $WIP_INTAKE_KINDS"

  local result valid
  result="$(wip_intake_validate_kind "$file" "$kind")"
  valid="$(jq -r '.valid' <<<"$result")"
  if [[ "$valid" != "true" ]]; then
    jq -nc \
      --arg file "$file" --arg kind "$kind" --argjson r "$result" '
      { ok: false, file: $file, kind: $kind, valid: false,
        missing: $r.missing, signals: $r.signals }'
    exit 4
  fi

  case "$kind" in
    brief) _wip_intake_apply_brief "$file" ;;
    amendment) _wip_intake_apply_amendment "$file" "$target" ;;
    workplan-seed) _wip_intake_apply_workplan_seed "$file" "$target" ;;
    spec)
      wip_die 3 not-implemented \
        "intake apply: spec routing requires the LDS seam (ADR-0006); not yet wired"
      ;;
    handoff)
      wip_die 4 not-terminal "intake apply: handoff is not a terminal kind; reshape first"
      ;;
  esac
}

_wip_intake_apply_brief() {
  local file="$1"
  local slug h1
  slug="$(wip_intake_derive_slug "$file")"
  [[ -n "$slug" ]] || wip_die 4 bad-slug "intake apply: could not derive slug from $file" "$file"

  h1="$(wip_intake_read_h1 "$file")"

  # Source init.bash so its function is in scope.
  # shellcheck disable=SC1091
  source "$WIP_LIB/wip-plumbing-subcommands/init.bash"

  local ledger rc
  set +e
  if [[ -n "$h1" ]]; then
    ledger="$(wip_plumbing_cmd_init "$slug" --title "$h1")"
  else
    ledger="$(wip_plumbing_cmd_init "$slug")"
  fi
  rc=$?
  set -e

  if [[ "$rc" != "0" ]]; then
    # init already emitted its error envelope on stdout.
    exit "$rc"
  fi

  jq -nc \
    --arg kind "brief" --arg slug "$slug" --argjson result "$ledger" '
    { ok: true, kind: $kind, dispatched: "init", target: $slug, result: $result }'
}

_wip_intake_apply_amendment() {
  local file="$1" cli_target="$2"
  local fm slug
  fm="$(wip_intake_read_front_matter "$file")"
  if [[ -n "$cli_target" ]]; then
    slug="$cli_target"
  else
    slug="$(_wip_intake_fm_str "$fm" "target")"
  fi
  [[ -n "$slug" ]] ||
    wip_die 4 missing-target "intake apply: amendment lacks target slug" "$file"

  # shellcheck disable=SC1091
  source "$WIP_LIB/wip-plumbing-subcommands/roadmap.bash"

  local ledger rc
  set +e
  ledger="$(wip_plumbing_cmd_roadmap amend "$slug" --from "$file")"
  rc=$?
  set -e
  [[ "$rc" == "0" ]] || exit "$rc"

  jq -nc \
    --arg kind "amendment" --arg slug "$slug" --argjson result "$ledger" '
    { ok: true, kind: $kind, dispatched: "roadmap amend", target: $slug,
      result: $result }'
}

_wip_intake_apply_workplan_seed() {
  local file="$1" cli_target="$2"
  local fm target slug step_id
  fm="$(wip_intake_read_front_matter "$file")"
  if [[ -n "$cli_target" ]]; then
    target="$cli_target"
  else
    target="$(_wip_intake_fm_str "$fm" "target")"
  fi
  [[ "$target" == */* ]] ||
    wip_die 4 missing-target "intake apply: workplan-seed target must be <slug>/<step-id>" "$file"
  slug="${target%%/*}"
  step_id="${target#*/}"

  # shellcheck disable=SC1091
  source "$WIP_LIB/wip-plumbing-subcommands/workplan.bash"

  local ledger rc
  set +e
  ledger="$(wip_plumbing_cmd_workplan init "$slug" "$step_id" --from "$file")"
  rc=$?
  set -e
  [[ "$rc" == "0" ]] || exit "$rc"

  jq -nc \
    --arg kind "workplan-seed" --arg target "$target" --argjson result "$ledger" '
    { ok: true, kind: $kind, dispatched: "workplan init", target: $target,
      result: $result }'
}
