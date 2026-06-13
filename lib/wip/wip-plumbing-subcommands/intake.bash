# intake — v0 single-kind validator (ADR-0009 surface; per-kind rules in step-07.5).
# shellcheck shell=bash

wip_plumbing_cmd_intake() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    validate) _wip_intake_validate_v0 "$@" ;;
    classify | apply)
      wip_die 2 not-implemented \
        "intake $sub: not in v0 — lands in step-07.5 (see engineering/specs/intake-kinds.md)"
      ;;
    "") wip_die 2 usage "intake: missing subcommand (validate)" ;;
    *) wip_die 2 usage "intake: unknown subcommand: $sub" ;;
  esac
}

# v0 shape check: file is parseable + has an H1 title + has a `## Goal` or
# `## Summary` heading. No front-matter parsing, no per-kind dispatch.
_wip_intake_validate_v0() {
  local file=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --kind | --kind=*)
        wip_die 2 not-implemented \
          "intake validate: --kind lands in step-07.5 (see engineering/specs/intake-kinds.md)"
        ;;
      -*) wip_die 2 usage "intake validate: unknown flag: $1" ;;
      *)
        if [[ -z "$file" ]]; then
          file="$1"
          shift
        else
          wip_die 2 usage "intake validate: unexpected arg: $1"
        fi
        ;;
    esac
  done

  [[ -n "$file" ]] || wip_die 2 usage "intake validate: missing <file>"
  [[ -f "$file" && -r "$file" ]] ||
    wip_die 2 not-found "intake validate: file not readable: $file"

  local has_title has_goal_or_summary
  has_title=0
  has_goal_or_summary=0
  if awk '/^# [^[:space:]]/ { found=1 } END { exit !found }' "$file"; then
    has_title=1
  fi
  if awk '/^## (Goal|Summary)([[:space:]]|$)/ { found=1 } END { exit !found }' "$file"; then
    has_goal_or_summary=1
  fi

  local missing="[]" valid=true
  if [[ "$has_title" == "0" ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["title"]')"
    valid=false
  fi
  if [[ "$has_goal_or_summary" == "0" ]]; then
    missing="$(jq -nc --argjson a "$missing" '$a + ["goal-or-summary-section"]')"
    valid=false
  fi

  jq -nc --arg file "$file" --argjson valid "$valid" --argjson missing "$missing" '
    { ok: $valid, file: $file, kind: null, valid: $valid, missing: $missing }'

  [[ "$valid" == "true" ]] || exit 4
}
