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
