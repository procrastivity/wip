# wip-plumbing-setup-lib.bash — write-or-skip-or-refuse helpers for the
# `setup` family. Sourced by lib/wip/wip-plumbing-subcommands/setup.bash.
#
# step-14's contract on each infrastructure file (per the workplan):
#   - absent             → write the template bytes        (status: wrote)
#   - present, byte-eq   → silent skip                     (status: skipped)
#   - present, differs   → refuse                          (status: refused)
#   - present, differs,
#     and $WIP_SETUP_FORCE=1 → overwrite                   (status: wrote_forced)
#
# `wip_setup_write_or_skip_present` is the lock-style "skip if present, never
# compare" variant for files like `flake.lock` that legitimately diverge per
# consumer (`nix flake update` rolls inputs forward).
#
# `$WIP_DRY_RUN=1` short-circuits all writes; the status is computed against
# the on-disk state but nothing is mutated.
# shellcheck shell=bash

# wip_setup_write_idempotent <template-path> <dest-path>
#
# Three-way write-or-skip-or-refuse. Prints one of {wrote, skipped, refused,
# wrote_forced} on stdout. Returns 0 on every status except refused (4) and
# I/O failure (2). Stderr is reserved for real I/O errors.
wip_setup_write_idempotent() {
  local tmpl="$1" dest="$2"
  [[ -f "$tmpl" ]] || {
    printf 'wip-plumbing: setup: template missing: %s\n' "$tmpl" >&2
    return 2
  }
  if [[ -e "$dest" ]]; then
    if cmp -s -- "$tmpl" "$dest"; then
      printf 'skipped'
      return 0
    fi
    if [[ "${WIP_SETUP_FORCE:-0}" != "1" ]]; then
      printf 'refused'
      return 4
    fi
    if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
      printf 'wrote_forced'
      return 0
    fi
    _wip_setup_copy_atomic "$tmpl" "$dest" || return 2
    printf 'wrote_forced'
    return 0
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'wrote'
    return 0
  fi
  _wip_setup_copy_atomic "$tmpl" "$dest" || return 2
  printf 'wrote'
  return 0
}

# wip_setup_write_or_skip_present <template-path> <dest-path>
#
# "Skip if present, never compare" — for `flake.lock` and other files that
# legitimately drift per consumer. Returns wrote or skipped (or wrote_forced
# under $WIP_SETUP_FORCE=1). Same exit-code contract as write_idempotent.
wip_setup_write_or_skip_present() {
  local tmpl="$1" dest="$2"
  [[ -f "$tmpl" ]] || {
    printf 'wip-plumbing: setup: template missing: %s\n' "$tmpl" >&2
    return 2
  }
  if [[ -e "$dest" ]]; then
    if [[ "${WIP_SETUP_FORCE:-0}" == "1" ]]; then
      if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
        printf 'wrote_forced'
        return 0
      fi
      _wip_setup_copy_atomic "$tmpl" "$dest" || return 2
      printf 'wrote_forced'
      return 0
    fi
    printf 'skipped'
    return 0
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'wrote'
    return 0
  fi
  _wip_setup_copy_atomic "$tmpl" "$dest" || return 2
  printf 'wrote'
  return 0
}

# _wip_setup_copy_atomic <src> <dest> — mkdir -p the parent, copy bytes via
# a tmpfile + mv so a forced exit can't leave a half-written file. Stderr on
# real I/O errors only.
_wip_setup_copy_atomic() {
  local src="$1" dest="$2" dir tmp
  dir="$(dirname -- "$dest")"
  mkdir -p -- "$dir" || {
    printf 'wip-plumbing: setup: mkdir failed: %s\n' "$dir" >&2
    return 1
  }
  tmp="$(mktemp -- "$dest.XXXXXX")" || {
    printf 'wip-plumbing: setup: mktemp failed in: %s\n' "$dir" >&2
    return 1
  }
  if ! cat -- "$src" >"$tmp"; then
    rm -f -- "$tmp"
    printf 'wip-plumbing: setup: write failed: %s\n' "$dest" >&2
    return 1
  fi
  mv -f -- "$tmp" "$dest" || {
    rm -f -- "$tmp"
    printf 'wip-plumbing: setup: mv failed: %s\n' "$dest" >&2
    return 1
  }
  return 0
}

# wip_setup_walk_template_tree <template-root> <dest-root>
#
# Walk every regular file under <template-root>; for each, compute the
# repo-relative path, choose the write helper (lock-style for
# `flake.lock`; strict for everything else), and invoke it. Emit one
# `<status><TAB><relpath>` line per file on stdout (suitable for piping
# into the subcommand ledger fold). Returns 0 unless a refusal occurred,
# in which case the highest-priority status is "refused" but the walk
# still completes (so the ledger lists every offender).
wip_setup_walk_template_tree() {
  local tmpl_root="$1" dest_root="$2"
  [[ -d "$tmpl_root" ]] || {
    printf 'wip-plumbing: setup: template tree missing: %s\n' "$tmpl_root" >&2
    return 1
  }
  local saw_refused=0
  local f rel dest status rc
  while IFS= read -r f; do
    rel="${f#"$tmpl_root/"}"
    dest="$dest_root/$rel"
    if [[ "$rel" == "flake.lock" ]]; then
      set +e
      status="$(wip_setup_write_or_skip_present "$f" "$dest")"
      rc=$?
      set -e
    else
      set +e
      status="$(wip_setup_write_idempotent "$f" "$dest")"
      rc=$?
      set -e
    fi
    case "$rc" in
      0) ;;
      4) saw_refused=1 ;;
      *)
        printf 'wip-plumbing: setup: write helper failed (%d) for %s\n' "$rc" "$rel" >&2
        return 1
        ;;
    esac
    printf '%s\t%s\n' "$status" "$rel"
  done < <(find "$tmpl_root" -type f | LC_ALL=C sort)
  [[ "$saw_refused" -eq 0 ]] || return 4
  return 0
}

# wip_setup_set_feature_flag <manifest> <feature> <key=value>...
#
# Idempotently set features.<feature>.<key>: <value> for each kv-pair.
# Creates features.<feature> if absent. yq -i in place. Stderr on yq error.
# Honors $WIP_DRY_RUN (no write). Returns 0 on no-op or success; 1 on yq error.
#
# Returns "updated" or "noop" on stdout so the caller can include
# manifest_updated in the ledger.
wip_setup_set_feature_flag() {
  local manifest="$1" feature="$2"
  shift 2
  [[ -f "$manifest" ]] || {
    printf 'wip-plumbing: setup: manifest missing: %s\n' "$manifest" >&2
    return 1
  }
  local current_json desired_json
  current_json="$(FEATURE="$feature" yq -o=json -I=0 '.features[strenv(FEATURE)] // {}' "$manifest" 2>/dev/null)" || current_json="{}"
  desired_json="$current_json"
  local kv key val
  for kv in "$@"; do
    case "$kv" in
      *=*)
        key="${kv%%=*}"
        val="${kv#*=}"
        desired_json="$(KEY="$key" VAL="$val" jq -c \
          --argjson v "$(_wip_setup_jsonify "$val")" \
          '.[env.KEY] = $v' <<<"$desired_json")" || return 1
        ;;
      *)
        printf 'wip-plumbing: setup: bad feature kv: %s\n' "$kv" >&2
        return 1
        ;;
    esac
  done
  if [[ "$(jq -cS . <<<"$current_json")" == "$(jq -cS . <<<"$desired_json")" ]]; then
    printf 'noop'
    return 0
  fi
  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'updated'
    return 0
  fi
  FEATURE="$feature" DESIRED="$desired_json" \
    yq -i '.features[strenv(FEATURE)] = (strenv(DESIRED) | from_json)' "$manifest" || return 1
  printf 'updated'
  return 0
}

# _wip_setup_jsonify <val> — convert a bash string to a JSON scalar. "true",
# "false", and pure integers become JSON booleans/numbers; everything else is
# a JSON string. Keeps the manifest tidy (no quoted "true").
_wip_setup_jsonify() {
  local v="$1"
  case "$v" in
    true | false) printf '%s' "$v" ;;
    '' | *[!0-9]*) jq -n --arg s "$v" '$s' ;;
    *) printf '%s' "$v" ;;
  esac
}

# wip_setup_sentinel_for <feature> — echo the sentinel path for a feature, or
# empty if the feature has no sentinel. Mirrors the map in
# `_wip_feature_records` so the setup post-check stays aligned with detect.
wip_setup_sentinel_for() {
  case "$1" in
    direnv) printf '.envrc' ;;
    changelog) printf 'CHANGELOG.md' ;;
    *) printf '' ;;
  esac
}
