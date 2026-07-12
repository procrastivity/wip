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
# The shaped body must be persisted — not the empty template skeleton.
brief="$tmp/.wip/initiatives/payments/BRIEF.md"
assert_grep "Stand up the payments service." "$brief" "brief body persisted (shaped Goal)"
assert_grep "^# Payments — BRIEF" "$brief" "brief standard header (decorated H1)"
assert_grep "Slug: \`payments\`" "$brief" "brief header carries Slug"
assert_not_grep "_decision 1_" "$brief" "brief has no template placeholder stub"
# Persisted brief round-trips back through the brief validator.
assert_eq "true" "$(jq -r '.valid' <<<"$(WIP_ROOT="$tmp" bin/wip-plumbing intake validate "$brief" --kind brief)")" "persisted brief re-validates"

# 2. apply --kind brief deriving slug from H1.
cat >"$tmp/brief-h1.md" <<'MD'
# Auth Rework

## Goal

Refresh tokens.
MD
out="$(run "$tmp/brief-h1.md" --kind brief)"
# "Auth Rework" derives -> "auth-rework" but auth-rework doesn't exist yet.
assert_eq "auth-rework" "$(jq -r '.target' <<<"$out")" "slug derived from H1"
assert_grep "Refresh tokens." "$tmp/.wip/initiatives/auth-rework/BRIEF.md" "H1-derived brief body persisted"

# 2b. a title containing & round-trips into the persisted BRIEF.md header
#     (depends on the wip_scaffold_render escaping fix — no {{title}} leak).
cat >"$tmp/brief-amp.md" <<'MD'
---
slug: tls-trust
---
# TLS proxy-domain wildcard & local trust

## Goal

Document the wildcard behavior & the local trust store.
MD
out="$(run "$tmp/brief-amp.md" --kind brief)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ampersand brief ok"
ampbrief="$tmp/.wip/initiatives/tls-trust/BRIEF.md"
assert_grep "^# TLS proxy-domain wildcard & local trust — BRIEF" "$ampbrief" "ampersand title verbatim in header"
assert_grep "the wildcard behavior & the local trust store." "$ampbrief" "ampersand body verbatim"
assert_not_grep "{{title}}" "$ampbrief" "no placeholder leak with ampersand title"

# 3. amendment -> dispatches through roadmap amend.
mkdir -p "$tmp/.wip/initiatives/auth"
cat >"$tmp/.wip/initiatives/auth/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — One** ✅ — done.
- **step-02 — Two** — current.
MD
cat >"$tmp/amend.md" <<'MD'
---
target: auth
insert-after: step-02
---
# Step

### step-03 — Three

A new step.
MD
out="$(run "$tmp/amend.md" --kind amendment)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "amendment ok"
assert_eq "roadmap amend" "$(jq -r '.dispatched' <<<"$out")" "amendment dispatched"
assert_eq "auth" "$(jq -r '.target' <<<"$out")" "amendment target"
assert_grep "step-03 — Three" "$tmp/.wip/initiatives/auth/roadmap.md" "amendment wrote roadmap"

# 4. workplan-seed -> dispatches through workplan init.
cat >"$tmp/wps.md" <<'MD'
---
target: auth/step-02
---
# Seed for step-02

Seed body.
MD
out="$(run "$tmp/wps.md" --kind workplan-seed)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "workplan-seed ok"
assert_eq "workplan init" "$(jq -r '.dispatched' <<<"$out")" "workplan-seed dispatched"
assert_eq "auth/step-02" "$(jq -r '.target' <<<"$out")" "workplan-seed target"
assert_file "$tmp/.wip/initiatives/auth/workplans/step-02-two.md" "workplan written"
assert_grep "## Seed (from intake)" "$tmp/.wip/initiatives/auth/workplans/step-02-two.md" "seed appended"

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

# 6b. bundle -> exit 4 not-terminal (explode happens at the porcelain layer).
cat >"$tmp/childA.md" <<'MD'
# Track A
Body.
MD
cat >"$tmp/bundle.md" <<'MD'
---
wip-kind: bundle
lead-as: brief
children:
  - path: childA.md
---
# New thing
## Goal
Do it.
MD
set +e
out="$(run "$tmp/bundle.md" --kind bundle 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "bundle exit 4"
assert_eq "not-terminal" "$(jq -r '.error.kind' <<<"$out")" "bundle not-terminal"

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

# 10. brief apply --anchor flag persists tracker_anchor on the record (ADR-0024).
cat >"$tmp/anchor-flag.md" <<'MD'
---
slug: anchored-flag
---
# Anchored Flag

## Goal

Persist an anchor via the CLI flag.
MD
out="$(run "$tmp/anchor-flag.md" --kind brief --anchor BDS-56)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "anchor-flag ok"
assert_eq "BDS-56" "$(yq -r '.initiatives[] | select(.slug=="anchored-flag") | .tracker_anchor' "$tmp/.wip.yaml")" "anchor flag persisted"

# 11. shaped front-matter tracker-anchor: key is honored when no flag is passed.
cat >"$tmp/anchor-fm.md" <<'MD'
---
slug: anchored-fm
tracker-anchor: BDS-77
---
# Anchored FM

## Goal

Persist an anchor via front-matter.
MD
out="$(run "$tmp/anchor-fm.md" --kind brief)"
assert_eq "BDS-77" "$(yq -r '.initiatives[] | select(.slug=="anchored-fm") | .tracker_anchor' "$tmp/.wip.yaml")" "front-matter anchor persisted"

# 12. flag wins over front-matter.
cat >"$tmp/anchor-both.md" <<'MD'
---
slug: anchored-both
tracker-anchor: BDS-11
---
# Anchored Both

## Goal

Flag should win.
MD
out="$(run "$tmp/anchor-both.md" --kind brief --anchor BDS-22)"
assert_eq "BDS-22" "$(yq -r '.initiatives[] | select(.slug=="anchored-both") | .tracker_anchor' "$tmp/.wip.yaml")" "flag wins over front-matter"

# 13. neither flag nor front-matter -> no tracker_anchor field (back-compat); a
#     brief that omits the key still validates and applies.
cat >"$tmp/anchor-none.md" <<'MD'
---
slug: anchored-none
---
# Anchored None

## Goal

No anchor at all.
MD
out="$(run "$tmp/anchor-none.md" --kind brief)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "no-anchor brief still applies"
assert_eq "false" "$(yq -o=json '.initiatives[] | select(.slug=="anchored-none") | has("tracker_anchor")' "$tmp/.wip.yaml")" "no anchor -> no field"

# 14. a malformed front-matter anchor fails at apply (init is the single gate).
cat >"$tmp/anchor-bad.md" <<'MD'
---
slug: anchored-bad
tracker-anchor: not-an-id
---
# Anchored Bad

## Goal

Bad anchor shape.
MD
set +e
run "$tmp/anchor-bad.md" --kind brief >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "malformed front-matter anchor exit 2"

# 15. step-02 pin 4: `intake apply --kind amendment --insert-after` against an
#     anchor that resolves only inside an HTML comment span must emit the JSON
#     error envelope on *stdout*, not exit silently. The dispatched `roadmap
#     amend` runs inside a command substitution here, so its wip_die envelope is
#     captured by the subshell — intake must forward it to the real caller.
cat >>"$tmp/.wip/initiatives/auth/roadmap.md" <<'MD'

<!--
- **step-09 — Ghost** — commented-out scaffold, no reader ever sees this.
-->
MD
cat >"$tmp/amend-shadowed.md" <<'MD'
---
target: auth
insert-after: step-09
---
# Step

### step-10 — Ten

Must never land inside the comment.
MD
set +e
out="$(run "$tmp/amend-shadowed.md" --kind amendment 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "step-02 pin 4: comment-shadowed anchor exits 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "step-02 pin 4: envelope ok=false on stdout"
assert_eq "4" "$(jq -r '.error.code' <<<"$out")" "step-02 pin 4: envelope error.code"
assert_eq "step-shadowed-in-comment" "$(jq -r '.error.kind' <<<"$out")" "step-02 pin 4: envelope error.kind"
assert_eq "true" "$(jq -r '(.error.message // "") != ""' <<<"$out")" "step-02 pin 4: envelope error.message non-empty"
assert_not_grep "step-10 — Ten" "$tmp/.wip/initiatives/auth/roadmap.md" "step-02 pin 4: nothing written into the comment"

# 15b. the same forwarding for an anchor that is absent outright (not shadowed):
#      still an envelope on stdout, with the not-found kind.
cat >"$tmp/amend-missing.md" <<'MD'
---
target: auth
insert-after: step-99
---
# Step

### step-11 — Eleven

Anchor does not exist at all.
MD
set +e
out="$(run "$tmp/amend-missing.md" --kind amendment 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "step-02 pin 4b: unresolvable anchor exits 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "step-02 pin 4b: envelope ok=false on stdout"
assert_eq "step-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out")" "step-02 pin 4b: envelope error.kind"

# 16. twin check: the same swallowed-envelope shape existed in every
#     `_wip_intake_apply_*` dispatcher. A failing `init` dispatch (malformed
#     tracker anchor, cf. case 14) and a failing `workplan init` dispatch must
#     both forward their envelope to stdout too, not just a bare exit code.
set +e
out="$(run "$tmp/anchor-bad.md" --kind brief 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "step-02 pin 4c: brief twin exits 2"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "step-02 pin 4c: brief twin forwards envelope on stdout"
assert_eq "2" "$(jq -r '.error.code' <<<"$out")" "step-02 pin 4c: brief twin envelope error.code"

# Re-applying case 4's seed reaches the `workplan init` dispatch (intake's own
# validate gate passes — auth/step-02 is a real step) and fails there with
# file-exists. A target like auth/step-99 would NOT exercise this: intake's
# validate gate rejects it before the dispatcher ever runs.
set +e
out="$(run "$tmp/wps.md" --kind workplan-seed 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "step-02 pin 4d: workplan-seed twin exits 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "step-02 pin 4d: workplan-seed twin forwards envelope on stdout"
assert_eq "file-exists" "$(jq -r '.error.kind' <<<"$out")" "step-02 pin 4d: workplan-seed twin envelope error.kind"

test_summary
