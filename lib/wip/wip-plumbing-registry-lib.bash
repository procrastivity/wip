# wip-plumbing-registry-lib.bash — global project registry (ADR-0008).
# Derived cache at $XDG_STATE_HOME/wip/projects.jsonl. Write errors never fail
# the calling verb; with WIP_VERBOSE=1, one-line stderr diagnostics.
# shellcheck shell=bash

# wip_registry_path — echo the registry file path.
wip_registry_path() {
  if [[ -n "${WIP_REGISTRY_FILE:-}" ]]; then
    printf '%s\n' "$WIP_REGISTRY_FILE"
  else
    printf '%s/wip/projects.jsonl\n' "${XDG_STATE_HOME:-$HOME/.local/state}"
  fi
}

# Internal: stderr diagnostic when WIP_VERBOSE=1.
_wip_registry_warn() {
  [[ "${WIP_VERBOSE:-0}" == "1" ]] && printf 'wip-plumbing: registry: %s\n' "$*" >&2
  return 0
}

# wip_registry_segment_encode <abs-path> — `/Users/x/y` -> `-Users-x-y`.
wip_registry_segment_encode() {
  local p="$1"
  [[ "$p" = /* ]] || return 1
  printf '%s\n' "${p//\//-}"
}

# wip_registry_segment_decode <segment> — `-Users-x-y` -> `/Users/x/y`.
wip_registry_segment_decode() {
  local s="$1"
  [[ "$s" = -* ]] || return 1
  printf '%s\n' "${s//-/\/}"
}

# _wip_iso_now — current UTC time in ISO-8601.
_wip_iso_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# _wip_iso_to_epoch <iso> — portable across GNU/BSD date.
_wip_iso_to_epoch() {
  local iso="$1" out=""
  [[ -n "$iso" ]] || return 1
  out="$(date -d "$iso" +%s 2>/dev/null)" && {
    printf '%s\n' "$out"
    return 0
  }
  out="$(date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "$iso" +%s 2>/dev/null)" && {
    printf '%s\n' "$out"
    return 0
  }
  return 1
}

# wip_registry_with_lock <reg-file> <cmd...> — wrap with flock(1) when available.
wip_registry_with_lock() {
  local reg_file="$1"
  shift
  local lock_file
  lock_file="$(dirname "$reg_file")/projects.lock"
  if command -v flock >/dev/null 2>&1; then
    (
      exec 9>"$lock_file" 2>/dev/null || {
        "$@"
        exit $?
      }
      flock -x 9 2>/dev/null || true
      "$@"
    )
  else
    "$@"
  fi
}

# _wip_registry_record_for <reg-file> <id> — emit matching record line (or empty).
_wip_registry_record_for() {
  local reg_file="$1" id="$2"
  [[ -f "$reg_file" ]] || return 0
  jq -c --arg id "$id" 'select(.id == $id)' "$reg_file" 2>/dev/null | head -n1
}

# _wip_registry_rewrite <reg-file> <id> <new-record-json>
#   Stream existing records, replace match by id (or append), atomic-rename.
_wip_registry_rewrite() {
  local reg_file="$1" id="$2" new_rec="$3"
  local dir tmp
  dir="$(dirname "$reg_file")"
  mkdir -p "$dir" 2>/dev/null || {
    _wip_registry_warn "mkdir failed: $dir"
    return 0
  }
  tmp="$reg_file.tmp.$$"
  {
    if [[ -f "$reg_file" ]]; then
      jq -c --arg id "$id" 'select(.id != $id)' "$reg_file" 2>/dev/null || true
    fi
    printf '%s\n' "$new_rec"
  } >"$tmp" 2>/dev/null || {
    _wip_registry_warn "write failed: $tmp"
    rm -f "$tmp" 2>/dev/null
    return 0
  }
  mv -f "$tmp" "$reg_file" 2>/dev/null || {
    _wip_registry_warn "rename failed: $reg_file"
    rm -f "$tmp" 2>/dev/null
    return 0
  }
  return 0
}

# wip_registry_touch <abs> <slug> <remote>
#   Fast-then-slow upsert. Empty slug/remote -> null. Silent on success.
wip_registry_touch() {
  local abs="$1" slug="${2:-}" remote="${3:-}"
  [[ -n "$abs" ]] || return 0
  local id reg_file now
  id="$(wip_registry_segment_encode "$abs")" || return 0
  reg_file="$(wip_registry_path)"
  now="$(_wip_iso_now)"

  wip_registry_with_lock "$reg_file" _wip_registry_touch_inner \
    "$reg_file" "$id" "$abs" "$slug" "$remote" "$now"
}

_wip_registry_touch_inner() {
  local reg_file="$1" id="$2" abs="$3" slug="$4" remote="$5" now="$6"
  local existing first_seen new_rec
  local slug_json remote_json

  existing="$(_wip_registry_record_for "$reg_file" "$id")"
  local cur_slug=""
  if [[ -n "$existing" ]]; then
    cur_slug="$(printf '%s' "$existing" | jq -r '.slug // ""')"
    # Empty input slug preserves the existing one (auto-touch shouldn't clobber
    # a slug set via `project register --slug`).
    [[ -z "$slug" && -n "$cur_slug" ]] && slug="$cur_slug"
  fi

  slug_json="$(jq -nc --arg s "$slug" 'if $s == "" then null else $s end')"
  remote_json="$(jq -nc --arg r "$remote" 'if $r == "" then null else $r end')"

  if [[ -n "$existing" ]]; then
    local cur_last cur_path cur_remote
    cur_last="$(printf '%s' "$existing" | jq -r '.last_seen // ""')"
    cur_path="$(printf '%s' "$existing" | jq -r '.path // ""')"
    cur_remote="$(printf '%s' "$existing" | jq -r '.remote // ""')"
    if [[ "$cur_slug" == "$slug" && "$cur_path" == "$abs" && "$cur_remote" == "$remote" ]]; then
      local cur_ep now_ep
      cur_ep="$(_wip_iso_to_epoch "$cur_last" || true)"
      now_ep="$(_wip_iso_to_epoch "$now" || true)"
      if [[ -n "$cur_ep" && -n "$now_ep" ]] && ((now_ep - cur_ep < 60)); then
        return 0
      fi
    fi
    first_seen="$(printf '%s' "$existing" | jq -r '.first_seen // ""')"
    [[ -n "$first_seen" ]] || first_seen="$now"
  else
    first_seen="$now"
  fi

  new_rec="$(jq -nc \
    --arg id "$id" --arg path "$abs" --argjson slug "$slug_json" \
    --arg first "$first_seen" --arg last "$now" --argjson remote "$remote_json" \
    '{id:$id,path:$path,slug:$slug,first_seen:$first,last_seen:$last,remote:$remote}')"

  _wip_registry_rewrite "$reg_file" "$id" "$new_rec"
}

# wip_registry_touch_root <abs> — derive slug/remote then call touch.
#   Honors WIP_NO_REGISTRY=1 and plumbing.register:false in .wip.yaml.
wip_registry_touch_root() {
  local abs="$1"
  [[ -n "$abs" ]] || return 0
  [[ "${WIP_NO_REGISTRY:-0}" == "1" ]] && return 0

  local reg slug remote
  reg="$(yq -r '.plumbing.register | tostring' "$abs/.wip.yaml" 2>/dev/null || printf 'null')"
  [[ "$reg" == "false" ]] && return 0

  slug="$(yq -r '.slug // ""' "$abs/.wip.yaml" 2>/dev/null || printf '')"
  [[ "$slug" == "null" ]] && slug=""
  remote="$(git -C "$abs" config --get remote.origin.url 2>/dev/null || printf '')"

  wip_registry_touch "$abs" "$slug" "$remote" || true
}

# wip_registry_iter — emit each registry record as one JSONL line on stdout.
wip_registry_iter() {
  local reg_file
  reg_file="$(wip_registry_path)"
  [[ -f "$reg_file" ]] || return 0
  jq -c '.' "$reg_file" 2>/dev/null || true
}

# wip_registry_resolve <id>
#   On success: print abs path on stdout, exit 0.
#   On ambiguous slug: print candidate records to stderr, exit 4.
#   On not found: exit 3.
wip_registry_resolve() {
  local q="$1"
  [[ -n "$q" ]] || return 3

  if [[ "$q" = /* ]]; then
    [[ -f "$q/.wip.yaml" ]] && {
      printf '%s\n' "$q"
      return 0
    }
    return 3
  fi

  local reg_file
  reg_file="$(wip_registry_path)"
  [[ -f "$reg_file" ]] || return 3

  local id_match
  id_match="$(jq -c --arg q "$q" 'select(.id == $q)' "$reg_file" 2>/dev/null | head -n1)"
  if [[ -n "$id_match" ]]; then
    printf '%s\n' "$id_match" | jq -r '.path'
    return 0
  fi

  local slug_matches
  slug_matches="$(jq -c --arg q "$q" 'select(.slug == $q)' "$reg_file" 2>/dev/null)"
  if [[ -z "$slug_matches" ]]; then
    return 3
  fi
  local n
  n="$(printf '%s\n' "$slug_matches" | grep -c .)"
  if ((n > 1)); then
    printf '%s\n' "$slug_matches" >&2
    return 4
  fi
  printf '%s\n' "$slug_matches" | jq -r '.path'
  return 0
}
