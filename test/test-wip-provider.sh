#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-provider"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# Fixture repo with an openai-compatible provider.
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

# --- happy path: all env set, api_key non-empty -----------------------------
out="$(WIP_ROOT="$tmp" TEST_BASE_URL="https://api.example/v1" \
  TEST_API_KEY="sk-secret" TEST_MODEL="gpt-foo" \
  bin/wip provider show)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "happy: ok=true"
assert_eq "openai-compatible" "$(jq -r '.kind' <<<"$out")" "happy: kind"
assert_eq "https://api.example/v1" "$(jq -r '.base_url' <<<"$out")" "happy: base_url"
assert_eq "gpt-foo" "$(jq -r '.model' <<<"$out")" "happy: model"
assert_eq "true" "$(jq -r '.api_key_present' <<<"$out")" "happy: api_key_present=true"
assert_eq "TEST_BASE_URL" "$(jq -r '.env.base_url_env' <<<"$out")" "happy: env.base_url_env"
assert_eq "TEST_API_KEY" "$(jq -r '.env.api_key_env' <<<"$out")" "happy: env.api_key_env"
assert_eq "TEST_MODEL" "$(jq -r '.env.model_env' <<<"$out")" "happy: env.model_env"
assert_eq "0.2.0-dev" "$(jq -r '.porcelain_version' <<<"$out")" "happy: porcelain_version"

# api_key value never appears in the output.
if grep -q "sk-secret" <<<"$out"; then
  echo "  FAIL api_key leaked into stdout" >&2
  _WIP_FAIL=$((_WIP_FAIL + 1))
else
  _WIP_PASS=$((_WIP_PASS + 1))
  echo "  ok   api_key not in stdout"
fi

# --- api_key explicitly empty -> api_key_present:false (no auth header path) --
out="$(WIP_ROOT="$tmp" TEST_BASE_URL="http://localhost:8000" \
  TEST_API_KEY="" TEST_MODEL="local-model" \
  bin/wip provider show)"
assert_eq "false" "$(jq -r '.api_key_present' <<<"$out")" "empty key -> present=false"

# --- missing env vars: exit 3 with provider-env-unset + env field -----------
set +e
out="$(WIP_ROOT="$tmp" TEST_API_KEY="sk" TEST_MODEL="m" bin/wip provider show 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "unset base_url env -> exit 3"
assert_eq "provider-env-unset" "$(jq -r '.error.kind' <<<"$out")" "kind=provider-env-unset"
assert_eq "TEST_BASE_URL" "$(jq -r '.error.env' <<<"$out")" "error.env names the missing var"

set +e
out="$(WIP_ROOT="$tmp" TEST_BASE_URL="x" TEST_MODEL="m" bin/wip provider show 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "unset api_key env -> exit 3"
assert_eq "TEST_API_KEY" "$(jq -r '.error.env' <<<"$out")" "error.env names api_key env"

# --- missing provider: block -----------------------------------------------
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
YAML
set +e
out="$(WIP_ROOT="$tmp" bin/wip provider show 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "no provider block -> exit 3"
assert_eq "no-provider" "$(jq -r '.error.kind' <<<"$out")" "kind=no-provider"

# --- unsupported kind ------------------------------------------------------
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
provider:
  kind: anthropic
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML
set +e
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=z bin/wip provider show 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "unsupported kind -> exit 3"
assert_eq "unsupported-provider" "$(jq -r '.error.kind' <<<"$out")" "kind=unsupported-provider"
assert_eq "anthropic" "$(jq -r '.error.provider_kind' <<<"$out")" "error.provider_kind echoes the rejected kind"

# --- bad provider (missing required *_env field) ---------------------------
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
provider:
  kind: openai-compatible
  api_key_env: TEST_API_KEY
  model_env:   TEST_MODEL
YAML
set +e
out="$(WIP_ROOT="$tmp" bin/wip provider show 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "bad provider (no base_url_env) -> exit 3"
assert_eq "bad-provider" "$(jq -r '.error.kind' <<<"$out")" "kind=bad-provider"

# --- --no-json renders prose ------------------------------------------------
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
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=z \
  bin/wip provider show --no-json)"
if grep -q "^kind:" <<<"$out" && grep -q "^api_key_present:" <<<"$out"; then
  _WIP_PASS=$((_WIP_PASS + 1))
  echo "  ok   --no-json prose"
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  printf '  FAIL --no-json prose\n       got: %q\n' "$out" >&2
fi

test_summary
