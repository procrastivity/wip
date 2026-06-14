#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-intake-amend"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-13"

# Fixture repo: initiative `foo` with roadmap that has step-01 and step-02.
mkdir -p "$tmp/.wip/initiatives/foo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: foo
initiatives:
  - slug: foo
    title: Foo
    status: in-flight
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML

cat >"$tmp/.wip/initiatives/foo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — One** ✅ — done.
- **step-02 — Two** — current.
MD

# Inbound artifact: a loose plan-file-ish fragment with no front-matter — the
# kind of thing classify would call "low" confidence and the porcelain has to
# either shape into amendment shape or ask. We force --kind amendment to skip
# kind-ambiguity.
cat >"$tmp/loose-plan.md" <<'MD'
# Add step-03 — Three

A new step that extends Round 1 of the foo initiative.

It adds X and Y.
MD

# Mock LLM returns a fully-shaped amendment.
shaped=$'---\ntarget: foo\ninsert-after: step-02\n---\n# Add step-03\n\n### step-03 — Three\n\nThe new step extends Round 1 with X and Y.\n'
resp=$(jq -nc --arg c "$shaped" '{choices:[{message:{role:"assistant",content:$c}}]}')
printf '%s\n' "$resp" >"$tmp/resp.json"

out="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp.json" \
  bin/wip intake "$tmp/loose-plan.md" --kind amendment --yes 2>/dev/null)"

assert_eq "true" "$(jq -r '.ok' <<<"$out")" "amend ok"
assert_eq "amendment" "$(jq -r '.kind' <<<"$out")" "kind=amendment"
assert_eq "foo" "$(jq -r '.target' <<<"$out")" "target=foo"
assert_eq "roadmap amend" "$(jq -r '.result.dispatched' <<<"$out")" "dispatched=roadmap amend"
assert_grep "step-03 — Three" "$tmp/.wip/initiatives/foo/roadmap.md" "roadmap now contains step-03"
assert_grep "<!-- wip-amend: " "$tmp/.wip/initiatives/foo/roadmap.md" "amend marker stamped"

# Re-run the same intake -> idempotent_noop on roadmap.
out2="$(WIP_ROOT="$tmp" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp/resp.json" \
  bin/wip intake "$tmp/loose-plan.md" --kind amendment --yes 2>/dev/null)"
assert_eq "true" "$(jq -r '.result.result.idempotent_noop' <<<"$out2")" "second run idempotent"

# --- --target override is forwarded to plumbing apply -----------------------
tmp2="$(mktemp -d)"
mkdir -p "$tmp2/.wip/initiatives/bar"
cat >"$tmp2/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: bar
initiatives:
  - slug: bar
    title: Bar
    status: in-flight
provider:
  kind: openai-compatible
  base_url_env: TEST_BASE_URL
  api_key_env:  TEST_API_KEY
  model_env:    TEST_MODEL
YAML
cat >"$tmp2/.wip/initiatives/bar/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — Alpha** — current.
MD

# The shape says target: bar already (front-matter), but we test that the
# --target flag passes through. Use a shape whose front-matter target matches
# the CLI flag so plumbing apply accepts it.
shaped2=$'---\ntarget: bar\ninsert-after: step-01\n---\n# Add step-02\n\n### step-02 — Beta\n\nA second step.\n'
resp2=$(jq -nc --arg c "$shaped2" '{choices:[{message:{role:"assistant",content:$c}}]}')
printf '%s\n' "$resp2" >"$tmp2/resp.json"

cat >"$tmp2/loose.md" <<'MD'
# Add a beta step

Loose narrative without front-matter.
MD

out3="$(WIP_ROOT="$tmp2" TEST_BASE_URL=x TEST_API_KEY=y TEST_MODEL=m \
  WIP_PROVIDER_CMD="cat >/dev/null; cat $tmp2/resp.json" \
  bin/wip intake "$tmp2/loose.md" --kind amendment --target bar --yes 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out3")" "--target ok"
assert_eq "bar" "$(jq -r '.target' <<<"$out3")" "--target propagated"
rm -rf "$tmp2"

test_summary
