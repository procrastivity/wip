# wip-plumbing-intake-lib.bash — front-matter parser, classify, per-kind
# validators. Sourced by bin/wip-plumbing. Pure bash + jq + yq + awk.
# Implements engineering/specs/intake-kinds.md (ADR-0009).
# shellcheck shell=bash

WIP_INTAKE_KINDS="brief amendment workplan-seed spec handoff"

wip_intake_kind_valid() {
  local k="$1" x
  for x in $WIP_INTAKE_KINDS; do [[ "$x" == "$k" ]] && return 0; done
  return 1
}

# wip_intake_read_front_matter <file> — emit JSON of the front-matter map.
# Empty `{}` when there is no `---` head. Never errors on malformed YAML;
# returns `{}` in that case too (caller decides what's missing).
wip_intake_read_front_matter() {
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

# wip_intake_read_h1 <file> — first H1 title text, empty if none.
wip_intake_read_h1() {
  awk '/^# [^[:space:]]/ { sub(/^# +/, ""); print; exit }' "$1"
}

# Emit a JSON array of initiative slugs from the reachable manifest, or `[]`.
wip_intake_existing_slugs() {
  local root mj
  root="$(wip_find_root 2>/dev/null)" || {
    printf '[]'
    return 0
  }
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || {
    printf '[]'
    return 0
  }
  printf '%s' "$mj" | jq -c '[.initiatives[]?.slug // empty]'
}

# Emit a JSON array of step ids found in <slug>'s roadmap.md, or `[]`.
# Matches `step-N` and `step-N.M` tokens in `###` headings or `- ` bullets.
wip_intake_roadmap_steps() {
  local slug="$1" root rmap
  root="$(wip_find_root 2>/dev/null)" || {
    printf '[]'
    return 0
  }
  rmap="$root/.wip/initiatives/$slug/roadmap.md"
  [[ -f "$rmap" ]] || {
    printf '[]'
    return 0
  }
  awk '
    /^(###|[[:space:]]*-)/ {
      s = $0
      while (match(s, /step-[0-9]+(\.[0-9]+)?/)) {
        print substr(s, RSTART, RLENGTH)
        s = substr(s, RSTART + RLENGTH)
      }
    }
  ' "$rmap" | jq -R . | jq -sc 'unique'
}

# Helper: get a string field from a parsed front-matter JSON; empty if absent.
_wip_intake_fm_str() {
  local fm="$1" key="$2"
  printf '%s' "$fm" | jq -r --arg k "$key" '.[$k] // empty' 2>/dev/null
}

# Emit "<count>\n<name>\n" — count of directives present and the last one's
# name (empty when count is 0). Two lines so callers can parse via `read`.
# Ranges over the four amendment directives (append-lane added in ADR-0010).
_wip_intake_amendment_directive() {
  local fm="$1" name="" count=0 d v
  for d in insert-after replace append-round append-lane; do
    v="$(_wip_intake_fm_str "$fm" "$d")"
    if [[ -n "$v" ]]; then
      name="$d"
      count=$((count + 1))
    fi
  done
  printf '%s\n%s\n' "$count" "$name"
}

# wip_intake_classify <file> — emit {kind, confidence, signals[]} per
# intake-kinds.md §4. Caller has already confirmed the file is readable.
# Exits non-zero only when file is unparseable / has no H1 (handled by caller).
wip_intake_classify_payload() {
  local file="$1"
  local h1 fm
  h1="$(wip_intake_read_h1 "$file")"
  [[ -n "$h1" ]] || return 4
  fm="$(wip_intake_read_front_matter "$file")"

  local signals="[]" kind="" confidence=""

  # 1. front-matter wip-kind, authoritative.
  local wk
  wk="$(_wip_intake_fm_str "$fm" "wip-kind")"
  if [[ -n "$wk" ]] && wip_intake_kind_valid "$wk"; then
    kind="$wk"
    confidence="high"
    signals="$(jq -nc --argjson a "$signals" --arg s "front-matter wip-kind=$wk" '$a + [$s]')"
  fi

  local tgt
  tgt="$(_wip_intake_fm_str "$fm" "target")"
  local dir_name="" dir_count=0
  {
    read -r dir_count
    read -r dir_name || true
  } < <(_wip_intake_amendment_directive "$fm")

  # Manifest lookup (best-effort).
  local slugs="[]" tgt_slug="" tgt_step="" slug_known="false" step_known="false"
  slugs="$(wip_intake_existing_slugs)"
  if [[ -z "$slugs" || "$slugs" == "null" ]]; then slugs="[]"; fi
  local manifest_reachable="false"
  if [[ "$(jq -r 'length' <<<"$slugs")" != "0" ]]; then manifest_reachable="true"; fi

  if [[ -n "$tgt" ]]; then
    if [[ "$tgt" == */* ]]; then
      tgt_slug="${tgt%%/*}"
      tgt_step="${tgt#*/}"
    else
      tgt_slug="$tgt"
    fi
    if [[ "$manifest_reachable" == "true" ]]; then
      if jq -e --arg s "$tgt_slug" 'index($s) != null' <<<"$slugs" >/dev/null 2>&1; then
        slug_known="true"
        if [[ -n "$tgt_step" ]]; then
          local steps
          steps="$(wip_intake_roadmap_steps "$tgt_slug")"
          if jq -e --arg s "$tgt_step" 'index($s) != null' <<<"$steps" >/dev/null 2>&1; then
            step_known="true"
          fi
        fi
      fi
    else
      signals="$(jq -nc --argjson a "$signals" '$a + ["no-manifest"]')"
    fi
  fi

  # 2. target + directive -> amendment high.
  if [[ -z "$kind" && -n "$tgt" && -n "$dir_name" ]]; then
    kind="amendment"
    confidence="high"
    signals="$(jq -nc --argjson a "$signals" --arg t "target=$tgt" --arg d "$dir_name" \
      '$a + [$t, $d]')"
  fi

  # 3. target matching <slug>/<step> with existing step -> workplan-seed high.
  if [[ -z "$kind" && -n "$tgt_step" && "$step_known" == "true" ]]; then
    kind="workplan-seed"
    confidence="high"
    signals="$(jq -nc --argjson a "$signals" --arg t "target=$tgt" \
      '$a + [$t, "step-in-roadmap"]')"
  fi

  # 4. target matching existing slug (no directive) -> amendment medium.
  if [[ -z "$kind" && -n "$tgt_slug" && -z "$tgt_step" && "$slug_known" == "true" ]]; then
    kind="amendment"
    confidence="medium"
    signals="$(jq -nc --argjson a "$signals" --arg t "target=$tgt" \
      '$a + [$t, "slug-known"]')"
  fi

  # If target is present but slug unknown (with manifest reachable), downgrade
  # to handoff low + signal.
  if [[ -z "$kind" && -n "$tgt" && "$manifest_reachable" == "true" && "$slug_known" == "false" ]]; then
    kind="handoff"
    confidence="low"
    signals="$(jq -nc --argjson a "$signals" --arg t "target=$tgt" \
      '$a + [$t, "unknown-target"]')"
  fi

  # Body-heading heuristics.
  local has_step_heading has_user_stories has_requirements has_goal_or_summary
  has_step_heading=$(awk '/^### step-[0-9]+/ { print "1"; exit }' "$file")
  has_user_stories=$(awk '/^## User stories([[:space:]]|$)/ { print "1"; exit }' "$file")
  has_requirements=$(awk '/^## Requirements([[:space:]]|$)/ { print "1"; exit }' "$file")
  has_goal_or_summary=$(awk '/^## (Goal|Summary)([[:space:]]|$)/ { print "1"; exit }' "$file")

  # 5. ### step-NN heading in body + no target -> amendment likely / handoff low.
  if [[ -z "$kind" && "$has_step_heading" == "1" && -z "$tgt" ]]; then
    kind="amendment"
    confidence="low"
    signals="$(jq -nc --argjson a "$signals" '$a + ["body has step heading"]')"
  fi

  # 6. spec heuristic.
  if [[ -z "$kind" && ("$has_user_stories" == "1" || "$has_requirements" == "1") ]]; then
    kind="spec"
    confidence="medium"
    signals="$(jq -nc --argjson a "$signals" '$a + ["spec body sections"]')"
  fi

  # 7. brief heuristic.
  if [[ -z "$kind" && "$has_goal_or_summary" == "1" && -z "$tgt" ]]; then
    kind="brief"
    confidence="medium"
    signals="$(jq -nc --argjson a "$signals" '$a + ["title + goal-or-summary"]')"
  fi

  # 8. fallback: parseable + titled, none of above -> handoff low.
  if [[ -z "$kind" ]]; then
    kind="handoff"
    confidence="low"
  fi

  jq -nc \
    --arg kind "$kind" --arg confidence "$confidence" --argjson signals "$signals" '
    { kind: $kind, confidence: $confidence, signals: $signals }'
}

# wip_intake_validate_kind <file> <kind> — emit {valid, missing[], signals[]}.
# Pure JSON; never errors. Caller has confirmed kind is in vocabulary.
wip_intake_validate_kind() {
  local file="$1" kind="$2"
  case "$kind" in
    brief) _wip_intake_validate_brief "$file" ;;
    amendment) _wip_intake_validate_amendment "$file" ;;
    workplan-seed) _wip_intake_validate_workplan_seed "$file" ;;
    spec) _wip_intake_validate_spec "$file" ;;
    handoff) _wip_intake_validate_handoff "$file" ;;
    *) jq -nc '{valid:false, missing:["unknown-kind"], signals:[]}' ;;
  esac
}

_wip_intake_empty_arr() { printf '[]'; }

_wip_intake_validate_brief() {
  local file="$1"
  local h1 fm tgt missing="[]" signals="[]"
  h1="$(wip_intake_read_h1 "$file")"
  fm="$(wip_intake_read_front_matter "$file")"
  tgt="$(_wip_intake_fm_str "$fm" "target")"
  [[ -n "$h1" ]] || missing="$(jq -nc --argjson a "$missing" '$a + ["title"]')"
  if ! awk '/^## (Goal|Summary)([[:space:]]|$)/ { found=1 } END { exit !found }' "$file"; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["goal-or-summary-section"]')"
  fi
  if [[ -n "$tgt" ]]; then
    local slugs
    slugs="$(wip_intake_existing_slugs)"
    if jq -e --arg s "$tgt" 'index($s) != null' <<<"$slugs" >/dev/null 2>&1; then
      missing="$(jq -nc --argjson a "$missing" '$a + ["use-amendment"]')"
    fi
  fi
  _wip_intake_emit_result "$missing" "$signals"
}

_wip_intake_validate_amendment() {
  local file="$1"
  local fm tgt dir_name="" dir_count=0 missing="[]" signals="[]"
  fm="$(wip_intake_read_front_matter "$file")"
  tgt="$(_wip_intake_fm_str "$fm" "target")"
  {
    read -r dir_count
    read -r dir_name || true
  } < <(_wip_intake_amendment_directive "$fm")

  [[ -n "$tgt" ]] || missing="$(jq -nc --argjson a "$missing" '$a + ["target"]')"
  if [[ "$dir_count" == "0" ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["directive"]')"
  elif [[ "$dir_count" -gt 1 ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["multiple-directives"]')"
  fi

  if [[ -n "$tgt" ]]; then
    local slugs
    slugs="$(wip_intake_existing_slugs)"
    if [[ "$(jq -r 'length' <<<"$slugs")" != "0" ]]; then
      if ! jq -e --arg s "$tgt" 'index($s) != null' <<<"$slugs" >/dev/null 2>&1; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["unknown-target"]')"
      fi
    else
      signals="$(jq -nc --argjson a "$signals" '$a + ["no-manifest"]')"
    fi
  fi

  case "$dir_name" in
    insert-after | replace)
      if ! awk '/^### step-[0-9]+/ { found=1 } END { exit !found }' "$file"; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["new-step-heading"]')"
      fi
      ;;
    append-round)
      if ! awk '/^## Round [0-9]+ — / { found=1 } END { exit !found }' "$file"; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["round-heading"]')"
      fi
      # Steps may be `### step-NN` headings (amendment form) or `- **step-NN —`
      # bullets (canonical roadmap form, used when the round carries `### Lane`
      # subheadings per ADR-0010). Either satisfies the step requirement.
      if ! awk '/^### step-[0-9]/ || /^- \*\*step-[0-9]/ { found=1 } END { exit !found }' "$file"; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["step-headings"]')"
      fi
      ;;
    append-lane)
      # A new lane in an existing round (ADR-0010): needs target-round + step
      # headings, and must NOT carry a ## Round heading (that would be append-round).
      if [[ -z "$(_wip_intake_fm_str "$fm" "target-round")" ]]; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["target-round"]')"
      fi
      if ! awk '/^### step-[0-9]+/ { found=1 } END { exit !found }' "$file"; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["step-headings"]')"
      fi
      if awk '/^## Round [0-9]+ — / { found=1 } END { exit !found }' "$file"; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["unexpected-round-heading"]')"
      fi
      ;;
  esac

  _wip_intake_emit_result "$missing" "$signals"
}

_wip_intake_validate_workplan_seed() {
  local file="$1"
  local fm tgt slug step missing="[]" signals="[]"
  fm="$(wip_intake_read_front_matter "$file")"
  tgt="$(_wip_intake_fm_str "$fm" "target")"
  if [[ -z "$tgt" ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["target"]')"
  elif [[ "$tgt" != */* ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["target-not-slug-step"]')"
  else
    slug="${tgt%%/*}"
    step="${tgt#*/}"
    local slugs
    slugs="$(wip_intake_existing_slugs)"
    if [[ "$(jq -r 'length' <<<"$slugs")" == "0" ]]; then
      signals="$(jq -nc --argjson a "$signals" '$a + ["no-manifest"]')"
    elif ! jq -e --arg s "$slug" 'index($s) != null' <<<"$slugs" >/dev/null 2>&1; then
      missing="$(jq -nc --argjson a "$missing" '$a + ["unknown-target-slug"]')"
    else
      local steps
      steps="$(wip_intake_roadmap_steps "$slug")"
      if [[ "$(jq -r 'length' <<<"$steps")" == "0" ]]; then
        signals="$(jq -nc --argjson a "$signals" '$a + ["no-roadmap"]')"
      elif ! jq -e --arg s "$step" 'index($s) != null' <<<"$steps" >/dev/null 2>&1; then
        missing="$(jq -nc --argjson a "$missing" '$a + ["step-not-in-roadmap"]')"
      fi
    fi
  fi
  _wip_intake_emit_result "$missing" "$signals"
}

_wip_intake_validate_spec() {
  local file="$1"
  local missing="[]" signals="[]"
  if ! awk '/^## Summary([[:space:]]|$)/ { found=1 } END { exit !found }' "$file"; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["summary-section"]')"
  fi
  if ! awk '/^## (User stories|Requirements)([[:space:]]|$)/ { found=1 } END { exit !found }' "$file"; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["user-stories-or-requirements"]')"
  fi
  signals="$(jq -nc --argjson a "$signals" '$a + ["lds-delegate-deferred"]')"
  _wip_intake_emit_result "$missing" "$signals"
}

_wip_intake_validate_handoff() {
  local file="$1"
  local h1 missing="[]" signals="[]"
  h1="$(wip_intake_read_h1 "$file")"
  [[ -n "$h1" ]] || missing="$(jq -nc --argjson a "$missing" '$a + ["title"]')"
  _wip_intake_emit_result "$missing" "$signals"
}

_wip_intake_emit_result() {
  local missing="$1" signals="$2"
  jq -nc --argjson missing "$missing" --argjson signals "$signals" '
    { valid: ($missing | length == 0), missing: $missing, signals: $signals }'
}

# wip_intake_derive_slug <file> — front-matter slug:, else slugify H1.
# Emits the slug to stdout (empty if neither yields one).
wip_intake_derive_slug() {
  local file="$1" fm slug h1
  fm="$(wip_intake_read_front_matter "$file")"
  slug="$(_wip_intake_fm_str "$fm" "slug")"
  if [[ -n "$slug" ]]; then
    printf '%s' "$slug"
    return 0
  fi
  h1="$(wip_intake_read_h1 "$file")"
  [[ -n "$h1" ]] || return 0
  printf '%s' "$h1" | tr '[:upper:]' '[:lower:]' |
    sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//'
}
