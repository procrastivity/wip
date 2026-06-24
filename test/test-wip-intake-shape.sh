#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-shape"
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

# Helper: build a single-choice chat-completions response wrapping <body>.
_resp() {
  local body="$1"
  jq -nc --arg c "$body" '{choices:[{message:{role:"assistant",content:$c}}]}'
}

# --- happy path: one good shape -> validate passes -> apply dispatches ------
goodbrief=$'# Payments\n\n## Goal\n\nStand up the payments service.\n'
_resp "$goodbrief" >"$tmp/resp-good.json"

# Input is a half-formed brief missing ## Goal.
cat >"$tmp/half.md" <<'MD'
---
wip-kind: brief
slug: payments
---
# Payments

(narrative without a Goal section)
MD

out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp-good.json" \
  bin/wip intake "$tmp/half.md" --yes 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "happy path ok"
assert_eq "brief" "$(jq -r '.kind' <<<"$out")" "happy path kind"
assert_eq "1" "$(jq -r '.rounds' <<<"$out")" "happy path rounds=1"
assert_eq "init" "$(jq -r '.result.dispatched' <<<"$out")" "dispatched=init"
assert_file "$tmp/.wip/initiatives/payments/BRIEF.md" "BRIEF written"
# After a brief apply, the envelope carries the deterministic next action: the
# fresh roadmap has no steps, so `next` says "author the roadmap" (not /wip:start).
assert_eq "scaffold" "$(jq -r '.next.source' <<<"$out")" "brief apply: next.source scaffold"
assert_eq "author the roadmap" "$(jq -r '.next.title' <<<"$out")" "brief apply: next.title"
assert_eq ".wip/initiatives/payments/roadmap.md" "$(jq -r '.next.path' <<<"$out")" "brief apply: next.path"

# --- shape-retry: first response missing Goal; second is good ---------------
tmp2="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp2/.wip.yaml"
cp "$tmp/half.md" "$tmp2/half.md"

badbrief=$'# Payments\n\n(no goal section)\n'
_resp "$badbrief" >"$tmp2/resp-bad.json"
_resp "$goodbrief" >"$tmp2/resp-good.json"

cat >"$tmp2/mock.sh" <<EOF
#!/usr/bin/env bash
cat >/dev/null
count_file="$tmp2/count"
n=\$(cat "\$count_file" 2>/dev/null || echo 0)
n=\$((n + 1))
printf '%s' "\$n" >"\$count_file"
if [[ "\$n" == "1" ]]; then
  cat "$tmp2/resp-bad.json"
else
  cat "$tmp2/resp-good.json"
fi
EOF
chmod +x "$tmp2/mock.sh"

out="$(WIP_ROOT="$tmp2" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp2/mock.sh" \
  bin/wip intake "$tmp2/half.md" --yes --max-rounds 3 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "retry path ok"
assert_eq "2" "$(jq -r '.rounds' <<<"$out")" "retry path rounds=2"
assert_eq "2" "$(cat "$tmp2/count")" "mock called twice"
rm -rf "$tmp2"

# --- shape-exhausted: every response is broken -> exit 4 shape-failed -------
tmp3="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp3/.wip.yaml"
cp "$tmp/half.md" "$tmp3/half.md"
_resp "$badbrief" >"$tmp3/resp-bad.json"

set +e
out="$(WIP_ROOT="$tmp3" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp3/resp-bad.json" \
  bin/wip intake "$tmp3/half.md" --yes --max-rounds 2 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "exhausted -> exit 4"
assert_eq "shape-failed" "$(jq -r '.error.kind' <<<"$out")" "envelope shape-failed"
assert_eq "2" "$(jq -r '.error.rounds' <<<"$out")" "envelope rounds=2"
# last_body should be the bad brief content
last="$(jq -r '.error.last_body' <<<"$out")"
case "$last" in
  *"Payments"*)
    _WIP_PASS=$((_WIP_PASS + 1))
    echo "  ok   last_body carried"
    ;;
  *)
    _WIP_FAIL=$((_WIP_FAIL + 1))
    echo "  FAIL last_body missing: $last" >&2
    ;;
esac
rm -rf "$tmp3"

# --- ASK path (interactive answer via stdin) --------------------------------
tmp4="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp4/.wip.yaml"
cp "$tmp/half.md" "$tmp4/half.md"

ask_resp=$'---ASK---\nquestion: what is the goal?\nwhy: artifact has no ## Goal section\n---END---\n'
_resp "$ask_resp" >"$tmp4/resp-ask.json"
_resp "$goodbrief" >"$tmp4/resp-good.json"

cat >"$tmp4/mock.sh" <<EOF
#!/usr/bin/env bash
cat >/dev/null
count_file="$tmp4/count"
n=\$(cat "\$count_file" 2>/dev/null || echo 0)
n=\$((n + 1))
printf '%s' "\$n" >"\$count_file"
if [[ "\$n" == "1" ]]; then
  cat "$tmp4/resp-ask.json"
else
  cat "$tmp4/resp-good.json"
fi
EOF
chmod +x "$tmp4/mock.sh"

# The user's answer is read by wip_p_prompt from stdin. Two answers needed:
# the ASK answer, then "y" to confirm the route (interactive, no --yes).
out="$(printf 'stand up payments\ny\n' | WIP_ROOT="$tmp4" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp4/mock.sh" \
  bin/wip intake "$tmp4/half.md" --max-rounds 3 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ASK path ok"
assert_eq "what is the goal?" "$(jq -r '.asked[0]' <<<"$out")" "asked array carried"
rm -rf "$tmp4"

# --- ASK + --yes -> exit 4 ask-without-tty ----------------------------------
tmp5="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp5/.wip.yaml"
cp "$tmp/half.md" "$tmp5/half.md"
_resp "$ask_resp" >"$tmp5/resp-ask.json"

set +e
out="$(WIP_ROOT="$tmp5" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp5/resp-ask.json" \
  bin/wip intake "$tmp5/half.md" --yes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "ASK+--yes -> exit 4"
assert_eq "ask-without-tty" "$(jq -r '.error.kind' <<<"$out")" "envelope ask-without-tty"
assert_eq "what is the goal?" "$(jq -r '.error.question' <<<"$out")" "envelope carries question"
rm -rf "$tmp5"

# --- bad-shape-response: response missing .choices[] -> exit 1 --------------
tmp6="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp6/.wip.yaml"
cp "$tmp/half.md" "$tmp6/half.md"
printf '{"error":"oops"}\n' >"$tmp6/resp-bad.json"

set +e
WIP_ROOT="$tmp6" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp6/resp-bad.json" \
  bin/wip intake "$tmp6/half.md" --yes >/dev/null 2>&1
rc=$?
set -e
assert_eq "1" "$rc" "missing choices -> exit 1"
rm -rf "$tmp6"

# --- conversation shape on retry: round 2 has 4 messages --------------------
tmp7="$(mktemp -d)"
cp "$tmp/.wip.yaml" "$tmp7/.wip.yaml"
cp "$tmp/half.md" "$tmp7/half.md"
_resp "$badbrief" >"$tmp7/resp-bad.json"
_resp "$goodbrief" >"$tmp7/resp-good.json"

cat >"$tmp7/mock.sh" <<EOF
#!/usr/bin/env bash
count_file="$tmp7/count"
n=\$(cat "\$count_file" 2>/dev/null || echo 0)
n=\$((n + 1))
printf '%s' "\$n" >"\$count_file"
cat >"$tmp7/req-\$n.json"
if [[ "\$n" == "1" ]]; then
  cat "$tmp7/resp-bad.json"
else
  cat "$tmp7/resp-good.json"
fi
EOF
chmod +x "$tmp7/mock.sh"

WIP_ROOT="$tmp7" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="$tmp7/mock.sh" \
  bin/wip intake "$tmp7/half.md" --yes --max-rounds 3 >/dev/null 2>/dev/null

req2="$(cat "$tmp7/req-2.json")"
assert_eq "4" "$(jq -r '.messages | length' <<<"$req2")" "round 2 has 4 messages"
assert_eq "system" "$(jq -r '.messages[0].role' <<<"$req2")" "round 2: msg[0]=system"
assert_eq "user" "$(jq -r '.messages[1].role' <<<"$req2")" "round 2: msg[1]=user"
assert_eq "assistant" "$(jq -r '.messages[2].role' <<<"$req2")" "round 2: msg[2]=assistant"
assert_eq "user" "$(jq -r '.messages[3].role' <<<"$req2")" "round 2: msg[3]=user"
# retry user message names missing[]
case "$(jq -r '.messages[3].content' <<<"$req2")" in
  *"goal-or-summary-section"*)
    _WIP_PASS=$((_WIP_PASS + 1))
    echo "  ok   retry message names missing[]"
    ;;
  *)
    _WIP_FAIL=$((_WIP_FAIL + 1))
    echo "  FAIL retry missing fields not echoed" >&2
    ;;
esac
rm -rf "$tmp7"

test_summary
