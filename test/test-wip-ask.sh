#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-ask"
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

# Canned good response.
cat >"$tmp/resp-ok.json" <<'JSON'
{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hello back"},"finish_reason":"stop"}]}
JSON

# --- happy path: prompt arg --------------------------------------------------
out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-ok.json" \
  bin/wip ask "hello there")"
assert_eq "hello back" "$out" "arg prompt -> assistant text"

# --- request shape: model + single user message ------------------------------
cat >"$tmp/req-capture.sh" <<EOF
#!/usr/bin/env bash
cat >"$tmp/captured.json"
cat "$tmp/resp-ok.json"
EOF
chmod +x "$tmp/req-capture.sh"

WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=mock-model \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask "what is your name?" >/dev/null

req="$(cat "$tmp/captured.json")"
assert_eq "mock-model" "$(jq -r '.model' <<<"$req")" "request: model"
assert_eq "1" "$(jq -r '.messages | length' <<<"$req")" "request: one message"
assert_eq "user" "$(jq -r '.messages[0].role' <<<"$req")" "request: user role"
assert_eq "what is your name?" "$(jq -r '.messages[0].content' <<<"$req")" "request: content"

# --- system prompt adds a system message before user -------------------------
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask --system "be terse" "expand this" >/dev/null
req="$(cat "$tmp/captured.json")"
assert_eq "2" "$(jq -r '.messages | length' <<<"$req")" "system: two messages"
assert_eq "system" "$(jq -r '.messages[0].role' <<<"$req")" "system: first is system"
assert_eq "be terse" "$(jq -r '.messages[0].content' <<<"$req")" "system: content"
assert_eq "user" "$(jq -r '.messages[1].role' <<<"$req")" "system: second is user"

# --- stdin path: prompt comes from pipe --------------------------------------
out="$(printf 'piped prompt' | WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask)"
req="$(cat "$tmp/captured.json")"
assert_eq "piped prompt" "$(jq -r '.messages[0].content' <<<"$req")" "stdin: prompt content"
assert_eq "hello back" "$out" "stdin: stdout text"

# --- '-' forces stdin even when stdin is a tty-style fixture -----------------
out="$(printf 'explicit-stdin' | WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask -)"
req="$(cat "$tmp/captured.json")"
assert_eq "explicit-stdin" "$(jq -r '.messages[0].content' <<<"$req")" "'-': prompt from stdin"

# --- arg beats stdin (stdin silently dropped) --------------------------------
out="$(printf 'IGNORED' | WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask "from arg")"
req="$(cat "$tmp/captured.json")"
assert_eq "from arg" "$(jq -r '.messages[0].content' <<<"$req")" "arg beats stdin"

# --- ask - "foo" -> exit 2 (ambiguous) ---------------------------------------
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp/req-capture.sh" \
  bin/wip ask - "foo" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "'- + positional' -> exit 2"

# --- response missing choices[0].message.content -> exit 1 -------------------
cat >"$tmp/resp-bad.json" <<'JSON'
{"error": {"message": "rate limited"}}
JSON
set +e
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-bad.json" \
  bin/wip ask "x" >/dev/null 2>&1
rc=$?
set -e
assert_eq "1" "$rc" "bad response shape -> exit 1"

# --- no provider block -> exit 3 ---------------------------------------------
tmp2="$(mktemp -d)"
cat >"$tmp2/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
YAML
set +e
WIP_ROOT="$tmp2" bin/wip ask "x" >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "no provider -> exit 3"
rm -rf "$tmp2"

# --- api_key never leaks into stderr under -v --------------------------------
cap="$(mktemp)"
WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY="sk-LEAK-XYZ" TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-ok.json" \
  bin/wip -v ask "x" >/dev/null 2>"$cap"
if grep -q "sk-LEAK-XYZ" "$cap"; then
  _WIP_FAIL=$((_WIP_FAIL + 1))
  echo "  FAIL api_key leaked into stderr under -v" >&2
else
  _WIP_PASS=$((_WIP_PASS + 1))
  echo "  ok   api_key not in stderr"
fi
rm -f "$cap"

test_summary
