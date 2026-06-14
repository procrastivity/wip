# template — show / list canonical templates that ship with wip.
#
# v1 surface: prompt templates under templates/prompts/. The id grammar is the
# path under templates/prompts/ minus the .md extension — e.g.
#   id "intake/preamble"  →  templates/prompts/intake/preamble.md
#   id "intake/brief"     →  templates/prompts/intake/brief.md
#
# Used by:
#   - lib/wip/wip-intake-shaper-lib.bash (CLI shaper, indirectly via disk read)
#   - .claude-plugin/commands/intake.md  (the /wip:intake skill body)
#
# Templates dir resolution lives in `wip_templates_dir` (lib). $WIP_TEMPLATES_DIR
# overrides; otherwise resolves to templates/ next to lib/wip/.
# shellcheck shell=bash

wip_plumbing_cmd_template() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_die 2 usage "template requires a subcommand (show|list)"
  shift
  case "$sub" in
    show) _wip_template_show "$@" ;;
    list) _wip_template_list "$@" ;;
    *) wip_die 2 usage "unknown template subcommand: $sub" ;;
  esac
}

_wip_template_show() {
  local id="${1:-}"
  [[ -n "$id" ]] || wip_die 2 usage "template show requires an <id>"
  case "$id" in
    /* | *..*) wip_die 2 usage "template id must be relative and contain no .." ;;
  esac
  local dir
  dir="$(wip_templates_dir)"
  [[ -n "$dir" && -d "$dir" ]] || wip_die 4 no-templates "templates dir not found" "$dir"
  local path="$dir/prompts/$id.md"
  if [[ ! -f "$path" ]]; then
    wip_die 4 unknown-template "no template at id $id" "$path"
  fi
  cat -- "$path"
}

_wip_template_list() {
  local dir
  dir="$(wip_templates_dir)"
  [[ -n "$dir" && -d "$dir" ]] || wip_die 4 no-templates "templates dir not found" "$dir"
  local prompts="$dir/prompts"
  if [[ ! -d "$prompts" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      printf '{"ok":true,"templates":[]}\n'
    fi
    return 0
  fi
  # Enumerate templates/prompts/**/*.md; emit {id, path} per entry. Sorted by id.
  local entries="[]" rel id obj
  # BSD-find safe: -type f -name '*.md'. Print paths relative to $prompts.
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    rel="${path#"$prompts"/}"
    id="${rel%.md}"
    obj="$(jq -nc --arg id "$id" --arg path "$path" '{id:$id, path:$path}')"
    entries="$(jq -nc --argjson a "$entries" --argjson o "$obj" '$a + [$o]')"
  done < <(find "$prompts" -type f -name '*.md' 2>/dev/null | LC_ALL=C sort)
  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc --argjson t "$entries" '{ok:true, templates:$t}'
  else
    printf '%s' "$entries" | jq -r '.[] | "\(.id)\t\(.path)"'
  fi
}
