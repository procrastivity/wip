# test/helpers.sh — minimal assertion harness. Plain bash, no bats.
# Sourced by test/test-*.sh after they `cd` to the repo root.
# shellcheck shell=bash

_WIP_PASS=0
_WIP_FAIL=0
_WIP_TEST_NAME="${_WIP_TEST_NAME:-tests}"

assert_eq() {
  local expected="$1" actual="$2" msg="${3:-assert_eq}"
  if [[ "$expected" == "$actual" ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       expected: %q\n       actual:   %q\n' "$msg" "$expected" "$actual" >&2
  fi
}

assert_file() {
  local path="$1" msg="${2:-assert_file}"
  if [[ -f "$path" ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       missing: %s\n' "$msg" "$path" >&2
  fi
}

assert_absent() {
  local path="$1" msg="${2:-assert_absent}"
  if [[ ! -e "$path" ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       unexpected: %s\n' "$msg" "$path" >&2
  fi
}

assert_grep() {
  local pattern="$1" path="$2" msg="${3:-assert_grep}"
  if grep -q -- "$pattern" "$path" 2>/dev/null; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       pattern: %s\n       file:    %s\n' "$msg" "$pattern" "$path" >&2
  fi
}

assert_cmp() {
  local a="$1" b="$2" msg="${3:-assert_cmp}"
  if cmp -s -- "$a" "$b"; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       a: %s\n       b: %s\n' "$msg" "$a" "$b" >&2
  fi
}

assert_not_grep() {
  local pattern="$1" path="$2" msg="${3:-assert_not_grep}"
  if ! grep -q -- "$pattern" "$path" 2>/dev/null; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "$msg"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n       unexpected: %s in %s\n' "$msg" "$pattern" "$path" >&2
  fi
}

# test_summary — print counts; return nonzero if any failed (fails `make test`).
test_summary() {
  printf '%s: %d passed, %d failed\n' "$_WIP_TEST_NAME" "$_WIP_PASS" "$_WIP_FAIL"
  [[ "$_WIP_FAIL" -eq 0 ]]
}

# --- fixture builders -------------------------------------------------------
# DRY helpers for constructing the canonical wip fixtures that test-*.sh files
# inline today. See workplans/step-03-dry-fixture-builders.md (D2–D4) for the
# opt tables and the generic-vs-keep-inline rule.

# wip_mktemp — `mktemp -d` with automatic cleanup. Every call registers its
# dir; a single EXIT trap removes them all, so a test file can call it many
# times without clobbering an earlier trap. Echoes the new dir.
_WIP_TMPDIRS=()
_wip_cleanup_tmpdirs() {
  local d
  for d in "${_WIP_TMPDIRS[@]:-}"; do
    [[ -n "$d" ]] && rm -rf "$d"
  done
}
wip_mktemp() {
  local d
  d="$(mktemp -d)"
  _WIP_TMPDIRS+=("$d")
  trap _wip_cleanup_tmpdirs EXIT
  printf '%s\n' "$d"
}

# wip_fixture_init <dir> [opts] — write a canonical <dir>/.wip.yaml for an
# in-flight initiative; also create <dir>/.wip/initiatives/<slug>/. See the
# opts table in workplans/step-03-dry-fixture-builders.md.
wip_fixture_init() {
  local dir="$1"
  shift
  # NB: name the status local `istatus`, not `status` — `status` is a
  # read-only special parameter in zsh and a portability footgun.
  local slug="demo" title="" istatus="in-flight" active_step="step-02"
  local want_brief=0 want_solo=0 want_orch=0
  while (($#)); do
    case "$1" in
      --slug)
        slug="$2"
        shift 2
        ;;
      --title)
        title="$2"
        shift 2
        ;;
      --status)
        istatus="$2"
        shift 2
        ;;
      --active-step)
        active_step="$2"
        shift 2
        ;;
      --no-active-step)
        active_step=""
        shift
        ;;
      --brief)
        want_brief=1
        shift
        ;;
      --solo)
        want_solo=1
        shift
        ;;
      --orchestration)
        want_orch=1
        shift
        ;;
      *)
        printf 'wip_fixture_init: unknown opt %q\n' "$1" >&2
        return 2
        ;;
    esac
  done
  mkdir -p "$dir/.wip/initiatives/$slug"
  {
    printf 'version: 1\n'
    printf 'features:\n'
    printf '  wip: { enabled: true, root: .wip }\n'
    if ((want_solo)); then printf '  solo: { enabled: true }\n'; fi
    if ((want_orch)); then
      printf '  orchestration: { enabled: true, backend: solo }\n'
    fi
    printf 'current_initiative: %s\n' "$slug"
    printf 'initiatives:\n'
    printf '  - slug: %s\n' "$slug"
    if [[ -n "$title" ]]; then printf '    title: %s\n' "$title"; fi
    printf '    status: %s\n' "$istatus"
    if [[ -n "$active_step" ]]; then
      printf '    active_step: %s\n' "$active_step"
    fi
    if ((want_brief)); then
      printf '    brief: .wip/initiatives/%s/BRIEF.md\n' "$slug"
    fi
    printf '    roadmap: .wip/initiatives/%s/roadmap.md\n' "$slug"
  } >"$dir/.wip.yaml"
}

# wip_fixture_roadmap <dir> [opts] — write a canonical roadmap.md. See the
# opts table in workplans/step-03-dry-fixture-builders.md.
wip_fixture_roadmap() {
  local dir="$1"
  shift
  local slug="demo" round2=0 deferred=0 backlog=0
  while (($#)); do
    case "$1" in
      --slug)
        slug="$2"
        shift 2
        ;;
      --round2)
        round2=1
        shift
        ;;
      --deferred)
        deferred=1
        shift
        ;;
      --backlog)
        backlog=1
        shift
        ;;
      *)
        printf 'wip_fixture_roadmap: unknown opt %q\n' "$1" >&2
        return 2
        ;;
    esac
  done
  mkdir -p "$dir/.wip/initiatives/$slug"
  {
    printf '# Roadmap — %s\n\n' "$slug"
    printf '## Round 1 — One\n\n'
    printf -- '- **step-01 — First** ✅ shipped 2026-05-01 — done.\n'
    printf -- '- **step-02 — Second** — current.\n'
    printf -- '- **step-03 — Third** — later.\n'
    if ((round2)); then
      printf '\n## Round 2 — Two\n\n'
      printf -- '- **step-04 — Fourth** — round 2.\n'
    fi
    if ((deferred)); then
      printf '\n## Deferred (decided-not-now)\n\n'
      printf -- '- **Round-level closeout writes** — postponed; revisit after v1.\n'
    fi
    if ((backlog)); then
      printf '\n## Backlog\n\n'
      printf -- '- **Cleanup chore** — sweep stragglers.\n'
    fi
  } >"$dir/.wip/initiatives/$slug/roadmap.md"
}
