#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="run"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Regression for test/run itself — the parallel, fail-aggregating runner. This
# exercises the FAIL path a green suite can't: write a passing fixture and a
# failing fixture into a mktemp workspace, then invoke `bash test/run` with
# EXPLICIT fixture paths so the runner does NOT re-discover the real suite (or
# this file) — no recursion.

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Classify a captured exit status as "zero"/"nonzero" so non-zero aggregation
# can be asserted with the harness's assert_eq (helpers has no assert_nonzero).
rc_class() {
  if [[ "$1" -eq 0 ]]; then
    echo zero
  else
    echo nonzero
  fi
}

# Two fixtures shaped like minimal test files: one passes (exit 0), one fails
# (exit non-zero). test/run's worker just runs `bash <file>` and records its
# exit status, so an exit code (plus a summary-style line) is all a fixture
# needs.
pass="$tmp/test-fixture-pass.sh"
fail="$tmp/test-fixture-fail.sh"

cat >"$pass" <<'SH'
#!/usr/bin/env bash
echo "fixture-pass: 1 passed, 0 failed"
exit 0
SH

cat >"$fail" <<'SH'
#!/usr/bin/env bash
echo "fixture-fail: 0 passed, 1 failed"
exit 1
SH

# (a) all-pass set ⇒ exit 0.
set +e
bash test/run "$pass" "$pass" >"$tmp/allpass.out" 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "all-pass set exits 0"

# (b) any-fail set ⇒ exit non-zero AND the failing file is named in output.
set +e
bash test/run "$pass" "$fail" >"$tmp/anyfail.out" 2>&1
rc=$?
set -e
assert_eq "nonzero" "$(rc_class "$rc")" "any-fail set exits non-zero"
assert_grep "test-fixture-fail.sh" "$tmp/anyfail.out" "failing file named in output"

# (c) --serial still aggregates the failure (exit non-zero).
set +e
bash test/run --serial "$pass" "$fail" >"$tmp/serial.out" 2>&1
rc=$?
set -e
assert_eq "nonzero" "$(rc_class "$rc")" "--serial aggregates failure (non-zero)"
assert_grep "test-fixture-fail.sh" "$tmp/serial.out" "--serial names failing file"

# (d) WIP_TEST_JOBS=1 still aggregates the failure (exit non-zero).
set +e
WIP_TEST_JOBS=1 bash test/run "$pass" "$fail" >"$tmp/jobs1.out" 2>&1
rc=$?
set -e
assert_eq "nonzero" "$(rc_class "$rc")" "WIP_TEST_JOBS=1 aggregates failure (non-zero)"

# (e) WIP_TEST_TIMING=1 emits a timing line; an all-pass timing run still exits 0.
set +e
WIP_TEST_TIMING=1 bash test/run "$pass" "$pass" >"$tmp/timing.out" 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "timing run exits 0 on all-pass"
assert_grep "Full-suite wall time:" "$tmp/timing.out" "WIP_TEST_TIMING=1 emits timing line"

test_summary
