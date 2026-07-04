# init — scaffold the repo manifest and/or an initiative from templates/.
# Idempotent; protected-path model (never clobbers existing content in v0).
# shellcheck shell=bash

wip_plumbing_cmd_init() {
  local slug="" title="" intake="ad-hoc" brief_body="" tracker_anchor=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --tracker-anchor)
        [[ $# -ge 2 ]] || wip_die 2 usage "init: --tracker-anchor requires an argument"
        tracker_anchor="$2"
        shift 2
        ;;
      --tracker-anchor=*)
        tracker_anchor="${1#--tracker-anchor=}"
        shift
        ;;
      --title)
        [[ $# -ge 2 ]] || wip_die 2 usage "init: --title requires an argument"
        title="$2"
        shift 2
        ;;
      --title=*)
        title="${1#--title=}"
        shift
        ;;
      --intake)
        [[ $# -ge 2 ]] || wip_die 2 usage "init: --intake requires an argument"
        intake="$2"
        shift 2
        ;;
      --intake=*)
        intake="${1#--intake=}"
        shift
        ;;
      --brief-body)
        [[ $# -ge 2 ]] || wip_die 2 usage "init: --brief-body requires an argument"
        brief_body="$2"
        shift 2
        ;;
      --brief-body=*)
        brief_body="${1#--brief-body=}"
        shift
        ;;
      -*) wip_die 2 usage "init: unknown flag: $1" ;;
      *)
        if [[ -z "$slug" ]]; then
          slug="$1"
          shift
        else
          wip_die 2 usage "init: unexpected arg: $1"
        fi
        ;;
    esac
  done

  case "$intake" in
    ad-hoc | structured) ;;
    *) wip_die 2 usage "init: --intake must be ad-hoc or structured" ;;
  esac

  # A tracker anchor is the durable initiative→source-issue link (ADR-0024 / D6).
  # Validate its lexical shape up front (single gate; intake-apply forwards here),
  # reusing the canonical tracker-id shape from _wip_tracker_id_valid.
  if [[ -n "$tracker_anchor" ]] && ! _wip_tracker_id_valid "$tracker_anchor"; then
    wip_die 2 usage "init: --tracker-anchor must be a tracker id (e.g. BDS-56): $tracker_anchor"
  fi

  local templates_dir
  templates_dir="$(_wip_init_templates_dir)" ||
    wip_die 1 internal "init: templates/ directory not found"

  if [[ -z "$slug" ]]; then
    [[ -z "$brief_body" ]] ||
      wip_die 2 usage "init: --brief-body requires a <slug> (initiative-level)"
    [[ -z "$tracker_anchor" ]] ||
      wip_die 2 usage "init: --tracker-anchor requires a <slug> (initiative-level)"
    _wip_init_repo "$templates_dir"
  else
    _wip_init_validate_slug "$slug"
    if [[ -n "$brief_body" ]]; then
      [[ -f "$brief_body" && -r "$brief_body" ]] ||
        wip_die 2 not-found "init: --brief-body file not readable: $brief_body"
    fi
    _wip_init_initiative "$templates_dir" "$slug" "$title" "$intake" "$brief_body" "$tracker_anchor"
  fi
}

# Echo the absolute path to templates/. Looks relative to $WIP_LIB first
# (lib/wip → ../../templates), then walks up from there for safety.
_wip_init_templates_dir() {
  local cand
  # shellcheck disable=SC1007  # CDPATH= prefixes cd (neutralize CDPATH), not an assignment.
  cand="$(CDPATH= cd -- "$WIP_LIB/../../templates" 2>/dev/null && pwd)" || cand=""
  [[ -d "$cand" ]] || return 1
  printf '%s\n' "$cand"
}

_wip_init_validate_slug() {
  local s="$1"
  [[ "$s" =~ ^[a-z0-9][a-z0-9-]*$ ]] ||
    wip_die 2 bad-slug "init: slug must match ^[a-z0-9][a-z0-9-]*$: $s"
}

# Humanize a slug: foo-bar -> Foo Bar.
_wip_init_humanize() {
  local s="$1"
  printf '%s\n' "$s" | awk '{
    n = split($0, parts, "-")
    out = ""
    for (i = 1; i <= n; i++) {
      w = parts[i]
      if (length(w) > 0) {
        w = toupper(substr(w,1,1)) substr(w,2)
      }
      out = (i == 1 ? w : out " " w)
    }
    print out
  }'
}

# Repo-level scaffold: write .wip.yaml + .wip/GLOSSARY.md + .wip/backlog.md if
# absent. Target dir is $WIP_ROOT when set, else $PWD.
_wip_init_repo() {
  local templates_dir="$1"
  local target
  target="${WIP_ROOT:-$PWD}"

  local wrote=() skipped=()
  _wip_init_scaffold_repo_files "$templates_dir" "$target" wrote skipped

  local manifest_updated=""
  local p
  for p in "${wrote[@]+"${wrote[@]}"}"; do
    if [[ "$p" == "$target/.wip.yaml" ]]; then
      manifest_updated=".wip.yaml"
      break
    fi
  done

  jq -nc \
    --arg manifest "$manifest_updated" \
    --argjson wrote "$(_wip_init_relpaths "$target" "${wrote[@]+"${wrote[@]}"}")" \
    --argjson skipped "$(_wip_init_relpaths "$target" "${skipped[@]+"${skipped[@]}"}")" '
    {
      ok: true,
      slug: null,
      wrote: $wrote,
      skipped_protected: $skipped,
      manifest_updated: (if $manifest == "" then null else $manifest end)
    }'
}

_wip_init_scaffold_repo_files() {
  local templates_dir="$1" target="$2" wrote_name="$3" skipped_name="$4"
  local date
  date="$(wip_scaffold_now)"

  _wip_init_try_write "$templates_dir/wip.yaml.tmpl" "$target/.wip.yaml" \
    "$wrote_name" "$skipped_name" "date=$date"

  local glossary_content backlog_content
  glossary_content="$(
    cat <<'EOF'
# .wip/GLOSSARY.md
#
# Placeholder. The `wip glossary` assembler (roadmap step-13) will replace
# this with the concatenation of `templates/glossary/core.md` plus the
# per-feature partials for every enabled feature in `.wip.yaml`.
EOF
  )"
  _wip_init_try_write_content "$target/.wip/GLOSSARY.md" "$glossary_content" \
    "$wrote_name" "$skipped_name"

  backlog_content="$(
    cat <<'EOF'
# Backlog — cross-cutting

_Unattached ideas / deferrals that don't yet belong to an initiative._
EOF
  )"
  _wip_init_try_write_content "$target/.wip/backlog.md" "$backlog_content" \
    "$wrote_name" "$skipped_name"
}

# Render <tmpl> -> <dest>, append outcome to the named wrote/skipped arrays.
# Remaining args are key=val pairs for wip_scaffold_render.
_wip_init_try_write() {
  local tmpl="$1" dest="$2" wrote_name="$3" skipped_name="$4"
  shift 4
  # shellcheck disable=SC2178  # nameref binds to an array in the caller.
  local -n _wr="$wrote_name"
  # shellcheck disable=SC2178
  local -n _sk="$skipped_name"
  local rc
  set +e
  wip_scaffold_render_to "$tmpl" "$dest" "$@"
  rc=$?
  set -e
  case "$rc" in
    0) _wr+=("$dest") ;;
    1) _sk+=("$dest") ;;
    *) wip_die 1 internal "init: scaffold write failed: $dest" ;;
  esac
}

# Compose a BRIEF.md by splicing a shaped brief body beneath the template's
# standard header. The header (decorated H1 + durable-context blockquote +
# Slug/Started lines) is the rendered template up to — but not including — its
# first `## ` section; the body is the shaped file from its first `## ` section
# to end (dropping the shaped H1/front-matter, since the template owns the H1).
# Echoes the composed content. Falls back to the rendered template if the shaped
# file carries no `## ` section (shouldn't happen — apply validates first).
_wip_init_compose_brief() {
  local tmpl="$1" body_file="$2" slug="$3" title="$4" date="$5"
  local rendered header body
  rendered="$(wip_scaffold_render "$tmpl" "slug=$slug" "title=$title" "date=$date")" ||
    return 1
  body="$(sed -n '/^## /,$p' "$body_file")"
  if [[ -z "$body" ]]; then
    printf '%s' "$rendered"
    return 0
  fi
  header="$(printf '%s\n' "$rendered" | sed '/^## /,$d')"
  printf '%s\n\n%s' "$header" "$body"
}

# Write literal <content> -> <dest>, append outcome to the named arrays.
_wip_init_try_write_content() {
  local dest="$1" content="$2" wrote_name="$3" skipped_name="$4"
  # shellcheck disable=SC2178  # nameref binds to an array in the caller.
  local -n _wr="$wrote_name"
  # shellcheck disable=SC2178
  local -n _sk="$skipped_name"
  local rc
  set +e
  wip_scaffold_write_or_skip "$dest" "$content"
  rc=$?
  set -e
  case "$rc" in
    0) _wr+=("$dest") ;;
    1) _sk+=("$dest") ;;
    *) wip_die 1 internal "init: scaffold write failed: $dest" ;;
  esac
}

# Convert absolute paths to repo-relative; emit a JSON array literal.
_wip_init_relpaths() {
  local root="$1"
  shift
  local arr="[]" p rel
  for p in "$@"; do
    rel="${p#"$root"/}"
    arr="$(jq -nc --argjson a "$arr" --arg p "$rel" '$a + [$p]')"
  done
  printf '%s' "$arr"
}

# Initiative-level scaffold: ensure manifest, then scaffold the initiative
# directory and append a registry entry.
_wip_init_initiative() {
  local templates_dir="$1" slug="$2" title="$3" intake="$4" brief_body="${5:-}" tracker_anchor="${6:-}"
  [[ -n "$title" ]] || title="$(_wip_init_humanize "$slug")"

  local target
  target="${WIP_ROOT:-$PWD}"

  local wrote=() skipped=() manifest_updated=""

  if [[ ! -f "$target/.wip.yaml" ]]; then
    _wip_init_scaffold_repo_files "$templates_dir" "$target" wrote skipped
    manifest_updated=".wip.yaml"
  fi

  local init_dir="$target/.wip/initiatives/$slug"
  if [[ -e "$init_dir" ]]; then
    wip_die 4 slug-exists "init: initiative already exists: $slug" \
      ".wip/initiatives/$slug"
  fi

  local date
  date="$(wip_scaffold_now)"

  if [[ -n "$brief_body" ]]; then
    # Persist the shaped brief body: standard template header (decorated H1 +
    # durable-context blockquote + Slug/Started) spliced above the shaped
    # sections, so intake captures the plan instead of an empty skeleton.
    local brief_content
    brief_content="$(_wip_init_compose_brief \
      "$templates_dir/brief.md.tmpl" "$brief_body" "$slug" "$title" "$date")" ||
      wip_die 1 internal "init: failed to compose brief from $brief_body"
    _wip_init_try_write_content "$init_dir/BRIEF.md" "$brief_content" wrote skipped
  else
    _wip_init_try_write "$templates_dir/brief.md.tmpl" "$init_dir/BRIEF.md" \
      wrote skipped "slug=$slug" "title=$title" "date=$date"
  fi
  _wip_init_try_write "$templates_dir/roadmap.md.tmpl" "$init_dir/roadmap.md" \
    wrote skipped "slug=$slug" "title=$title" "date=$date"

  if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
    _wip_init_append_initiative "$target/.wip.yaml" "$slug" "$title" "$intake" "$tracker_anchor" ||
      wip_die 1 internal "init: failed to update manifest"
    manifest_updated=".wip.yaml"
  else
    [[ -z "$manifest_updated" ]] && manifest_updated=".wip.yaml"
  fi

  jq -nc \
    --arg slug "$slug" \
    --argjson wrote "$(_wip_init_relpaths "$target" "${wrote[@]+"${wrote[@]}"}")" \
    --argjson skipped "$(_wip_init_relpaths "$target" "${skipped[@]+"${skipped[@]}"}")" \
    --arg manifest "$manifest_updated" '
    {
      ok: true,
      slug: $slug,
      wrote: $wrote,
      skipped_protected: $skipped,
      manifest_updated: (if $manifest == "" then null else $manifest end)
    }'
}

_wip_init_append_initiative() {
  local manifest="$1" slug="$2" title="$3" intake="$4" tracker_anchor="${5:-}"
  local brief=".wip/initiatives/$slug/BRIEF.md"
  local roadmap=".wip/initiatives/$slug/roadmap.md"

  local pre_count pre_current
  pre_count="$(yq -r '(.initiatives // []) | length' "$manifest" 2>/dev/null)"
  pre_current="$(yq -r '.current_initiative // ""' "$manifest" 2>/dev/null)"
  [[ "$pre_current" == "null" ]] && pre_current=""

  SLUG="$slug" TITLE="$title" INTAKE="$intake" BRIEF="$brief" ROADMAP="$roadmap" \
    yq -i '
      .initiatives = ((.initiatives // []) + [{
        "slug": strenv(SLUG),
        "title": strenv(TITLE),
        "status": "in-flight",
        "intake": strenv(INTAKE),
        "brief": strenv(BRIEF),
        "roadmap": strenv(ROADMAP)
      }])
    ' "$manifest" || return 1

  # The record carries a top-level `tracker_anchor` ONLY when captured at intake
  # (ADR-0024 / D3): it is intake-anchored, NOT roadmap-authored, so it is a
  # sibling of `tracker_map` (never inside it — preserving ADR-0019 §C's "roadmap
  # is SoT for tracker_map"). Written as a separate conditional set on the record
  # just appended (`.initiatives[-1]`) so the field is simply ABSENT when no anchor
  # is supplied (back-compat) — no empty-string key on legacy inits.
  if [[ -n "$tracker_anchor" ]]; then
    ANCHOR="$tracker_anchor" \
      yq -i '.initiatives[-1].tracker_anchor = strenv(ANCHOR)' "$manifest" || return 1
  fi

  if [[ "$pre_count" == "0" && -z "$pre_current" ]]; then
    SLUG="$slug" yq -i '.current_initiative = strenv(SLUG)' "$manifest" || return 1
  fi
  return 0
}
