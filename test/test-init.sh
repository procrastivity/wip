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

test_summary
