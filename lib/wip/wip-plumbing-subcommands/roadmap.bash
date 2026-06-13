# roadmap — deterministic edits to an initiative's roadmap.md. v1 ships
# only `roadmap amend <slug> --from <file>`. Reuses intake amendment shape
# rules; idempotent via SHA-256-of-rendered-payload markers.
# shellcheck shell=bash

wip_plumbing_cmd_roadmap() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    amend) _wip_roadmap_cmd_amend "$@" ;;
    "") wip_die 2 usage "roadmap: missing subcommand (amend)" ;;
    *) wip_die 2 usage "roadmap: unknown subcommand: $sub" ;;
  esac
}

_wip_roadmap_cmd_amend() {
  local slug="" from="" cli_kind="" cli_value=""
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
      --insert-after | --insert-after=* | --replace | --replace=* | --append-round | --append-round=*)
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
_wip_roadmap_amend_idempotent_or_apply() {
  local kind="$1" value="$2" payload="$3"
  local roadmap_abs="$4" roadmap_path="$5" slug="$6"
  local hash marker
  hash="$(printf '%s' "$payload" | wip_amend_hash)"
  marker="$(wip_amend_marker "$hash")"

  if wip_amend_has_marker "$roadmap_abs" "$hash"; then
    jq -nc --arg slug "$slug" --arg directive "$kind $value" --arg path "$roadmap_path" '
      { ok: true, slug: $slug, directive: $directive, wrote: [],
        idempotent_noop: true }'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    jq -nc --arg slug "$slug" --arg directive "$kind $value" --arg path "$roadmap_path" '
      { ok: true, slug: $slug, directive: $directive, wrote: [$path],
        idempotent_noop: false, dry_run: true }'
    return 0
  fi

  case "$kind" in
    insert-after)
      wip_amend_apply_insert_after "$roadmap_abs" "$value" "$payload" "$marker" ||
        wip_die 4 step-not-in-roadmap "roadmap amend: target step not found: $value" "$roadmap_path"
      ;;
    replace)
      wip_amend_apply_replace "$roadmap_abs" "$value" "$payload" "$marker" ||
        wip_die 4 step-not-in-roadmap "roadmap amend: target step not found: $value" "$roadmap_path"
      ;;
    append-round)
      wip_amend_apply_append_round "$roadmap_abs" "$payload" "$marker"
      ;;
  esac

  jq -nc --arg slug "$slug" --arg directive "$kind $value" --arg path "$roadmap_path" '
    { ok: true, slug: $slug, directive: $directive, wrote: [$path],
      idempotent_noop: false }'
}
