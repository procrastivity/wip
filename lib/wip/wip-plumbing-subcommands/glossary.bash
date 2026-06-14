# glossary — assemble / check the effective glossary for this project.
#
# v1 surface (step-13): two subcommands.
#   assemble [--output <path>]   render core.md + enabled-feature partials
#   check                        compare on-disk .wip/GLOSSARY.md vs fresh
#
# `assemble` defaults to stdout (markdown bytes, no envelope — like
# `template show`); --output writes the file atomically and emits a JSON
# write ledger. `check` is the drift seam wired into pre-commit.
#
# Inclusion rules live in wip-plumbing-glossary-lib.bash (single source of
# truth). Adding a new partial (e.g. lds.md) is a one-row addition there.
# shellcheck shell=bash

wip_plumbing_cmd_glossary() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_die 2 usage "glossary requires a subcommand (assemble|check)"
  shift
  case "$sub" in
    assemble) _wip_glossary_assemble "$@" ;;
    check) _wip_glossary_check "$@" ;;
    *) wip_die 2 usage "unknown glossary subcommand: $sub" ;;
  esac
}

_wip_glossary_load() {
  # Source the lib once. Idempotent — subsequent calls are no-ops.
  if ! declare -F wip_glossary_render >/dev/null 2>&1; then
    # shellcheck source=lib/wip/wip-plumbing-glossary-lib.bash
    source "$WIP_LIB/wip-plumbing-glossary-lib.bash"
  fi
}

_wip_glossary_assemble() {
  _wip_glossary_load
  local output=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --output)
        [[ $# -ge 2 ]] || wip_die 2 usage "--output requires a path"
        output="$2"
        shift 2
        ;;
      --output=*)
        output="${1#--output=}"
        shift
        ;;
      *) wip_die 2 usage "glossary assemble: unknown arg: $1" ;;
    esac
  done

  local root mj dir
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")" || true
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  dir="$(wip_templates_dir)"
  [[ -n "$dir" && -d "$dir" ]] || wip_die 4 no-templates "templates dir not found" "$dir"

  local resolved
  resolved="$(wip_glossary_resolve "$mj")" || wip_die 4 no-templates "templates dir not found" "$dir"

  if [[ -z "$output" ]]; then
    # stdout-first: raw bytes, no envelope.
    wip_glossary_render "$root" "$mj"
    return 0
  fi

  # --output path: atomic write + JSON ledger.
  local abs_out
  case "$output" in
    /*) abs_out="$output" ;;
    *) abs_out="$root/$output" ;;
  esac

  local included skipped
  included="$(jq -c '[.[] | select(.body_present) | {name, source_path, predicate}]' <<<"$resolved")"
  skipped="$(jq -c '
    [.[] | select(.body_present | not) |
      {name, predicate, reason: "predicate-true; partial-not-shipped"}]' <<<"$resolved")"

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    jq -nc \
      --arg wrote "$output" \
      --argjson included "$included" \
      --argjson skipped "$skipped" \
      --argjson dry true '
      {ok:true, dry_run:$dry, wrote:[$wrote],
       partials_included:$included, partials_skipped:$skipped}'
    return 0
  fi

  local tmp
  tmp="$(mktemp "${abs_out}.XXXXXX")" || wip_die 1 internal "mktemp failed near $abs_out"
  if ! wip_glossary_render "$root" "$mj" >"$tmp"; then
    rm -f "$tmp"
    wip_die 1 render-failed "glossary render failed"
  fi
  mv -f "$tmp" "$abs_out"

  jq -nc \
    --arg wrote "$output" \
    --argjson included "$included" \
    --argjson skipped "$skipped" '
    {ok:true, wrote:[$wrote],
     partials_included:$included, partials_skipped:$skipped}'
}

_wip_glossary_check() {
  _wip_glossary_load
  while [[ $# -gt 0 ]]; do
    case "$1" in
      *) wip_die 2 usage "glossary check: unknown arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")" || true
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  local dir
  dir="$(wip_templates_dir)"
  [[ -n "$dir" && -d "$dir" ]] || wip_die 4 no-templates "templates dir not found" "$dir"

  local target target_abs resolved
  target="$(wip_glossary_target_path "$root" "$mj")"
  target_abs="$root/$target"

  resolved="$(wip_glossary_resolve "$mj")"

  local expected actual_bytes drift byte_diff
  expected="$(wip_glossary_render "$root" "$mj")"
  if [[ -f "$target_abs" ]]; then
    actual_bytes="$(cat -- "$target_abs")"
  else
    actual_bytes=""
  fi

  if [[ "$expected" == "$actual_bytes" ]]; then
    drift="false"
    byte_diff=0
  else
    drift="true"
    # Byte count delta (absolute). Cheap shell-side branching signal.
    local e_len a_len
    e_len="${#expected}"
    a_len="${#actual_bytes}"
    if [[ "$e_len" -ge "$a_len" ]]; then
      byte_diff=$((e_len - a_len))
    else
      byte_diff=$((a_len - e_len))
    fi
  fi

  local included
  included="$(jq -c '
    [.[] | {name, predicate,
      status: (if .body_present then "included" else "skipped" end),
      reason: (if .body_present then .predicate else "predicate-true; partial-not-shipped" end)
    }]' <<<"$resolved")"

  if [[ "$drift" == "false" ]]; then
    jq -nc \
      --arg expected_path "$target" \
      --argjson partials "$included" '
      {ok:true, drift:false, expected_path:$expected_path, partials:$partials}'
    return 0
  fi

  # Drift: emit JSON envelope on stdout (ok:false) + unified diff on stderr.
  jq -nc \
    --arg expected_path "$target" \
    --argjson byte_diff "$byte_diff" \
    --argjson partials "$included" '
    {ok:false, drift:true,
     error:{code:4, kind:"glossary-drift",
            message:("on-disk " + $expected_path + " differs from assembled glossary")},
     expected_path:$expected_path,
     actual_path:$expected_path,
     byte_diff_count:$byte_diff,
     partials:$partials}'

  if [[ "${WIP_QUIET:-0}" != "1" ]]; then
    printf 'wip-plumbing: glossary drift: %s differs from fresh assemble\n' "$target" >&2
    printf 'wip-plumbing: regenerate with: wip-plumbing glossary assemble > %s\n' "$target" >&2
    diff -u <(printf '%s\n' "$actual_bytes") <(printf '%s\n' "$expected") \
      --label "$target (on disk)" --label "$target (assembled)" >&2 || true
  fi
  exit 4
}
