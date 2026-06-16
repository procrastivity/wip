# workplan — scaffold an initiative step's workplan from
# templates/workplan.md.tmpl. v1 ships only `workplan init`.
# shellcheck shell=bash

wip_plumbing_cmd_workplan() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    init) _wip_workplan_cmd_init "$@" ;;
    "") wip_die 2 usage "workplan: missing subcommand (init)" ;;
    *) wip_die 2 usage "workplan: unknown subcommand: $sub" ;;
  esac
}

_wip_workplan_cmd_init() {
  local slug="" step_id="" from="" slug_override="" force=0 activate=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --from)
        [[ $# -ge 2 ]] || wip_die 2 usage "workplan init: --from requires an argument"
        from="$2"
        shift 2
        ;;
      --from=*)
        from="${1#--from=}"
        shift
        ;;
      --slug)
        [[ $# -ge 2 ]] || wip_die 2 usage "workplan init: --slug requires an argument"
        slug_override="$2"
        shift 2
        ;;
      --slug=*)
        slug_override="${1#--slug=}"
        shift
        ;;
      --force)
        force=1
        shift
        ;;
      --activate)
        activate=1
        shift
        ;;
      -*) wip_die 2 usage "workplan init: unknown flag: $1" ;;
      *)
        if [[ -z "$slug" ]]; then
          slug="$1"
        elif [[ -z "$step_id" ]]; then
          step_id="$1"
        else
          wip_die 2 usage "workplan init: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done

  [[ -n "$slug" ]] || wip_die 2 usage "workplan init: missing <slug>"
  [[ -n "$step_id" ]] || wip_die 2 usage "workplan init: missing <step-id>"

  # Resolve initiative + roadmap.
  local root mj init_record roadmap_path
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "workplan init: initiative not in manifest: $slug"
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  # Verify step exists; pull title for slug derivation + template.
  local doc step_record step_title
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"
  step_record="$(wip_roadmap_step "$doc" "$step_id")"
  if [[ -z "$step_record" || "$step_record" == "null" ]]; then
    wip_die 4 step-not-in-roadmap "workplan init: step not in roadmap: $step_id" "$roadmap_path"
  fi
  step_title="$(jq -r '.title' <<<"$step_record")"

  # Derive workplan slug.
  local derived="$slug_override"
  if [[ -z "$derived" ]]; then
    derived="$(printf '%s' "$step_title" | tr '[:upper:]' '[:lower:]' |
      sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//')"
  fi
  [[ -n "$derived" ]] || wip_die 4 bad-slug "workplan init: could not derive workplan slug"

  # Optional seed validation.
  if [[ -n "$from" ]]; then
    [[ -f "$from" && -r "$from" ]] ||
      wip_die 2 not-found "workplan init: --from file not readable: $from"
    local vresult valid
    vresult="$(wip_intake_validate_kind "$from" workplan-seed)"
    valid="$(jq -r '.valid' <<<"$vresult")"
    if [[ "$valid" != "true" ]]; then
      jq -nc --arg file "$from" --argjson r "$vresult" '
        { ok: false, file: $file, kind: "workplan-seed", valid: false,
          missing: $r.missing, signals: $r.signals }'
      exit 4
    fi
  fi

  # Build target path.
  local rel_path=".wip/initiatives/$slug/workplans/$step_id-$derived.md"
  local abs_path="$root/$rel_path"

  local file_exists=0
  [[ -e "$abs_path" ]] && file_exists=1

  # Pre-existing file gates on --force. WITHOUT --activate, an existing
  # workplan refuses (exit 4). WITH --activate, an existing workplan is NOT an
  # error: we skip the write but still set active_step, so "start" is
  # re-runnable for a step whose workplan already exists.
  if [[ "$file_exists" == "1" && "$force" != "1" && "$activate" != "1" ]]; then
    wip_die 4 file-exists "workplan init: workplan already exists: $rel_path" "$rel_path"
  fi

  # Decide whether the workplan file is (re)written. Existing + --activate (and
  # not --force) keeps the file untouched.
  local will_write=1
  if [[ "$file_exists" == "1" && "$force" != "1" ]]; then
    will_write=0
  fi

  # Render the workplan body up front (needed for both the dry-run ledger and
  # the real write).
  local content=""
  if [[ "$will_write" == "1" ]]; then
    local templates_dir date
    templates_dir="$(_wip_workplan_templates_dir)" ||
      wip_die 1 internal "workplan init: templates/ not found"
    date="$(wip_scaffold_now)"
    content="$(wip_scaffold_render "$templates_dir/workplan.md.tmpl" \
      "slug=$slug" "step_id=$step_id" "step_title=$step_title" "date=$date")" ||
      wip_die 1 internal "workplan init: render failed"

    # Append seed body (if present) under a "## Seed (from intake)" section.
    if [[ -n "$from" ]]; then
      local seed_body
      seed_body="$(wip_amend_extract_body "$from")"
      content="$content"$'\n## Seed (from intake)\n\n'"$seed_body"
    fi
  fi

  # Activation: set initiatives.<slug>.active_step in .wip.yaml — the same key
  # detect/status read. Honors $WIP_DRY_RUN (touches nothing under --dry-run).
  local manifest_updated=""
  if [[ "$activate" == "1" ]]; then
    local astatus
    astatus="$(_wip_workplan_set_active_step "$root/.wip.yaml" "$slug" "$step_id")" ||
      wip_die 1 internal "workplan init: could not set active_step in .wip.yaml"
    [[ "$astatus" == "updated" ]] && manifest_updated=".wip.yaml"
  fi

  # Write (overwrite when --force, else write_or_skip would refuse — but we
  # already gated that above). Skipped under --dry-run.
  if [[ "$will_write" == "1" && "${WIP_DRY_RUN:-0}" != "1" ]]; then
    if [[ "$force" == "1" && -e "$abs_path" ]]; then
      rm -f "$abs_path"
    fi
    wip_scaffold_write_or_skip "$abs_path" "$content" >/dev/null || {
      wip_die 1 internal "workplan init: write failed: $rel_path"
    }
  fi

  # Ledger. `wrote` lists the workplan when (re)written; `skipped` lists it when
  # an existing workplan is kept under --activate. `active_step` is present only
  # with --activate.
  local wrote="[]" skipped="[]"
  if [[ "$will_write" == "1" ]]; then
    wrote="$(jq -nc --arg p "$rel_path" '[$p]')"
  else
    skipped="$(jq -nc --arg p "$rel_path" '[$p]')"
  fi
  jq -nc \
    --arg slug "$slug" --arg step "$step_id" \
    --argjson wrote "$wrote" --argjson skipped "$skipped" \
    --arg activate "$activate" --arg mu "$manifest_updated" \
    --arg dry "${WIP_DRY_RUN:-0}" '
    { ok: true, slug: $slug, step: $step, wrote: $wrote }
    + (if ($skipped | length) > 0 then { skipped: $skipped } else {} end)
    + (if $activate == "1" then { active_step: $step } else {} end)
    + (if $mu != "" then { manifest_updated: $mu } else {} end)
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}

# _wip_workplan_set_active_step <manifest> <slug> <step-id>
#
# Idempotently set initiatives[slug].active_step = <step-id> via yq -i — the
# same key detect/status read. Mirrors the deterministic yq machinery in
# `_wip_init_append_initiative` / `wip_setup_set_feature_flag`. Honors
# $WIP_DRY_RUN (no write). Prints "updated" or "noop"; returns 1 on yq error.
_wip_workplan_set_active_step() {
  local manifest="$1" slug="$2" step="$3"
  [[ -f "$manifest" ]] || {
    printf 'wip-plumbing: workplan: manifest missing: %s\n' "$manifest" >&2
    return 1
  }
  local current
  current="$(SLUG="$slug" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""
  ' "$manifest" 2>/dev/null)" || current=""
  [[ "$current" == "null" ]] && current=""
  if [[ "$current" == "$step" ]]; then
    printf 'noop'
    return 0
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi
  SLUG="$slug" STEP="$step" yq -i '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) = strenv(STEP)
  ' "$manifest" || return 1
  printf 'updated'
  return 0
}

# Echo the absolute templates/ path; same lookup init.bash uses.
_wip_workplan_templates_dir() {
  local cand
  # shellcheck disable=SC1007  # CDPATH= prefixes cd (neutralize CDPATH).
  cand="$(CDPATH= cd -- "$WIP_LIB/../../templates" 2>/dev/null && pwd)" || cand=""
  [[ -d "$cand" ]] || return 1
  printf '%s\n' "$cand"
}
