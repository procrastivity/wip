# bundle — the multi-file bundle assembler (ADR-0011 / spec wip-bundle.md).
#
# Turns N loose handoff files into ONE `bundle` lead manifest, then optionally
# chains into the existing `intake --kind bundle` explode. This verb is a
# porcelain-only front-end: it adds the assemble step (provider-shaper builds
# the manifest) and reuses the UNCHANGED plumbing validate + porcelain explode.
# No new plumbing verb; the single-file primitive and nested-bundle refusal
# both stand.
#
# Pipeline:
#   collect >=2 readable inputs → pick manifest location (common parent / -o)
#     → compute each child path relative to that location → shaper assembles +
#     ASK/validate loop → write manifest → (--intake) chain `wip intake`.
#
# The assemble shaper shares the intake shaper's machinery (preamble, response
# extractor, ASK + validate-retry loop) but reads its rules from
# templates/prompts/bundle/assemble.md (prompt-sharing seam, step-11).
# shellcheck shell=bash

wip_cmd_bundle() {
  local target="" lead_as="" output="" do_intake=0 dry_run=0 yes=0
  local -a inputs=()
  local max_rounds=2
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --target)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "bundle: --target requires an argument"
        target="$2"
        shift 2
        ;;
      --target=*)
        target="${1#--target=}"
        shift
        ;;
      --lead-as)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "bundle: --lead-as requires an argument"
        lead_as="$2"
        shift 2
        ;;
      --lead-as=*)
        lead_as="${1#--lead-as=}"
        shift
        ;;
      -o | --output)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "bundle: -o requires an argument"
        output="$2"
        shift 2
        ;;
      --output=*)
        output="${1#--output=}"
        shift
        ;;
      -o=*)
        output="${1#-o=}"
        shift
        ;;
      --intake)
        do_intake=1
        shift
        ;;
      --dry-run)
        dry_run=1
        shift
        ;;
      -y | --yes)
        yes=1
        shift
        ;;
      --)
        shift
        while [[ $# -gt 0 ]]; do
          inputs+=("$1")
          shift
        done
        ;;
      -*) wip_p_die 2 usage "bundle: unknown flag: $1" ;;
      *)
        inputs+=("$1")
        shift
        ;;
    esac
  done

  if [[ -n "$lead_as" ]]; then
    case "$lead_as" in
      brief | amendment) ;;
      *) wip_p_die 2 usage "bundle: --lead-as must be one of: brief amendment" ;;
    esac
  fi

  # >=2 readable inputs. One file is a usage error (use `wip intake` directly).
  if [[ "${#inputs[@]}" -lt 2 ]]; then
    wip_p_die 2 bundle-too-few-inputs \
      "bundle: need two or more input files (got ${#inputs[@]}); use 'wip intake <file>' for one"
  fi
  local f
  for f in "${inputs[@]}"; do
    [[ -f "$f" && -r "$f" ]] ||
      wip_p_die 2 bundle-input-unreadable "bundle: input not readable: $f" \
        "$(jq -nc --arg p "$f" '{path:$p}')"
  done

  # `-o -` emits the assembled manifest bytes to stdout (review/pipe mode);
  # it implies no chaining and no JSON envelope (precedent: `template show`).
  local to_stdout=0
  if [[ "$output" == "-" ]]; then
    to_stdout=1
    do_intake=0
  fi

  # Locate plumbing + repo root + provider (same setup as intake).
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

  # --- choose manifest location + per-child relative paths -------------------
  # children[].path is resolved by the explode relative to the manifest's dir,
  # so the manifest must live where those relative paths hold.
  local -a abs_inputs=()
  for f in "${inputs[@]}"; do
    local a
    a="$(_wip_bundle_abspath "$f")" ||
      wip_p_die 2 bundle-input-unreadable "bundle: cannot resolve path: $f"
    abs_inputs+=("$a")
  done

  local manifest_path manifest_dir abs_fallback=0
  if [[ -n "$output" && "$to_stdout" == "0" ]]; then
    manifest_path="$output"
    manifest_dir="$(dirname "$output")"
    mkdir -p "$manifest_dir" 2>/dev/null ||
      wip_p_die 2 usage "bundle: cannot create -o directory: $manifest_dir"
    manifest_dir="$(_wip_bundle_dir_abspath "$manifest_dir")" ||
      wip_p_die 2 usage "bundle: cannot resolve -o directory: $(dirname "$output")"
  else
    local common
    common="$(_wip_bundle_common_parent "${abs_inputs[@]}")"
    if [[ "$common" == "/" ]]; then
      # No shared ancestor below / — fall back to absolute child paths.
      abs_fallback=1
      manifest_dir="$PWD"
    else
      manifest_dir="$common"
    fi
    manifest_path="$manifest_dir/bundle.md"
  fi

  # Build the (relative) child path list, parallel to abs_inputs.
  local -a child_paths=()
  local warn_abs="[]"
  for f in "${abs_inputs[@]}"; do
    local cp
    if [[ "$abs_fallback" == "1" ]]; then
      cp="$f"
      warn_abs="$(jq -nc --argjson a "$warn_abs" --arg p "$f" '$a + [$p]')"
    else
      cp="$(_wip_bundle_relpath "$f" "$manifest_dir")"
    fi
    child_paths+=("$cp")
  done

  # --- assemble (shaper loop) ------------------------------------------------
  # Shape into a tempfile inside the manifest dir so the relative child paths
  # resolve during validate exactly as they will for the explode.
  local shaped_path
  shaped_path="$(mktemp "$manifest_dir/.wip-bundle-shape.XXXXXX")"
  _WIP_BUNDLE_SHAPED_PATH="$shaped_path"
  trap '_wip_bundle_cleanup_shape' EXIT

  local sys_prompt user_msg messages
  sys_prompt="$(_wip_bundle_assemble_system_prompt)"
  user_msg="$(_wip_bundle_initial_user_message "$target" "$lead_as" "$yes" child_paths abs_inputs)"
  messages="$(jq -nc --arg sys "$sys_prompt" --arg user "$user_msg" '
    [ {role:"system", content:$sys},
      {role:"user",   content:$user} ]')"

  local rounds=0 final_state="" last_missing="[]" last_body=""
  while [[ "$rounds" -lt "$max_rounds" ]]; do
    rounds=$((rounds + 1))
    local req resp
    req="$(jq -nc --arg model "$model" --argjson msgs "$messages" '{model:$model, messages:$msgs}')"
    if ! resp="$(wip_provider_chat "$req" "$cfg")"; then
      wip_p_die 1 transport-error "bundle: provider call failed at round $rounds"
    fi
    local content
    content="$(jq -r '.choices[0].message.content // empty' <<<"$resp" 2>/dev/null || true)"
    if [[ -z "$content" ]]; then
      [[ "${WIP_VERBOSE:-0}" == "1" ]] && wip_p_warn "raw response: $resp"
      wip_p_die 1 bad-response "bundle: response missing .choices[0].message.content"
    fi

    local extracted mode
    extracted="$(wip_shaper_extract_response "$content")"
    mode="$(jq -r '.mode' <<<"$extracted")"
    case "$mode" in
      ask)
        if [[ "$yes" == "1" ]]; then
          wip_p_die 4 ask-without-tty \
            "bundle: shaper asked a clarifying question under --yes" \
            "$(jq -nc --argjson e "$extracted" '{question:$e.question, why:$e.why}')"
        fi
        local q why answer
        q="$(jq -r '.question' <<<"$extracted")"
        why="$(jq -r '.why' <<<"$extracted")"
        [[ -n "$why" ]] && wip_p_warn "shaper: $why"
        answer="$(wip_p_prompt "$q")" || wip_p_die 4 ask-without-tty \
          "bundle: no tty / stdin available to answer shaper question" \
          "$(jq -nc --argjson e "$extracted" '{question:$e.question, why:$e.why}')"
        local followup
        followup="$(wip_shaper_followup_user_message "$answer")"
        messages="$(jq -nc --argjson msgs "$messages" --arg asst "$content" --arg user "$followup" '
          $msgs + [ {role:"assistant", content:$asst}, {role:"user", content:$user} ]')"
        continue
        ;;
      invalid)
        wip_p_die 1 bad-shape-response \
          "bundle: shaper response not parseable as manifest or ASK at round $rounds"
        ;;
      shape) ;;
    esac

    local body
    body="$(jq -r '.body' <<<"$extracted")"
    last_body="$body"
    printf '%s\n' "$body" >"$shaped_path"

    local validate_json validate_rc
    set +e
    validate_json="$(WIP_ROOT="$root" "$plumbing" intake validate "$shaped_path" --kind bundle 2>/dev/null)"
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
    retry_msg="$(wip_shaper_retry_user_message "bundle" "$last_missing")"
    messages="$(jq -nc --argjson msgs "$messages" --arg asst "$content" --arg user "$retry_msg" '
      $msgs + [ {role:"assistant", content:$asst}, {role:"user", content:$user} ]')"
  done

  if [[ "$final_state" != "ok" ]]; then
    local truncated_body
    truncated_body="$(printf '%s' "$last_body" | head -c 4096)"
    wip_p_die 4 shape-failed \
      "bundle: assembled manifest did not validate after $rounds round(s)" \
      "$(jq -nc --argjson missing "$last_missing" --arg body "$truncated_body" --argjson rounds "$rounds" \
        '{missing:$missing, rounds:$rounds, last_body:$body}')"
  fi

  # --- emit / write / chain --------------------------------------------------
  # Parse children[] out of the validated manifest for the envelope.
  local fm children_env
  fm="$(_wip_bundle_fm_json "$shaped_path")"
  lead_as="$(jq -r '.["lead-as"] // empty' <<<"$fm")"
  local route_target
  route_target="$(jq -r '.target // empty' <<<"$fm")"
  [[ -n "$route_target" ]] || route_target="$target"
  children_env="$(jq -c '[(.children // [])[] | if type=="object" then
      ({path:.path} + (if .lane then {lane:.lane} else {} end)
                    + (if .["depends-on"] then {depends_on:.["depends-on"]} else {} end)
                    + (if .kind then {kind:.kind} else {} end))
    else {path:.} end]' <<<"$fm")"

  # `-o -`: stream the manifest bytes to stdout, no envelope.
  if [[ "$to_stdout" == "1" ]]; then
    cat -- "$shaped_path"
    return 0
  fi

  local wrote="[]"
  if [[ "$dry_run" == "1" ]]; then
    wip_p_warn "dry-run: assembled bundle manifest (would write $manifest_path), rounds=$rounds"
  else
    cp "$shaped_path" "$manifest_path"
    wrote="$(jq -nc --arg p "$manifest_path" '[$p]')"
  fi

  # --intake: chain into the UNCHANGED `wip intake <manifest> --kind bundle`.
  # Re-exec the porcelain so the explode runs with a clean state; capture its
  # JSON envelope. (Skipped under --dry-run-only when nothing was written.)
  local intake_env="" intake_rc=0
  if [[ "$do_intake" == "1" ]]; then
    local -a iargs=("intake" "$manifest_path" "--kind" "bundle")
    [[ "$yes" == "1" ]] && iargs+=("--yes")
    [[ "$dry_run" == "1" ]] && iargs+=("--dry-run")
    set +e
    intake_env="$("$_WIP_P_SELF" "${iargs[@]}" 2>/dev/null)"
    intake_rc=$?
    set -e
    [[ -n "$intake_env" ]] || intake_env='{}'
  fi

  local ok="true"
  [[ "$do_intake" == "1" && "$intake_rc" != "0" ]] && ok="false"

  jq -nc \
    --argjson ok "$ok" \
    --arg manifest "$manifest_path" \
    --arg lead_as "$lead_as" --arg target "$route_target" \
    --argjson children "$children_env" --argjson wrote "$wrote" \
    --argjson dry "$([[ "$dry_run" == "1" ]] && printf true || printf false)" \
    --argjson did_intake "$([[ "$do_intake" == "1" ]] && printf true || printf false)" \
    --argjson intake "${intake_env:-null}" \
    --argjson warn_abs "$warn_abs" '
    { ok: $ok, verb: "bundle", manifest: $manifest,
      lead_as: $lead_as, target: $target, dry_run: $dry,
      children: $children, wrote: $wrote }
    + (if $did_intake then {intake: $intake} else {} end)
    + (if ($warn_abs | length) > 0 then {warnings: [("no common parent; using absolute child paths: " + ($warn_abs | join(", ")))]} else {} end)'

  # Don't rm the manifest on success; it's the artifact. Clear the shape trap.
  _wip_bundle_cleanup_shape
  trap - EXIT
  [[ "$ok" == "true" ]]
}

# _wip_bundle_assemble_system_prompt — preamble (output protocol + hard rules,
# shared with intake) + the bundle assembly rules. Both halves come from the
# templates dir via the same seam the intake shaper uses.
_wip_bundle_assemble_system_prompt() {
  local preamble rules
  preamble="$(_wip_shaper_preamble)"
  rules="$(_wip_bundle_read_assemble_template)"
  printf '%s\n\n%s\n' "$preamble" "$rules"
}

_wip_bundle_read_assemble_template() {
  local dir path
  dir="$(_wip_shaper_templates_dir)"
  path="$dir/prompts/bundle/assemble.md"
  [[ -f "$path" ]] || return 1
  cat -- "$path"
}

# _wip_bundle_initial_user_message <target> <lead-as> <yes> <paths-arrayname>
#   <inputs-arrayname> — build the first shape request: the flags + one block
# per input file (its child path + content).
_wip_bundle_initial_user_message() {
  local target="$1" lead_as="$2" yes_mode="$3"
  local -n _paths="$4"
  local -n _inputs="$5"
  local guidance
  if [[ "$yes_mode" == "1" ]]; then
    guidance="Mode: non-interactive (--yes). Do NOT emit ASK. Use your best
judgment for any unclear lane/dependency/target."
  else
    guidance="Mode: interactive. If target / lead-as / a child's lane or
dependency is unclear, emit a single ASK per the protocol."
  fi
  printf '# Assembly request\n\n'
  printf -- '- target: %s\n' "${target:-<infer>}"
  printf -- '- lead-as: %s\n\n' "${lead_as:-<infer>}"
  printf '# Input files (use each path EXACTLY as given in children[].path)\n\n'
  local i
  for i in "${!_inputs[@]}"; do
    printf '## child path: %s\n\n' "${_paths[$i]}"
    printf '```\n'
    cat -- "${_inputs[$i]}"
    printf '\n```\n\n'
  done
  printf '# Task\n\nAssemble these into one bundle lead manifest per the rules in your system prompt.\n%s\n' "$guidance"
}

# _wip_bundle_abspath <file> — canonical absolute path of an existing file.
_wip_bundle_abspath() {
  local p="$1" dir base
  dir="$(dirname "$p")"
  base="$(basename "$p")"
  # shellcheck disable=SC1007  # CDPATH= neutralizes CDPATH, not an assignment
  dir="$(CDPATH= cd -- "$dir" 2>/dev/null && pwd)" || return 1
  printf '%s/%s' "$dir" "$base"
}

# _wip_bundle_dir_abspath <dir> — canonical absolute path of an existing dir.
_wip_bundle_dir_abspath() {
  # shellcheck disable=SC1007
  CDPATH= cd -- "$1" 2>/dev/null && pwd
}

# _wip_bundle_common_parent <abs-file>... — longest common parent directory of
# the inputs (their dirnames' shared ancestor). "/" when fully disjoint.
_wip_bundle_common_parent() {
  local common
  common="$(dirname "$1")"
  shift
  local f d
  for f in "$@"; do
    d="$(dirname "$f")"
    while [[ "$d" != "$common" && "$d" != "$common/"* ]]; do
      [[ "$common" == "/" ]] && break
      common="$(dirname "$common")"
    done
  done
  printf '%s' "$common"
}

# _wip_bundle_relpath <target-abs> <base-abs-dir> — target path relative to base.
_wip_bundle_relpath() {
  local target="$1" base="$2" up=""
  while [[ "$target" != "$base" && "$target" != "$base/"* ]]; do
    [[ "$base" == "/" ]] && break
    base="$(dirname "$base")"
    up="../$up"
  done
  local rest="${target#"$base"}"
  rest="${rest#/}"
  printf '%s' "${up}${rest}"
}

# _wip_bundle_fm_json <file> — YAML front-matter as compact JSON ({} if absent).
_wip_bundle_fm_json() {
  local file="$1" yaml json
  yaml="$(awk '
    NR == 1 && /^---[[:space:]]*$/ { in_fm = 1; next }
    in_fm && /^---[[:space:]]*$/ { exit }
    in_fm { print }
  ' "$file")"
  if [[ -z "$yaml" ]]; then
    printf '{}'
    return 0
  fi
  json="$(printf '%s\n' "$yaml" | yq -o=json '.' 2>/dev/null)"
  if [[ -z "$json" || "$json" == "null" ]]; then
    printf '{}'
  else
    printf '%s' "$json" | jq -c '.'
  fi
}

# Trap target — safe under `set -u` (global may be unset).
_wip_bundle_cleanup_shape() {
  if [[ -n "${_WIP_BUNDLE_SHAPED_PATH:-}" ]]; then
    rm -f "$_WIP_BUNDLE_SHAPED_PATH"
    _WIP_BUNDLE_SHAPED_PATH=""
  fi
}
