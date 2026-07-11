#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="init"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Keep the global registry off-disk for the duration of the test.
export WIP_NO_REGISTRY=1
export WIP_NOW="2026-06-13"

# 1. repo-level scaffold on an empty dir.
mkdir -p "$tmp/a"
out="$(WIP_ROOT="$tmp/a" bin/wip-plumbing init)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "repo-level ok"
assert_eq "null" "$(jq -r '.slug' <<<"$out")" "repo-level slug null"
assert_eq ".wip.yaml" "$(jq -r '.manifest_updated' <<<"$out")" "repo-level manifest_updated"
assert_eq "3" "$(jq -r '.wrote|length' <<<"$out")" "repo-level wrote 3 files"
assert_file "$tmp/a/.wip.yaml" ".wip.yaml present"
assert_file "$tmp/a/.wip/GLOSSARY.md" "GLOSSARY present"
assert_file "$tmp/a/.wip/backlog.md" "backlog present"

# 2. second run is idempotent — everything skipped.
out2="$(WIP_ROOT="$tmp/a" bin/wip-plumbing init)"
assert_eq "true" "$(jq -r '.ok' <<<"$out2")" "second run ok"
assert_eq "0" "$(jq -r '.wrote|length' <<<"$out2")" "second run wrote 0"
assert_eq "3" "$(jq -r '.skipped_protected|length' <<<"$out2")" "second run skipped 3"

# 3. initiative scaffold sets current_initiative when first.
mkdir -p "$tmp/b"
out3="$(WIP_ROOT="$tmp/b" bin/wip-plumbing init auth-rework --title "Auth Rework")"
assert_eq "auth-rework" "$(jq -r '.slug' <<<"$out3")" "init slug echo"
assert_file "$tmp/b/.wip/initiatives/auth-rework/BRIEF.md" "BRIEF written"
assert_file "$tmp/b/.wip/initiatives/auth-rework/roadmap.md" "roadmap written"
assert_eq "auth-rework" "$(yq -r '.current_initiative' "$tmp/b/.wip.yaml")" "current_initiative set"
assert_eq "auth-rework" "$(yq -r '.initiatives[0].slug' "$tmp/b/.wip.yaml")" "manifest initiative slug"
assert_eq "Auth Rework" "$(yq -r '.initiatives[0].title' "$tmp/b/.wip.yaml")" "manifest initiative title"

# 4. second init of same slug exits 4.
set +e
out4="$(WIP_ROOT="$tmp/b" bin/wip-plumbing init auth-rework 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "duplicate slug exit 4"
assert_eq "slug-exists" "$(jq -r '.error.kind' <<<"$out4")" "duplicate slug kind"

# 5. bad slug exits 2.
mkdir -p "$tmp/c"
for bad in "Auth" "-foo" "foo_bar"; do
  set +e
  WIP_ROOT="$tmp/c" bin/wip-plumbing init "$bad" >/dev/null 2>&1
  rc=$?
  set -e
  assert_eq "2" "$rc" "bad slug '$bad' exit 2"
done

# 6. --dry-run does not touch disk.
mkdir -p "$tmp/d"
out6="$(WIP_ROOT="$tmp/d" bin/wip-plumbing --dry-run init demo)"
assert_eq "true" "$(jq -r '.ok' <<<"$out6")" "dry-run ok"
assert_eq "demo" "$(jq -r '.slug' <<<"$out6")" "dry-run slug echo"
assert_absent "$tmp/d/.wip.yaml" "dry-run wrote nothing"

# 7. humanized title default.
mkdir -p "$tmp/e"
WIP_ROOT="$tmp/e" bin/wip-plumbing init foo-bar-baz >/dev/null
assert_eq "Foo Bar Baz" "$(yq -r '.initiatives[0].title' "$tmp/e/.wip.yaml")" "humanized title"

# 8. second initiative in existing manifest does not overwrite current_initiative.
WIP_ROOT="$tmp/e" bin/wip-plumbing init second-thing >/dev/null
assert_eq "foo-bar-baz" "$(yq -r '.current_initiative' "$tmp/e/.wip.yaml")" "current_initiative preserved"
assert_eq "2" "$(yq -r '.initiatives | length' "$tmp/e/.wip.yaml")" "two initiatives"

# 9. a title containing & renders verbatim into BRIEF.md (regression for the
#    wip_scaffold_render sed-replacement escaping bug).
mkdir -p "$tmp/f"
WIP_ROOT="$tmp/f" bin/wip-plumbing init xcind-tls --title "X & Y" >/dev/null
brief="$tmp/f/.wip/initiatives/xcind-tls/BRIEF.md"
assert_grep "X & Y" "$brief" "ampersand title verbatim in BRIEF.md"
assert_not_grep "{{title}}" "$brief" "no placeholder leak in BRIEF.md"
assert_eq "X & Y" "$(yq -r '.initiatives[] | select(.slug=="xcind-tls") | .title' "$tmp/f/.wip.yaml")" "manifest title verbatim"

# 10. --brief-body splices a shaped body beneath the standard header.
mkdir -p "$tmp/g"
cat >"$tmp/g-body.md" <<'MD'
---
slug: ignored-here
---
# Shaped Title

## Goal

The shaped goal text.

## Constraints

- Ship by Friday.
MD
WIP_ROOT="$tmp/g" bin/wip-plumbing init shaped --title "Shaped Title" --brief-body "$tmp/g-body.md" >/dev/null
gbrief="$tmp/g/.wip/initiatives/shaped/BRIEF.md"
assert_grep "^# Shaped Title — BRIEF" "$gbrief" "brief-body keeps decorated header"
assert_grep "Slug: \`shaped\`" "$gbrief" "brief-body keeps Slug line"
assert_grep "The shaped goal text." "$gbrief" "brief-body persists shaped Goal"
assert_grep "Ship by Friday." "$gbrief" "brief-body persists shaped Constraints"
assert_not_grep "_decision 1_" "$gbrief" "brief-body drops template stub"
assert_not_grep "^# Shaped Title$" "$gbrief" "brief-body drops shaped raw H1"

# 11. --brief-body requires a slug (repo-level use is an error).
set +e
WIP_ROOT="$tmp/g" bin/wip-plumbing init --brief-body "$tmp/g-body.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "--brief-body without slug exit 2"

# 12. --brief-body with an unreadable file exits 2.
set +e
WIP_ROOT="$tmp/g" bin/wip-plumbing init other --brief-body "$tmp/nope.md" >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "--brief-body missing file exit 2"

# 13. --tracker-anchor persists a top-level tracker_anchor on the record
#     (ADR-0024 / D3); the field is a sibling of tracker_map, never inside it.
mkdir -p "$tmp/h"
WIP_ROOT="$tmp/h" bin/wip-plumbing init anchored --title Anchored --tracker-anchor BDS-56 >/dev/null
assert_eq "BDS-56" "$(yq -r '.initiatives[] | select(.slug=="anchored") | .tracker_anchor' "$tmp/h/.wip.yaml")" "tracker_anchor persisted"
assert_eq "null" "$(yq -o=json '.initiatives[] | select(.slug=="anchored") | .tracker_map' "$tmp/h/.wip.yaml")" "anchor is NOT inside tracker_map"

# 13b. ADR-0026: the anchor validator accepts github/gitlab issue refs, not just
#      Linear keys. Bare `#N`, qualified `owner/repo#N`, and nested
#      `grp/sub/proj#N` all persist and round-trip (the `#` is YAML-quoted by the
#      writer so it is not read back as a comment).
gh_i=0
for good in "#123" "octocat/hello#123" "grp/sub/proj#45"; do
  slug="anch-$gh_i"
  WIP_ROOT="$tmp/h" bin/wip-plumbing init "$slug" --title A --tracker-anchor "$good" >/dev/null
  assert_eq "$good" "$(yq -r ".initiatives[] | select(.slug==\"$slug\") | .tracker_anchor" "$tmp/h/.wip.yaml")" "anchor '$good' persisted"
  gh_i=$((gh_i + 1))
done

# 14. no --tracker-anchor -> field absent (back-compat).
WIP_ROOT="$tmp/h" bin/wip-plumbing init plain --title Plain >/dev/null
assert_eq "false" "$(yq -o=json '.initiatives[] | select(.slug=="plain") | has("tracker_anchor")' "$tmp/h/.wip.yaml")" "no anchor -> no field"

# 15. malformed anchor shape exits 2 (validated against the ADR-0026 union:
#     a Linear key OR a `#N` / `owner/repo#N` ref). A bare `#` with no digits,
#     an `owner/repo` with no `#N`, and a lowercase key all still reject.
for bad in "bds-56" "BDS56" "BDS-" "not-an-id" "#" "owner/repo" "#abc"; do
  set +e
  WIP_ROOT="$tmp/h" bin/wip-plumbing init "bad-$RANDOM" --tracker-anchor "$bad" >/dev/null 2>&1
  rc=$?
  set -e
  assert_eq "2" "$rc" "bad anchor '$bad' exit 2"
done

# 16. --tracker-anchor without a slug (repo-level) exits 2.
mkdir -p "$tmp/i"
set +e
WIP_ROOT="$tmp/i" bin/wip-plumbing init --tracker-anchor BDS-1 >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "--tracker-anchor without slug exit 2"

# 17. Initiative START emission (ADR-0024 / D1–D2): with issue-tracker enabled AND
#     an anchor, init seeds a `<slug>/initiative` in-progress cache entry and
#     echoes the intent (parity with `workplan init --activate`).
mkdir -p "$tmp/j/.wip"
cat >"$tmp/j/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  issue-tracker: { enabled: true, backend: linear }
YAML
out17="$(WIP_ROOT="$tmp/j" bin/wip-plumbing init tracked --title Tracked --tracker-anchor BDS-56)"
assert_eq "tracked/initiative" "$(jq -r '.intent.node' <<<"$out17")" "init emits initiative intent node"
assert_eq "in-progress" "$(jq -r '.intent.to' <<<"$out17")" "init intent to=in-progress"
assert_eq "start" "$(jq -r '.intent.reason' <<<"$out17")" "init intent reason=start"
cache="$tmp/j/.wip/tracker-cache.json"
assert_file "$cache" "tracker cache written"
assert_eq "in-progress" "$(jq -r '.["tracked/initiative"].state' "$cache")" "cache seeds initiative in-progress"
assert_eq "start" "$(jq -r '.["tracked/initiative"].reason' "$cache")" "cache reason=start"

# 18. Emission gate: anchor present but issue-tracker DISABLED -> no intent, no cache.
mkdir -p "$tmp/k/.wip"
cat >"$tmp/k/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
YAML
out18="$(WIP_ROOT="$tmp/k" bin/wip-plumbing init untracked --tracker-anchor BDS-56)"
assert_eq "false" "$(jq -r 'has("intent")' <<<"$out18")" "issue-tracker disabled -> no intent"
assert_absent "$tmp/k/.wip/tracker-cache.json" "issue-tracker disabled -> no cache"

# 19. Emission gate: issue-tracker enabled but NO anchor -> no intent.
out19="$(WIP_ROOT="$tmp/j" bin/wip-plumbing init noanchor)"
assert_eq "false" "$(jq -r 'has("intent")' <<<"$out19")" "no anchor -> no intent"

# 20. Dry-run parity: the intent shape is emitted but the cache is never written.
mkdir -p "$tmp/l/.wip"
cat >"$tmp/l/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  issue-tracker: { enabled: true, backend: linear }
YAML
out20="$(WIP_ROOT="$tmp/l" bin/wip-plumbing --dry-run init dryinit --tracker-anchor BDS-77)"
assert_eq "dryinit/initiative" "$(jq -r '.intent.node' <<<"$out20")" "dry-run emits intent node"
assert_eq "in-progress" "$(jq -r '.intent.to' <<<"$out20")" "dry-run intent to=in-progress"
assert_eq "start" "$(jq -r '.intent.reason' <<<"$out20")" "dry-run intent reason=start"
assert_absent "$tmp/l/.wip/tracker-cache.json" "dry-run writes no cache"

test_summary
