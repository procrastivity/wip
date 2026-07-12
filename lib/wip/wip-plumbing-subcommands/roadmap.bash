# roadmap — deterministic edits to an initiative's roadmap.md. v1 ships
# only `roadmap amend <slug> --from <file>`. Reuses intake amendment shape
# rules; idempotent via SHA-256-of-rendered-payload markers.
# shellcheck shell=bash

wip_plumbing_cmd_roadmap() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    amend) _wip_roadmap_cmd_amend "$@" ;;
    parse) _wip_roadmap_cmd_parse "$@" ;;
    "") wip_die 2 usage "roadmap: missing subcommand (amend, parse)" ;;
    *) wip_die 2 usage "roadmap: unknown subcommand: $sub" ;;
  esac
}

# roadmap parse <file> — emit the parsed roadmap JSON document (read-only).
# Missing file => empty document (exit 0). The grammar — including lanes
# (ADR-0010) — is documented in wip-plumbing-roadmap-lib.bash.
_wip_roadmap_cmd_parse() {
  local file=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -*) wip_die 2 usage "roadmap parse: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_die 2 usage "roadmap parse: unexpected arg: $1"
        fi
        ;;
    esac
  done
  [[ -n "$file" ]] || wip_die 2 usage "roadmap parse: missing <file>"
  wip_roadmap_parse "$file"
}

_wip_roadmap_cmd_amend() {
  local slug="" from="" cli_kind="" cli_value="" cli_target_round=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --from)
        [[ $# -ge 2 ]] || wip_die 2 usage "roadmap amend: --from requires an argument"
        from="$2"
        shift 2
        ;;
      --from=*)
        from="${1#--from=}"
        shift
        ;;
      --target-round)
        [[ $# -ge 2 ]] || wip_die 2 usage "roadmap amend: --target-round requires an argument"
        cli_target_round="$2"
        shift 2
        ;;
      --target-round=*)
        cli_target_round="${1#--target-round=}"
        shift
        ;;
      --insert-after | --insert-after=* | --replace | --replace=* | --append-round | --append-round=* | --append-lane | --append-lane=* | --insert-step-in-lane | --insert-step-in-lane=*)
        _wip_roadmap_amend_parse_dir "$1" "${2:-}" cli_kind cli_value
        if [[ "$1" == *=* ]]; then
          shift
        else
          shift 2
        fi
        ;;
      -*) wip_die 2 usage "roadmap amend: unknown flag: $1" ;;
      *)
        if [[ -z "$slug" ]]; then
          slug="$1"
          shift
        else
          wip_die 2 usage "roadmap amend: unexpected arg: $1"
        fi
        ;;
    esac
  done

  [[ -n "$slug" ]] || wip_die 2 usage "roadmap amend: missing <slug>"
  [[ -n "$from" ]] || wip_die 2 usage "roadmap amend: --from <file> is required in v1"
  [[ -f "$from" && -r "$from" ]] ||
    wip_die 2 not-found "roadmap amend: --from file not readable: $from"

  # Resolve initiative.
  local root mj init_record roadmap_path
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "roadmap amend: initiative not in manifest: $slug"
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"
  local roadmap_abs="$root/$roadmap_path"
  [[ -f "$roadmap_abs" ]] ||
    wip_die 4 no-roadmap "roadmap amend: missing roadmap.md: $roadmap_path" "$roadmap_path"

  # Refuse to amend on top of a malformed lane structure (ADR-0010 §5/§6).
  local existing_doc lane_err_count
  existing_doc="$(wip_roadmap_parse "$roadmap_abs")"
  lane_err_count="$(jq -r '.lane_errors | length' <<<"$existing_doc")"
  if [[ "$lane_err_count" != "0" ]]; then
    jq -nc --arg path "$roadmap_path" --argjson errors "$(jq -c '.lane_errors' <<<"$existing_doc")" '
      { ok: false, error: { code: 4, kind: "lane-malformed",
        message: "roadmap has a malformed lane structure; fix it before amending",
        path: $path, lane_errors: $errors } }'
    exit 4
  fi

  # Validate artifact shape (amendment kind).
  local vresult valid
  vresult="$(wip_intake_validate_kind "$from" amendment)"
  valid="$(jq -r '.valid' <<<"$vresult")"
  if [[ "$valid" != "true" ]]; then
    jq -nc --arg file "$from" --argjson r "$vresult" '
      { ok: false, file: $file, kind: "amendment", valid: false,
        missing: $r.missing, signals: $r.signals }'
    exit 4
  fi

  # Extract directive from front-matter; reconcile with CLI.
  local fm fm_kind fm_value
  fm="$(wip_intake_read_front_matter "$from")"
  local fm_dir
  fm_dir="$(wip_amend_extract_directive_from_fm "$fm")"
  if [[ -n "$fm_dir" ]]; then
    fm_kind="${fm_dir%%$'\t'*}"
    fm_value="${fm_dir#*$'\t'}"
  fi

  local kind value
  if [[ -n "$cli_kind" && -n "$fm_kind" ]]; then
    if [[ "$cli_kind" != "$fm_kind" || "$cli_value" != "$fm_value" ]]; then
      wip_die 2 directive-mismatch \
        "roadmap amend: CLI flag $cli_kind=$cli_value disagrees with artifact $fm_kind=$fm_value"
    fi
    kind="$cli_kind"
    value="$cli_value"
  elif [[ -n "$cli_kind" ]]; then
    kind="$cli_kind"
    value="$cli_value"
  elif [[ -n "$fm_kind" ]]; then
    kind="$fm_kind"
    value="$fm_value"
  else
    wip_die 2 no-directive "roadmap amend: no directive in artifact or CLI"
  fi

  # Render + apply per directive.
  local body payload hash marker rendered_step_id=""
  body="$(wip_amend_extract_body "$from")"

  case "$kind" in
    insert-after)
      payload="$(printf '%s\n' "$body" | wip_amend_render_step_bullet "")" ||
        wip_die 4 render-failed "roadmap amend: artifact body has no step heading" "$from"
      _wip_roadmap_amend_idempotent_or_apply \
        "insert-after" "$value" "$payload" "$roadmap_abs" "$roadmap_path" "$slug"
      ;;
    replace)
      # Read existing title from the roadmap so the artifact can keep it.
      local existing_title
      existing_title="$(_wip_roadmap_amend_step_title "$roadmap_abs" "$value")"
      payload="$(printf '%s\n' "$body" | wip_amend_render_step_bullet "$existing_title")" ||
        wip_die 4 render-failed "roadmap amend: artifact body has no step heading" "$from"
      _wip_roadmap_amend_idempotent_or_apply \
        "replace" "$value" "$payload" "$roadmap_abs" "$roadmap_path" "$slug"
      ;;
    append-round)
      payload="$(printf '%s\n' "$body" | wip_amend_render_round_block)"
      _wip_roadmap_amend_idempotent_or_apply \
        "append-round" "$value" "$payload" "$roadmap_abs" "$roadmap_path" "$slug"
      ;;
    append-lane)
      local fm_round target_round
      fm_round="$(_wip_intake_fm_str "$fm" target-round)"
      # The round is part of the shaped directive contract; refuse a CLI flag that
      # disagrees with the artifact (parity with the directive-mismatch check).
      if [[ -n "$cli_target_round" && -n "$fm_round" && "$cli_target_round" != "$fm_round" ]]; then
        wip_die 2 directive-mismatch \
          "roadmap amend: --target-round $cli_target_round disagrees with artifact target-round: $fm_round"
      fi
      target_round="${cli_target_round:-$fm_round}"
      [[ -n "$target_round" ]] ||
        wip_die 2 usage "roadmap amend: append-lane requires --target-round <N> (or target-round: in the artifact)"
      [[ "$target_round" =~ ^[0-9]+$ ]] ||
        wip_die 2 usage "roadmap amend: --target-round must be a round number, got: $target_round"
      payload="$(printf '%s\n' "$body" | wip_amend_render_lane_block "$value")" ||
        wip_die 4 render-failed "roadmap amend: append-lane body has no step heading" "$from"
      # Refuse a lane name already present in the target round — appending it would
      # leave the roadmap malformed (duplicate-lane) on the next parse. Skip this
      # when the exact same lane block is already stamped (an idempotent re-apply,
      # which the helper below handles as a no-op rather than a conflict).
      if ! wip_amend_has_marker "$roadmap_abs" "$(printf '%s' "$payload" | wip_amend_hash)" &&
        jq -e --argjson n "$target_round" --arg lane "$value" \
          '[.rounds[] | select(.n == $n) | .lanes[]] | index($lane) != null' \
          <<<"$existing_doc" >/dev/null 2>&1; then
        wip_die 4 duplicate-lane \
          "roadmap amend: lane '$value' already exists in round $target_round" "$roadmap_path"
      fi
      _wip_roadmap_amend_idempotent_or_apply \
        "append-lane" "$value" "$payload" "$roadmap_abs" "$roadmap_path" "$slug" "$target_round"
      ;;
    insert-step-in-lane)
      local fm_round target_round
      fm_round="$(_wip_intake_fm_str "$fm" target-round)"
      if [[ -n "$cli_target_round" && -n "$fm_round" && "$cli_target_round" != "$fm_round" ]]; then
        wip_die 2 directive-mismatch \
          "roadmap amend: --target-round $cli_target_round disagrees with artifact target-round: $fm_round"
      fi
      target_round="${cli_target_round:-$fm_round}"
      [[ -n "$target_round" ]] ||
        wip_die 2 usage "roadmap amend: insert-step-in-lane requires --target-round <N> (or target-round: in the artifact)"
      [[ "$target_round" =~ ^[0-9]+$ ]] ||
        wip_die 2 usage "roadmap amend: --target-round must be a round number, got: $target_round"
      # Renders a single step bullet (same body shape as insert-after); the apply
      # places it at the end of the named lane.
      payload="$(printf '%s\n' "$body" | wip_amend_render_step_bullet "")" ||
        wip_die 4 render-failed "roadmap amend: insert-step-in-lane body has no step heading" "$from"
      _wip_roadmap_amend_idempotent_or_apply \
        "insert-step-in-lane" "$value" "$payload" "$roadmap_abs" "$roadmap_path" "$slug" "$target_round"
      ;;
    *) wip_die 2 usage "roadmap amend: unknown directive: $kind" ;;
  esac
  : "$rendered_step_id"
}

# Parse a directive flag (--flag value | --flag=value) into kind+value.
# Refuses a second directive on the same command.
_wip_roadmap_amend_parse_dir() {
  local cur="$1" nxt="$2"
  # shellcheck disable=SC2178
  local -n _kind="$3"
  # shellcheck disable=SC2178
  local -n _value="$4"
  local flag="${cur%%=*}"
  local want="${flag#--}"
  [[ -z "$_kind" ]] ||
    wip_die 2 usage "roadmap amend: multiple directive flags (was $_kind, now $want)"
  if [[ "$cur" == *=* ]]; then
    _value="${cur#*=}"
  else
    [[ -n "$nxt" ]] || wip_die 2 usage "roadmap amend: $cur requires a value"
    _value="$nxt"
  fi
  _kind="$want"
}

# Echo the title of <step-id> from <roadmap-path>, empty if absent.
_wip_roadmap_amend_step_title() {
  local path="$1" sid="$2"
  local doc
  doc="$(wip_roadmap_parse "$path")"
  jq -r --arg s "$sid" '
    [.rounds[].steps[] | select(.id == $s) | .title] | (.[0] // "")
  ' <<<"$doc"
}

# Compute hash + marker; check idempotency; apply if not stamped.
# <extra> (7th arg) carries the target round for append-lane; unused otherwise.
_wip_roadmap_amend_idempotent_or_apply() {
  local kind="$1" value="$2" payload="$3"
  local roadmap_abs="$4" roadmap_path="$5" slug="$6" extra="${7:-}"
  local hash marker directive
  hash="$(printf '%s' "$payload" | wip_amend_hash)"
  marker="$(wip_amend_marker "$hash")"

  # Human-facing directive label; lane-targeting directives name the round.
  if [[ "$kind" == "append-lane" || "$kind" == "insert-step-in-lane" ]]; then
    directive="$kind $value (round $extra)"
  else
    directive="$kind $value"
  fi

  if wip_amend_has_marker "$roadmap_abs" "$hash"; then
    jq -nc --arg slug "$slug" --arg directive "$directive" --arg path "$roadmap_path" '
      { ok: true, slug: $slug, directive: $directive, wrote: [],
        idempotent_noop: true }'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    jq -nc --arg slug "$slug" --arg directive "$directive" --arg path "$roadmap_path" '
      { ok: true, slug: $slug, directive: $directive, wrote: [$path],
        idempotent_noop: false, dry_run: true }'
    return 0
  fi

  case "$kind" in
    insert-after)
      local ia_rc=0
      wip_amend_apply_insert_after "$roadmap_abs" "$value" "$payload" "$marker" || ia_rc=$?
      if [[ "$ia_rc" == "2" ]]; then
        wip_die 4 step-shadowed-in-comment "roadmap amend: target step only found inside a comment span: $value" "$roadmap_path"
      elif [[ "$ia_rc" != "0" ]]; then
        wip_die 4 step-not-in-roadmap "roadmap amend: target step not found: $value" "$roadmap_path"
      fi
      ;;
    replace)
      local repl_rc=0
      wip_amend_apply_replace "$roadmap_abs" "$value" "$payload" "$marker" || repl_rc=$?
      if [[ "$repl_rc" == "2" ]]; then
        wip_die 4 step-shadowed-in-comment "roadmap amend: target step only found inside a comment span: $value" "$roadmap_path"
      elif [[ "$repl_rc" != "0" ]]; then
        wip_die 4 step-not-in-roadmap "roadmap amend: target step not found: $value" "$roadmap_path"
      fi
      ;;
    append-round)
      wip_amend_apply_append_round "$roadmap_abs" "$payload" "$marker"
      ;;
    append-lane)
      wip_amend_apply_append_lane "$roadmap_abs" "$extra" "$payload" "$marker" ||
        wip_die 4 round-not-in-roadmap "roadmap amend: target round not found: $extra" "$roadmap_path"
      ;;
    insert-step-in-lane)
      local isil_rc=0
      wip_amend_apply_insert_step_in_lane "$roadmap_abs" "$extra" "$value" "$payload" "$marker" || isil_rc=$?
      if [[ "$isil_rc" == "2" ]]; then
        wip_die 4 round-not-in-roadmap "roadmap amend: target round not found: $extra" "$roadmap_path"
      elif [[ "$isil_rc" != "0" ]]; then
        wip_die 4 lane-not-in-round "roadmap amend: lane '$value' not found in round $extra" "$roadmap_path"
      fi
      ;;
  esac

  jq -nc --arg slug "$slug" --arg directive "$directive" --arg path "$roadmap_path" '
    { ok: true, slug: $slug, directive: $directive, wrote: [$path],
      idempotent_noop: false }'
}
