#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="intake-apply"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-13"

mkdir -p "$tmp/.wip/initiatives/auth"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: auth
initiatives:
  - slug: auth
    title: Auth
    status: in-flight
YAML

run() { WIP_ROOT="$tmp" bin/wip-plumbing intake apply "$@"; }

# 1. apply --kind brief with front-matter slug -> init dispatch.
cat >"$tmp/brief-slug.md" <<'MD'
---
slug: payments
---
# Payments

## Goal

Stand up the payments service.
MD
out="$(run "$tmp/brief-slug.md" --kind brief)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "brief slug ok"
assert_eq "init" "$(jq -r '.dispatched' <<<"$out")" "brief dispatched init"
assert_eq "payments" "$(jq -r '.target' <<<"$out")" "brief target slug"
assert_file "$tmp/.wip/initiatives/payments/BRIEF.md" "payments BRIEF written"

# 2. apply --kind brief deriving slug from H1.
cat >"$tmp/brief-h1.md" <<'MD'
# Auth Rework

## Goal

Refresh tokens.
MD
out="$(run "$tmp/brief-h1.md" --kind brief)"
# "Auth Rework" derives -> "auth-rework" but auth-rework doesn't exist yet.
assert_eq "auth-rework" "$(jq -r '.target' <<<"$out")" "slug derived from H1"

# 3. amendment -> exit 3 not-implemented.
cat >"$tmp/amend.md" <<'MD'
---
target: auth
insert-after: step-02
---
# Step
### step-03 — X
Body.
MD
set +e
out="$(run "$tmp/amend.md" --kind amendment 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "amendment exit 3"
assert_eq "not-implemented" "$(jq -r '.error.kind' <<<"$out")" "amendment not-implemented"

# 4. workplan-seed -> exit 3.
mkdir -p "$tmp/.wip/initiatives/auth"
cat >"$tmp/.wip/initiatives/auth/roadmap.md" <<'MD'
# Roadmap
- **step-01 — One**
MD
cat >"$tmp/wps.md" <<'MD'
---
target: auth/step-01
---
# Seed
Body.
MD
set +e
out="$(run "$tmp/wps.md" --kind workplan-seed 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "workplan-seed exit 3"

# 5. spec -> exit 3.
cat >"$tmp/spec.md" <<'MD'
# Spec
## Summary
Foo.
## User stories
- a
MD
set +e
out="$(run "$tmp/spec.md" --kind spec 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "spec exit 3"

# 6. handoff -> exit 4 not-terminal.
cat >"$tmp/handoff.md" <<'MD'
# Notes
Body.
MD
set +e
out="$(run "$tmp/handoff.md" --kind handoff 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "handoff exit 4"
assert_eq "not-terminal" "$(jq -r '.error.kind' <<<"$out")" "handoff not-terminal"

# 7. missing --kind -> exit 2.
set +e
run "$tmp/brief-slug.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing --kind exit 2"

# 8. shape failure before dispatch -> exit 4 with validate envelope.
cat >"$tmp/bad-brief.md" <<'MD'
not a markdown file with a heading
MD
set +e
out="$(run "$tmp/bad-brief.md" --kind brief 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "bad brief exit 4"
assert_eq "false" "$(jq -r '.valid' <<<"$out")" "bad brief valid=false"

# 9. dry-run with brief -> no writes.
mkdir -p "$tmp-dry"
cp "$tmp/.wip.yaml" "$tmp-dry/.wip.yaml"
cat >"$tmp-dry/new.md" <<'MD'
---
slug: ephemeral
---
# Ephemeral
## Goal
Body.
MD
out="$(WIP_ROOT="$tmp-dry" bin/wip-plumbing --dry-run intake apply "$tmp-dry/new.md" --kind brief)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "dry-run ok"
assert_absent "$tmp-dry/.wip/initiatives/ephemeral/BRIEF.md" "dry-run wrote no BRIEF"
rm -rf "$tmp-dry"

test_summary
