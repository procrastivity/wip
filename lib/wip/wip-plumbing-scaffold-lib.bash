# wip-plumbing-scaffold-lib.bash — template + protected-path write helpers.
# Sourced by bin/wip-plumbing. Used by `init` (step-07) and later `workplan
# init` (step-08.5). Pure bash + sed. No jq, no yq, no network.
# shellcheck shell=bash

# wip_scaffold_now — current date as YYYY-MM-DD. Honors $WIP_NOW for tests.
wip_scaffold_now() {
  printf '%s\n' "${WIP_NOW:-$(date +%F)}"
}

# wip_scaffold_render <tmpl> <key=val>... — read <tmpl>, substitute {{key}}
# placeholders with the matching value, echo the rendered content to stdout.
# Values must not contain newlines or the sed delimiter (use ASCII unit
# separator 0x1f as the delimiter so common characters survive).
wip_scaffold_render() {
  local tmpl="$1"
  shift
  [[ -f "$tmpl" ]] || {
    printf 'wip-plumbing: scaffold template missing: %s\n' "$tmpl" >&2
    return 1
  }
  local content key val
  content="$(cat "$tmpl")"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      *=*)
        key="${1%%=*}"
        val="${1#*=}"
        content="$(printf '%s' "$content" | sed $'s\037{{'"$key"$'}}\037'"$val"$'\037g')"
        ;;
      *)
        printf 'wip-plumbing: scaffold render: bad key=val: %s\n' "$1" >&2
        return 1
        ;;
    esac
    shift
  done
  printf '%s' "$content"
}

# wip_scaffold_write_or_skip <dest> <content> — write <content> to <dest> if
# the file does not already exist (protected-path model). Honors $WIP_DRY_RUN
# (no write; same return code). Returns 0 on wrote, 1 on skipped-protected.
# Stderr on real I/O errors only.
wip_scaffold_write_or_skip() {
  local dest="$1" content="$2"
  if [[ -e "$dest" ]]; then
    return 1
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    return 0
  fi
  local dir
  dir="$(dirname -- "$dest")"
  mkdir -p -- "$dir" || {
    printf 'wip-plumbing: scaffold write: mkdir failed: %s\n' "$dir" >&2
    return 2
  }
  printf '%s' "$content" >"$dest" || {
    printf 'wip-plumbing: scaffold write: write failed: %s\n' "$dest" >&2
    return 2
  }
  return 0
}

# wip_scaffold_render_to <tmpl> <dest> <key=val>... — convenience: render then
# write-or-skip. Exit codes match write_or_skip (0 wrote, 1 skipped, 2 error).
wip_scaffold_render_to() {
  local tmpl="$1" dest="$2"
  shift 2
  local content
  content="$(wip_scaffold_render "$tmpl" "$@")" || return 2
  wip_scaffold_write_or_skip "$dest" "$content"
}
