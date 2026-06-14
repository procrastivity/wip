#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-flags"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML

cat >"$tmp/seed.md" <<'MD'
# Stub
## Goal
A goal.
MD

# --- missing file arg -> exit 2 ---------------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "no file -> exit 2"

# --- nonexistent file -> exit 2 ---------------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake "$tmp/does-not-exist.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing file -> exit 2"

# --- bogus --kind -> exit 2 -------------------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake "$tmp/seed.md" --kind bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "bogus --kind -> exit 2"

# --- non-numeric --max-rounds -> exit 2 -------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake "$tmp/seed.md" --kind brief --max-rounds foo >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "non-numeric --max-rounds -> exit 2"

# --- unknown flag -> exit 2 -------------------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD='cat >/dev/null; printf "{}"' \
  bin/wip intake "$tmp/seed.md" --bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "unknown flag -> exit 2"

# --- no provider block -> exit 3 (config error before LLM ever fires) -------
tmp2="$(mktemp -d)"
cat >"$tmp2/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
YAML
cat >"$tmp2/seed.md" <<'MD'
# X
## Goal
Y.
MD
set +e
WIP_ROOT="$tmp2" bin/wip intake "$tmp2/seed.md" --kind brief --yes >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "no provider -> exit 3"
rm -rf "$tmp2"

test_summary
