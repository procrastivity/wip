# project — manage the global project registry (ADR-0008).
# Subcommands: list | register | resolve | forget.
# shellcheck shell=bash

wip_plumbing_cmd_project() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    list) _wip_project_list "$@" ;;
    register) _wip_project_register "$@" ;;
    resolve) _wip_project_resolve "$@" ;;
    forget) _wip_project_forget "$@" ;;
    "") wip_die 2 usage "project: missing subcommand (list|register|resolve|forget)" ;;
    *) wip_die 2 usage "project: unknown subcommand: $sub" ;;
  esac
}

_wip_project_list() {
  local json=0 prune=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --json) json=1 ;;
      --prune) prune=1 ;;
      *) wip_die 2 usage "project list: unknown arg: $1" ;;
    esac
    shift
  done

  local reg_file
  reg_file="$(wip_registry_path)"

  if [[ "$prune" == "1" && -f "$reg_file" ]]; then
    _wip_project_prune "$reg_file"
  fi

  if [[ ! -f "$reg_file" ]]; then
    if [[ "$json" == "1" ]]; then
      return 0
    fi
    printf 'ID\tSLUG\tPATH\tLAST_SEEN\n'
    return 0
  fi

  if [[ "$json" == "1" ]]; then
    jq -c '.' "$reg_file"
    return 0
  fi

  {
    printf 'ID\tSLUG\tPATH\tLAST_SEEN\n'
    jq -r '[.id, (.slug // "-"), .path, .last_seen] | @tsv' "$reg_file"
  } | column -t -s $'\t' 2>/dev/null || jq -r '[.id, (.slug // "-"), .path, .last_seen] | @tsv' "$reg_file"
}

_wip_project_prune() {
  local reg_file="$1"
  local tmp
  tmp="$reg_file.tmp.$$"
  : >"$tmp" 2>/dev/null || return 0
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    local p
    p="$(printf '%s' "$line" | jq -r '.path // ""' 2>/dev/null || printf '')"
    if [[ -n "$p" && -f "$p/.wip.yaml" ]]; then
      printf '%s\n' "$line" >>"$tmp"
    fi
  done <"$reg_file"
  mv -f "$tmp" "$reg_file" 2>/dev/null || rm -f "$tmp" 2>/dev/null
}

_wip_project_register() {
  local path="" slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --slug)
        slug="$2"
        shift 2
        ;;
      --slug=*)
        slug="${1#--slug=}"
        shift
        ;;
      -*) wip_die 2 usage "project register: unknown flag: $1" ;;
      *)
        if [[ -z "$path" ]]; then
          path="$1"
          shift
        else
          wip_die 2 usage "project register: unexpected arg: $1"
        fi
        ;;
    esac
  done
  [[ -n "$path" ]] || path="$PWD"
  # Resolve to abs path.
  if [[ "$path" != /* ]]; then
    # shellcheck disable=SC1007  # CDPATH= prefixes cd to neutralize CDPATH.
    path="$(CDPATH= cd -- "$path" 2>/dev/null && pwd)" ||
      wip_die 4 no-manifest "project register: path not found: $path"
  fi
  [[ -f "$path/.wip.yaml" ]] || wip_die 4 no-manifest "project register: no .wip.yaml at $path" ".wip.yaml"

  # Allow CLI slug to override manifest slug.
  local manifest_slug remote
  manifest_slug="$(yq -r '.slug // ""' "$path/.wip.yaml" 2>/dev/null || printf '')"
  [[ "$manifest_slug" == "null" ]] && manifest_slug=""
  [[ -z "$slug" ]] && slug="$manifest_slug"
  remote="$(git -C "$path" config --get remote.origin.url 2>/dev/null || printf '')"

  wip_registry_touch "$path" "$slug" "$remote" || true

  local id rec
  id="$(wip_registry_segment_encode "$path")"
  rec="$(_wip_registry_record_for "$(wip_registry_path)" "$id")"
  if [[ -n "$rec" ]]; then
    jq -n --argjson r "$rec" '{ok:true, record:$r}'
  else
    jq -n --arg id "$id" --arg path "$path" \
      '{ok:true, record:{id:$id,path:$path,slug:null,first_seen:null,last_seen:null,remote:null}}'
  fi
}

_wip_project_resolve() {
  [[ $# -ge 1 ]] || wip_die 2 usage "project resolve: missing <id>"
  local q="$1"
  shift
  [[ $# -eq 0 ]] || wip_die 2 usage "project resolve: unexpected arg: $1"

  local path rc reg_file id rec
  set +e
  path="$(wip_registry_resolve "$q" 2>/dev/null)"
  rc=$?
  set -e
  case "$rc" in
    0) ;;
    3) wip_die 3 not-found "project resolve: no project matches: $q" ;;
    4) wip_die 4 ambiguous "project resolve: slug is ambiguous: $q" ;;
    *) wip_die 1 internal "project resolve: failed for: $q" ;;
  esac

  reg_file="$(wip_registry_path)"
  id="$(wip_registry_segment_encode "$path")"
  rec="$(_wip_registry_record_for "$reg_file" "$id")"
  if [[ -n "$rec" ]]; then
    jq -n --argjson r "$rec" '{ok:true, record:$r}'
  else
    jq -n --arg id "$id" --arg path "$path" \
      '{ok:true, record:{id:$id,path:$path,slug:null,first_seen:null,last_seen:null,remote:null}}'
  fi
}

_wip_project_forget() {
  [[ $# -ge 1 ]] || wip_die 2 usage "project forget: missing <id>"
  local q="$1"
  shift
  [[ $# -eq 0 ]] || wip_die 2 usage "project forget: unexpected arg: $1"

  local reg_file
  reg_file="$(wip_registry_path)"
  [[ -f "$reg_file" ]] || wip_die 3 not-found "project forget: registry is empty"

  # Try to find by id, slug, or abs path.
  local target_id=""
  if [[ "$q" = /* ]]; then
    target_id="$(wip_registry_segment_encode "$q")"
  else
    local m
    m="$(jq -c --arg q "$q" 'select(.id == $q or .slug == $q)' "$reg_file" 2>/dev/null)"
    local n
    n="$(printf '%s\n' "$m" | grep -c .)"
    if ((n == 0)); then
      wip_die 3 not-found "project forget: no project matches: $q"
    fi
    if ((n > 1)); then
      printf '%s\n' "$m" >&2
      wip_die 4 ambiguous "project forget: slug is ambiguous: $q"
    fi
    target_id="$(printf '%s' "$m" | jq -r '.id')"
  fi

  local tmp
  tmp="$reg_file.tmp.$$"
  local before after
  before="$(grep -c . "$reg_file" 2>/dev/null || echo 0)"
  jq -c --arg id "$target_id" 'select(.id != $id)' "$reg_file" >"$tmp" 2>/dev/null || {
    rm -f "$tmp"
    wip_die 1 internal "project forget: rewrite failed"
  }
  after="$(grep -c . "$tmp" 2>/dev/null || echo 0)"
  mv -f "$tmp" "$reg_file"

  if ((before == after)); then
    wip_die 3 not-found "project forget: no project matches: $q"
  fi
  jq -n --arg id "$target_id" '{ok:true, forgot:$id}'
}
