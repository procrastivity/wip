# intake — the porcelain shaper/router. ADR-0009 phases 2 + 4.
#
# Pipeline (per state machine in the workplan):
#   classify (shellout) → pick-kind → shape loop (LLM ± ASK + validate retry)
#     → route (derive --target from front-matter) → apply (shellout)
#
# Every LLM call routes through wip_provider_chat so WIP_PROVIDER_CMD keeps
# the suite network-free. The shaped artifact lives in $TMPDIR; --output
# persists it; --dry-run skips apply entirely.
# shellcheck shell=bash

wip_cmd_intake() {
  local file="" kind="" target="" yes=0 dry_run=0 output="" max_rounds=2
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --kind)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "intake: --kind requires an argument"
        kind="$2"
        shift 2
        ;;
      --kind=*)
        kind="${1#--kind=}"
        shift
        ;;
      --target)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "intake: --target requires an argument"
        target="$2"
        shift 2
        ;;
      --target=*)
        target="${1#--target=}"
        shift
        ;;
      --output)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "intake: --output requires an argument"
        output="$2"
        shift 2
        ;;
      --output=*)
        output="${1#--output=}"
        shift
        ;;
      --max-rounds)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "intake: --max-rounds requires an argument"
        max_rounds="$2"
        shift 2
        ;;
      --max-rounds=*)
        max_rounds="${1#--max-rounds=}"
        shift
        ;;
      -y | --yes)
        yes=1
        shift
        ;;
      --dry-run)
        dry_run=1
        shift
        ;;
      --)
        shift
        break
        ;;
      -*) wip_p_die 2 usage "intake: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_p_die 2 usage "intake: unexpected arg: $1"
        fi
        ;;
    esac
  done

  [[ -n "$file" ]] || wip_p_die 2 usage "intake: missing <file>"
  [[ -f "$file" && -r "$file" ]] ||
    wip_p_die 2 not-found "intake: file not readable: $file"

  if [[ -n "$kind" ]]; then
    case "$kind" in
      brief | amendment | workplan-seed | spec | handoff) ;;
      *) wip_p_die 2 usage "intake: --kind must be one of: brief amendment workplan-seed spec handoff" ;;
    esac
  fi

  # Clamp max-rounds to >=1 — round 1 is the initial shape attempt; retries
  # are rounds 2..N. max-rounds=0 would mean "never call the LLM," which
  # isn't a useful state to support; clamp + warn.
  if [[ "$max_rounds" =~ ^[0-9]+$ ]] && [[ "$max_rounds" -lt 1 ]]; then
    max_rounds=1
    [[ "${WIP_VERBOSE:-0}" == "1" ]] && wip_p_warn "intake: --max-rounds clamped to 1"
  elif ! [[ "$max_rounds" =~ ^[0-9]+$ ]]; then
    wip_p_die 2 usage "intake: --max-rounds must be a non-negative integer"
  fi

  # Locate the plumbing binary and the repo root upfront — both shellouts
  # and the provider need them.
  local plumbing
  plumbing="$(wip_p_find_plumbing)" ||
    wip_p_die 3 no-plumbing "could not locate wip-plumbing binary"

  local root="" d="$PWD"
  if [[ -n "${WIP_ROOT:-}" ]]; then
    if [[ -f "$WIP_ROOT/.wip.yaml" ]]; then
      root="$WIP_ROOT"
    else
      wip_p_die 3 no-manifest "WIP_ROOT=$WIP_ROOT has no .wip.yaml"
    fi
  else
    while :; do
      if [[ -f "$d/.wip.yaml" ]]; then
        root="$d"
        break
      fi
      [[ "$d" == "/" ]] && break
      d="$(dirname "$d")"
    done
    [[ -n "$root" ]] || wip_p_die 3 no-manifest "no .wip.yaml found from $PWD upward"
  fi

  wip_provider_load "$root"
  local cfg="$WIP_PROVIDER_CFG"
  local model
  model="$(jq -r '.model' <<<"$cfg")"

  local asked_json="[]"
  local rounds=0

  # --- phase 1: classify -----------------------------------------------------
  local classify_json
  if ! classify_json="$(_wip_intake_run_classify "$plumbing" "$root" "$file")"; then
    wip_p_die 4 classify-failed "intake: plumbing classify rejected $file (no title or unparseable)"
  fi
  local class_kind class_conf
  class_kind="$(jq -r '.kind' <<<"$classify_json")"
  class_conf="$(jq -r '.confidence' <<<"$classify_json")"

  # --- phase 2: pick kind ----------------------------------------------------
  if [[ -z "$kind" ]]; then
    if [[ "$class_conf" == "high" ]]; then
      kind="$class_kind"
    elif [[ "$yes" == "1" ]]; then
      wip_p_die 4 kind-ambiguous \
        "intake: classify confidence=$class_conf for kind=$class_kind; pass --kind or drop --yes" \
        "$(jq -nc --argjson c "$classify_json" '{classify:$c}')"
    else
      local ans
      ans="$(wip_p_prompt "classify guess: $class_kind ($class_conf). Accept? [kind to override, blank=accept, q=quit]")" || ans="q"
      case "$ans" in
        "" | y | Y | yes) kind="$class_kind" ;;
        q | quit) wip_p_die 4 kind-ambiguous "intake: cancelled at kind-pick" ;;
        *)
          case "$ans" in
            brief | amendment | workplan-seed | spec | handoff) kind="$ans" ;;
            *) wip_p_die 2 usage "intake: kind override must be one of: brief amendment workplan-seed spec handoff" ;;
          esac
          ;;
      esac
    fi
  fi

  # --- phase 3: shape loop ---------------------------------------------------
  _WIP_INTAKE_SHAPED_PATH="$(_wip_intake_mktemp)"
  local shaped_path="$_WIP_INTAKE_SHAPED_PATH"
  trap '_wip_intake_cleanup_shape' EXIT

  local messages
  local sys_prompt user_msg
  sys_prompt="$(wip_shaper_system_prompt "$kind")"
  user_msg="$(wip_shaper_initial_user_message "$kind" "$classify_json" "$file" "$yes")"
  messages="$(jq -nc --arg sys "$sys_prompt" --arg user "$user_msg" '
    [ {role:"system", content:$sys},
      {role:"user",   content:$user} ]')"

  local last_missing="[]"
  local last_body=""
  local final_state=""
  while [[ "$rounds" -lt "$max_rounds" ]]; do
    rounds=$((rounds + 1))
    local req resp
    req="$(jq -nc --arg model "$model" --argjson msgs "$messages" '
      { model: $model, messages: $msgs }')"
    if ! resp="$(wip_provider_chat "$req" "$cfg")"; then
      wip_p_die 1 transport-error "intake: provider call failed at round $rounds"
    fi
    local content
    content="$(jq -r '.choices[0].message.content // empty' <<<"$resp" 2>/dev/null || true)"
    if [[ -z "$content" ]]; then
      if [[ "${WIP_VERBOSE:-0}" == "1" ]]; then
        wip_p_warn "raw response: $resp"
      fi
      wip_p_die 1 bad-response "intake: response missing .choices[0].message.content"
    fi

    local extracted
    extracted="$(wip_shaper_extract_response "$content")"
    local mode
    mode="$(jq -r '.mode' <<<"$extracted")"

    case "$mode" in
      ask)
        if [[ "$yes" == "1" ]]; then
          wip_p_die 4 ask-without-tty \
            "intake: shaper asked a clarifying question under --yes" \
            "$(jq -nc --argjson e "$extracted" '{question:$e.question, why:$e.why}')"
        fi
        local q why answer
        q="$(jq -r '.question' <<<"$extracted")"
        why="$(jq -r '.why' <<<"$extracted")"
        if [[ -n "$why" ]]; then
          wip_p_warn "shaper: $why"
        fi
        answer="$(wip_p_prompt "$q")" || wip_p_die 4 ask-without-tty \
          "intake: no tty / stdin available to answer shaper question" \
          "$(jq -nc --argjson e "$extracted" '{question:$e.question, why:$e.why}')"
        asked_json="$(jq -nc --argjson a "$asked_json" --arg q "$q" '$a + [$q]')"
        local followup
        followup="$(wip_shaper_followup_user_message "$answer")"
        messages="$(jq -nc --argjson msgs "$messages" --arg asst "$content" --arg user "$followup" '
          $msgs + [ {role:"assistant", content:$asst}, {role:"user", content:$user} ]')"
        continue
        ;;
      invalid)
        wip_p_die 1 bad-shape-response \
          "intake: shaper response not parseable as shape or ASK at round $rounds"
        ;;
      shape) ;;
    esac

    local body
    body="$(jq -r '.body' <<<"$extracted")"
    last_body="$body"
    printf '%s\n' "$body" >"$shaped_path"

    local validate_json validate_rc
    set +e
    validate_json="$(_wip_intake_run_validate "$plumbing" "$root" "$shaped_path" "$kind")"
    validate_rc=$?
    set -e

    if [[ "$validate_rc" == "0" ]]; then
      final_state="ok"
      break
    fi

    last_missing="$(jq -c '.missing // []' <<<"$validate_json" 2>/dev/null || printf '[]')"
    if [[ "$rounds" -ge "$max_rounds" ]]; then
      final_state="shape-failed"
      break
    fi

    local retry_msg
    retry_msg="$(wip_shaper_retry_user_message "$kind" "$last_missing")"
    messages="$(jq -nc --argjson msgs "$messages" --arg asst "$content" --arg user "$retry_msg" '
      $msgs + [ {role:"assistant", content:$asst}, {role:"user", content:$user} ]')"
  done

  if [[ "$final_state" != "ok" ]]; then
    local truncated_body
    truncated_body="$(printf '%s' "$last_body" | head -c 4096)"
    wip_p_die 4 shape-failed \
      "intake: shape did not validate after $rounds round(s)" \
      "$(jq -nc --argjson missing "$last_missing" --arg body "$truncated_body" --argjson rounds "$rounds" \
        '{missing:$missing, rounds:$rounds, last_body:$body}')"
  fi

  # Persist shaped artifact if --output given.
  if [[ -n "$output" ]]; then
    cp "$shaped_path" "$output"
  fi

  # --- phase 4: route --------------------------------------------------------
  local route_target=""
  case "$kind" in
    brief)
      if [[ -n "$target" ]]; then
        route_target="$target"
      else
        route_target="$(WIP_LIB="${WIP_LIB:-}" _wip_intake_derive_slug "$shaped_path")"
        if [[ -z "$route_target" ]]; then
          wip_p_die 4 shape-failed "intake: brief shaped without a derivable slug" \
            "$(jq -nc --arg path "$shaped_path" '{shaped_path:$path}')"
        fi
        if [[ "$yes" != "1" && "$dry_run" != "1" ]]; then
          if ! wip_p_confirm "create initiative '$route_target' from shaped brief?"; then
            wip_p_die 4 kind-ambiguous "intake: cancelled at route confirmation"
          fi
        fi
      fi
      ;;
    amendment | workplan-seed)
      if [[ -n "$target" ]]; then
        route_target="$target"
      else
        route_target="$(_wip_intake_read_fm_target "$shaped_path")"
        [[ -n "$route_target" ]] || wip_p_die 4 shape-failed \
          "intake: shaped $kind missing 'target:' front-matter" \
          "$(jq -nc --arg path "$shaped_path" '{shaped_path:$path}')"
      fi
      ;;
    spec | handoff)
      route_target=""
      ;;
  esac

  # --- phase 5: apply (or skip on --dry-run) ---------------------------------
  if [[ "$dry_run" == "1" ]]; then
    jq -nc \
      --arg kind "$kind" --arg target "$route_target" \
      --arg shaped "$shaped_path" --argjson asked "$asked_json" \
      --argjson rounds "$rounds" '
      { ok: true, dry_run: true, kind: $kind, target: $target,
        rounds: $rounds, asked: $asked, shaped_path: $shaped }'
    wip_p_warn "dry-run: shaped kind=$kind, target=$route_target, rounds=$rounds, shaped_path=$shaped_path"
    # Don't rm the shaped artifact under --dry-run so the user can inspect.
    _WIP_INTAKE_SHAPED_PATH=""
    trap - EXIT
    return 0
  fi

  local apply_json apply_rc
  set +e
  apply_json="$(_wip_intake_run_apply "$plumbing" "$root" "$shaped_path" "$kind" "$route_target")"
  apply_rc=$?
  set -e
  if [[ "$apply_rc" != "0" ]]; then
    wip_p_die 4 apply-failed \
      "intake: apply rejected the shaped artifact (rc=$apply_rc)" \
      "$(jq -nc --argjson r "$apply_json" '{apply:$r}')"
  fi

  jq -nc \
    --arg kind "$kind" --arg target "$route_target" \
    --argjson asked "$asked_json" --argjson rounds "$rounds" \
    --argjson result "$apply_json" '
    { ok: true, kind: $kind, target: $target,
      rounds: $rounds, asked: $asked, result: $result }'
  wip_p_warn "intake: kind=$kind target=$route_target rounds=$rounds applied"
}

_wip_intake_mktemp() {
  mktemp -t wip-intake-shape.XXXXXX
}

# Trap target — safe under `set -u` because the global may be unset.
_wip_intake_cleanup_shape() {
  if [[ -n "${_WIP_INTAKE_SHAPED_PATH:-}" ]]; then
    rm -f "$_WIP_INTAKE_SHAPED_PATH"
    _WIP_INTAKE_SHAPED_PATH=""
  fi
}

_wip_intake_run_classify() {
  local plumbing="$1" root="$2" file="$3"
  local out rc
  set +e
  out="$(WIP_ROOT="$root" "$plumbing" intake classify "$file" 2>/dev/null)"
  rc=$?
  set -e
  if [[ "$rc" != "0" ]]; then
    return "$rc"
  fi
  printf '%s' "$out"
}

_wip_intake_run_validate() {
  local plumbing="$1" root="$2" file="$3" kind="$4"
  local out rc
  set +e
  out="$(WIP_ROOT="$root" "$plumbing" intake validate "$file" --kind "$kind" 2>/dev/null)"
  rc=$?
  set -e
  printf '%s' "$out"
  return "$rc"
}

_wip_intake_run_apply() {
  local plumbing="$1" root="$2" file="$3" kind="$4" target="$5"
  local out rc
  set +e
  if [[ -n "$target" ]]; then
    out="$(WIP_ROOT="$root" "$plumbing" intake apply "$file" --kind "$kind" --target "$target" 2>/dev/null)"
  else
    out="$(WIP_ROOT="$root" "$plumbing" intake apply "$file" --kind "$kind" 2>/dev/null)"
  fi
  rc=$?
  set -e
  printf '%s' "$out"
  return "$rc"
}

# _wip_intake_read_fm_target <file> — pull `target:` out of YAML front-matter.
_wip_intake_read_fm_target() {
  local file="$1"
  awk '
    NR == 1 && /^---[[:space:]]*$/ { in_fm = 1; next }
    in_fm && /^---[[:space:]]*$/ { exit }
    in_fm && /^target:[[:space:]]*/ {
      sub(/^target:[[:space:]]*/, "")
      sub(/[[:space:]]+$/, "")
      print
      exit
    }
  ' "$file"
}

# _wip_intake_derive_slug <file> — slug from front-matter `slug:`, else
# kebab-cased H1.
_wip_intake_derive_slug() {
  local file="$1" fm_slug h1
  fm_slug="$(awk '
    NR == 1 && /^---[[:space:]]*$/ { in_fm = 1; next }
    in_fm && /^---[[:space:]]*$/ { exit }
    in_fm && /^slug:[[:space:]]*/ {
      sub(/^slug:[[:space:]]*/, "")
      sub(/[[:space:]]+$/, "")
      print
      exit
    }
  ' "$file")"
  if [[ -n "$fm_slug" ]]; then
    printf '%s' "$fm_slug"
    return 0
  fi
  h1="$(awk '/^# [^[:space:]]/ { sub(/^# +/, ""); print; exit }' "$file")"
  [[ -n "$h1" ]] || return 0
  printf '%s' "$h1" | tr '[:upper:]' '[:lower:]' |
    sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//'
}
