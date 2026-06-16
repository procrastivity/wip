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
      brief | amendment | workplan-seed | spec | handoff | bundle) ;;
      *) wip_p_die 2 usage "intake: --kind must be one of: brief amendment workplan-seed spec handoff bundle" ;;
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

  _wip_intake_single_file "$file" "$kind" "$target" 0
}

# _wip_intake_single_file <file> <kind> <target> <is_child> — the full intake
# pipeline for ONE file (ADR-0009 phases 1-5). Inherits the run flags + setup
# (yes, dry_run, output, max_rounds, plumbing, root, cfg, model) from
# wip_cmd_intake via dynamic scope. The top-level call passes is_child=0; the
# bundle explode re-enters this per child with is_child=1. A child may never
# resolve to kind=bundle (nested bundles are refused). For a bundle, after the
# shape loop the function hands off to _wip_intake_explode_bundle instead of
# routing + applying.
_wip_intake_single_file() {
  local file="$1" kind="$2" target="$3" is_child="${4:-0}"
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
            brief | amendment | workplan-seed | spec | handoff | bundle) kind="$ans" ;;
            *) wip_p_die 2 usage "intake: kind override must be one of: brief amendment workplan-seed spec handoff bundle" ;;
          esac
          ;;
      esac
    fi
  fi

  # A bundle child must never itself be a bundle (ADR-0009 / no nested bundles).
  if [[ "$is_child" == "1" && "$kind" == "bundle" ]]; then
    wip_p_die 4 nested-bundle \
      "intake: a bundle child cannot itself be a bundle (nested bundles are not allowed)"
  fi

  # --- phase 3: shape loop ---------------------------------------------------
  # A bundle's children paths are relative to the LEAD doc, so the shaped bundle
  # artifact must live in the source directory for the validator (and explode)
  # to resolve them. Other kinds shape into $TMPDIR.
  if [[ "$kind" == "bundle" ]]; then
    _WIP_INTAKE_SHAPED_PATH="$(_wip_intake_mktemp_in "$(dirname "$file")")"
  else
    _WIP_INTAKE_SHAPED_PATH="$(_wip_intake_mktemp)"
  fi
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

  # Persist shaped artifact if --output given (top-level only — children share
  # the top-level invocation's --output but must not overwrite it).
  if [[ "$is_child" == "0" && -n "$output" ]]; then
    cp "$shaped_path" "$output"
  fi

  # --- bundle: explode instead of route + apply (ADR-0009 phase 2/4) ---------
  # A bundle is non-terminal: the shaped artifact is a lead doc + a children
  # manifest. Fan out into one lead intake + per-child intakes (each reusing
  # this same single-file pipeline). --dry-run fans out without applying.
  if [[ "$kind" == "bundle" ]]; then
    _wip_intake_explode_bundle "$shaped_path" "$file"
    return $?
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

# _wip_intake_mktemp_in <dir> — a shape tempfile inside <dir> (so a bundle's
# relative child paths resolve against the lead doc's directory).
_wip_intake_mktemp_in() {
  mktemp "$1/.wip-intake-shape.XXXXXX"
}

# _wip_intake_fm_json <file> — emit the YAML front-matter as compact JSON
# (`{}` when absent or unparseable). Mirrors the plumbing reader; the porcelain
# needs it to walk a bundle's structured children[]/cross-cuts manifest.
_wip_intake_fm_json() {
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

# --- bundle explode --------------------------------------------------------
# A bundle is a lead doc + a children manifest. The explode (ADR-0009 phase 2/4)
# applies the lead, then each child, reusing the single-file pipeline. The
# parallelism/cross-cuts the lead names become on-disk lane + Cross-cuts
# structure (ADR-0010). Per-child apply is independent / non-atomic.

# _wip_intake_materialize_lead <shaped-bundle> <fm-json> <lead-as> — emit the
# lead artifact: the bundle body with bundle-only front-matter keys stripped,
# plus (for an amendment lead) one empty `### Lane <name>` per distinct child
# lane and a `## Cross-cuts (from bundle)` section from cross-cuts.shared-seams.
# The lanes/Cross-cuts go AFTER the round's main steps so they don't split the
# round (a `## ` heading would otherwise end it).
_wip_intake_materialize_lead() {
  local shaped="$1" fm="$2" lead_as="$3"
  local fm_yaml stripped body
  fm_yaml="$(awk 'NR==1&&/^---[[:space:]]*$/{f=1;next} f&&/^---[[:space:]]*$/{exit} f{print}' "$shaped")"
  body="$(awk 'NR==1&&/^---[[:space:]]*$/{f=1;next} f&&!p&&/^---[[:space:]]*$/{p=1;next} f&&!p{next}{print}' "$shaped")"
  stripped="$(printf '%s\n' "$fm_yaml" |
    yq -o=yaml 'del(.["wip-kind"]) | del(.["lead-as"]) | del(.children) | del(.["cross-cuts"])' 2>/dev/null)"
  body="$(printf '%s\n' "$body" |
    awk '{l[NR]=$0} END{last=NR; while(last>0&&l[last]~/^[[:space:]]*$/)last--; for(i=1;i<=last;i++)print l[i]}')"

  printf -- '---\n%s\n---\n%s\n' "$stripped" "$body"

  [[ "$lead_as" == "amendment" ]] || return 0

  local lane
  while IFS= read -r lane; do
    [[ -n "$lane" && "$lane" != "null" ]] || continue
    printf '\n### Lane %s\n' "$lane"
  done < <(jq -r '[.children[]? | if type=="object" then (.lane // empty) else empty end] | unique[]' <<<"$fm" 2>/dev/null)

  local seams s
  seams="$(jq -r '(.["cross-cuts"].["shared-seams"] // [])[]?' <<<"$fm" 2>/dev/null)"
  if [[ -n "$seams" ]]; then
    printf '\n## Cross-cuts (from bundle)\n\n'
    while IFS= read -r s; do
      [[ -n "$s" ]] && printf -- '- %s\n' "$s"
    done <<<"$seams"
  fi
}

# _wip_intake_child_order <children-json> — emit child indices (one per line) in
# dependency order: a child's `depends-on` (a path or id of a sibling) is emitted
# first. Unknown deps are ignored; a cycle falls back to manifest order.
_wip_intake_child_order() {
  local children="$1" n i
  n="$(jq -r 'length' <<<"$children")"
  local -a key1 key2 deps_raw done_flag
  for ((i = 0; i < n; i++)); do
    key1[i]="$(jq -r ".[$i] | if type==\"object\" then (.id // .path // \"\") else . end" <<<"$children")"
    key2[i]="$(jq -r ".[$i] | if type==\"object\" then (.path // \"\") else . end" <<<"$children")"
    deps_raw[i]="$(jq -r ".[$i] | if type==\"object\" then ((.[\"depends-on\"] // []) | if type==\"array\" then .[] else . end) else empty end" <<<"$children")"
    done_flag[i]=0
  done
  local emitted_count=0
  while ((emitted_count < n)); do
    local progressed=0
    for ((i = 0; i < n; i++)); do
      [[ "${done_flag[i]}" == "1" ]] && continue
      local ok=1 d j di
      while IFS= read -r d; do
        [[ -n "$d" ]] || continue
        di=""
        for ((j = 0; j < n; j++)); do
          if [[ "$d" == "${key1[j]}" || "$d" == "${key2[j]}" ]]; then
            di="$j"
            break
          fi
        done
        [[ -n "$di" ]] || continue
        [[ "${done_flag[di]}" == "1" ]] || {
          ok=0
          break
        }
      done <<<"${deps_raw[i]}"
      if [[ "$ok" == "1" ]]; then
        printf '%d\n' "$i"
        done_flag[i]=1
        emitted_count=$((emitted_count + 1))
        progressed=1
      fi
    done
    if [[ "$progressed" == "0" ]]; then
      for ((i = 0; i < n; i++)); do
        [[ "${done_flag[i]}" == "0" ]] || continue
        printf '%d\n' "$i"
        done_flag[i]=1
        emitted_count=$((emitted_count + 1))
      done
    fi
  done
}

# _wip_intake_seed_child <child-abs-path> <kind> <slug> <directive-line> [<round>]
# Emit a seed artifact: front-matter (wip-kind + target + the bundle-assigned
# directive) followed by the child doc's body (any existing front-matter
# stripped). The shaper reshapes the body into proper step form; the structural
# directive is pinned deterministically here, not left to the LLM.
_wip_intake_seed_child() {
  local cpath="$1" kind="$2" slug="$3" directive="$4" tround="${5:-}"
  printf -- '---\n'
  printf 'wip-kind: %s\n' "$kind"
  [[ "$kind" != "brief" ]] && printf 'target: %s\n' "$slug"
  [[ -n "$directive" ]] && printf '%s\n' "$directive"
  [[ -n "$tround" ]] && printf 'target-round: %s\n' "$tround"
  printf -- '---\n'
  awk 'NR==1&&/^---[[:space:]]*$/{f=1;next} f&&!p&&/^---[[:space:]]*$/{p=1;next} f&&!p{next}{print}' "$cpath"
}

# _wip_intake_explode_bundle <shaped-bundle-file> <source-file>
# Fan a validated bundle out into one lead intake + per-child intakes. Inherits
# run flags + setup from _wip_intake_single_file via dynamic scope (yes, dry_run,
# plumbing, root, cfg, model, target). Children resolve relative to the source
# file's directory. Returns 0 iff the lead and every applied child succeeded.
_wip_intake_explode_bundle() {
  local shaped="$1" source_file="$2"
  local base fm lead_as slug
  base="$(dirname "$source_file")"
  fm="$(_wip_intake_fm_json "$shaped")"
  lead_as="$(jq -r '.["lead-as"] // empty' <<<"$fm")"
  [[ -n "$lead_as" ]] || lead_as="amendment"
  slug="$(jq -r '.target // empty' <<<"$fm")"
  [[ -n "$slug" ]] || slug="${target:-}"

  # --- materialize + apply (or, under --dry-run, validate) the lead ---------
  local lead_tmp
  lead_tmp="$(_wip_intake_mktemp)"
  _wip_intake_materialize_lead "$shaped" "$fm" "$lead_as" >"$lead_tmp"
  local round_n
  round_n="$(awk '/^## Round [0-9]+ —/ { print $3; exit }' "$lead_tmp")"

  local lead_env lead_ok="false" lead_rc
  if [[ "$dry_run" == "1" ]]; then
    set +e
    lead_env="$(_wip_intake_run_validate "$plumbing" "$root" "$lead_tmp" "$lead_as")"
    lead_rc=$?
    set -e
    [[ "$lead_rc" == "0" ]] && lead_ok="true"
  else
    set +e
    lead_env="$(_wip_intake_run_apply "$plumbing" "$root" "$lead_tmp" "$lead_as" "$slug")"
    lead_rc=$?
    set -e
    [[ "$lead_rc" == "0" ]] && lead_ok="true"
  fi
  [[ -n "$lead_env" ]] || lead_env='{}'
  rm -f "$lead_tmp"

  # --- children: topo-sort, seed, run each through the single-file pipeline --
  local children_json
  children_json="$(jq -c '.children // []' <<<"$fm")"
  local child_envs="[]" all_ok="$lead_ok" idx
  while IFS= read -r idx; do
    [[ -n "$idx" ]] || continue
    local centry cpath ckind clane ctarget abs
    centry="$(jq -c ".[$idx]" <<<"$children_json")"
    cpath="$(jq -r 'if type=="object" then (.path // "") else . end' <<<"$centry")"
    ckind="$(jq -r 'if type=="object" then (.kind // "amendment") else "amendment" end' <<<"$centry")"
    clane="$(jq -r 'if type=="object" then (.lane // "") else "" end' <<<"$centry")"
    ctarget="$(jq -r 'if type=="object" then (.target // "") else "" end' <<<"$centry")"
    [[ -n "$ctarget" ]] || ctarget="$slug"

    if [[ -z "$cpath" ]]; then
      child_envs="$(jq -nc --argjson a "$child_envs" '$a + [{ok:false, skipped:"no-path"}]')"
      all_ok="false"
      continue
    fi
    if [[ "$cpath" == /* ]]; then abs="$cpath"; else abs="$base/$cpath"; fi

    # Resolve the bundle-assigned directive for an amendment child.
    local directive="" tround=""
    if [[ "$ckind" == "amendment" ]]; then
      local hint_ia hint_rep hint_ar hint_al
      hint_ia="$(jq -r 'if type=="object" then (.["insert-after"] // "") else "" end' <<<"$centry")"
      hint_rep="$(jq -r 'if type=="object" then (.replace // "") else "" end' <<<"$centry")"
      hint_ar="$(jq -r 'if type=="object" then (.["append-round"] // "") else "" end' <<<"$centry")"
      hint_al="$(jq -r 'if type=="object" then (.["append-lane"] // "") else "" end' <<<"$centry")"
      if [[ -n "$clane" ]]; then
        directive="insert-step-in-lane: $clane"
        tround="$round_n"
      elif [[ -n "$hint_ia" ]]; then
        directive="insert-after: $hint_ia"
      elif [[ -n "$hint_rep" ]]; then
        directive="replace: $hint_rep"
      elif [[ -n "$hint_ar" ]]; then
        directive="append-round: $hint_ar"
      elif [[ -n "$hint_al" ]]; then
        directive="append-lane: $hint_al"
        tround="$round_n"
      else
        # No lane and no directive: this child's content lives in the lead body
        # (folded). Record it and move on — nothing to apply.
        child_envs="$(jq -nc --argjson a "$child_envs" --arg p "$cpath" '$a + [{ok:true, path:$p, skipped:"folded-into-lead"}]')"
        continue
      fi
    fi

    # Seed + run the child through the full pipeline (subshell isolates its
    # exit + temp-file trap; a child wip_p_die becomes a captured envelope).
    local seed
    seed="$(_wip_intake_mktemp)"
    _wip_intake_seed_child "$abs" "$ckind" "$ctarget" "$directive" "$tround" >"$seed"
    local cenv crc
    set +e
    cenv="$(_wip_intake_single_file "$seed" "$ckind" "$ctarget" 1)"
    crc=$?
    set -e
    rm -f "$seed"
    [[ -n "$cenv" ]] || cenv='{}'
    child_envs="$(jq -nc --argjson a "$child_envs" --arg p "$cpath" --argjson e "$cenv" \
      --argjson ok "$([[ "$crc" == "0" ]] && printf true || printf false)" \
      '$a + [{ok:$ok, path:$p, result:$e}]')"
    [[ "$crc" == "0" ]] || all_ok="false"
  done < <(_wip_intake_child_order "$children_json")

  # --- aggregate envelope ----------------------------------------------------
  local n_children n_applied
  n_children="$(jq -r 'length' <<<"$child_envs")"
  n_applied="$(jq -r '[.[] | select(.skipped | not)] | length' <<<"$child_envs")"
  jq -nc \
    --argjson ok "$([[ "$all_ok" == "true" ]] && printf true || printf false)" \
    --arg slug "$slug" --arg lead_as "$lead_as" \
    --argjson dry "$([[ "$dry_run" == "1" ]] && printf true || printf false)" \
    --argjson lead_ok "$([[ "$lead_ok" == "true" ]] && printf true || printf false)" \
    --argjson lead "$lead_env" --argjson children "$child_envs" \
    --argjson nc "$n_children" --argjson na "$n_applied" '
    { ok: $ok, kind: "bundle", dry_run: $dry, target: $slug,
      lead: { ok: $lead_ok, lead_as: $lead_as, result: $lead },
      children: $children,
      summary: { children: $nc, applied: $na } }'

  [[ "$all_ok" == "true" ]]
}
