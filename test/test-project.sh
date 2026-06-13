#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
WIP_BIN="$PWD/bin/wip-plumbing"
_WIP_TEST_NAME="project"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/proj-a" "$tmp/proj-b"
cat >"$tmp/proj-a/.wip.yaml" <<'YAML'
version: 1
features: {}
YAML
cat >"$tmp/proj-b/.wip.yaml" <<'YAML'
version: 1
features: {}
YAML

reg="$tmp/state/projects.jsonl"
export WIP_REGISTRY_FILE="$reg"
export XDG_STATE_HOME="$tmp/state"

# register A with slug, B without.
out="$(bin/wip-plumbing project register "$tmp/proj-a" --slug wip)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "register a ok"
assert_eq "wip" "$(jq -r '.record.slug' <<<"$out")" "register a slug"
out="$(bin/wip-plumbing project register "$tmp/proj-b")"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "register b ok"

# list --json
out="$(bin/wip-plumbing project list --json)"
assert_eq "2" "$(printf '%s\n' "$out" | grep -c .)" "list --json: 2 records"

# resolve by slug.
out="$(bin/wip-plumbing project resolve wip)"
assert_eq "$tmp/proj-a" "$(jq -r '.record.path' <<<"$out")" "resolve by slug"

# resolve by segment.
id_a="-$(printf '%s' "$tmp/proj-a" | sed 's|^/||; s|/|-|g')"
out="$(bin/wip-plumbing project resolve "$id_a")"
assert_eq "$tmp/proj-a" "$(jq -r '.record.path' <<<"$out")" "resolve by segment"

# resolve by abs path.
out="$(bin/wip-plumbing project resolve "$tmp/proj-a")"
assert_eq "$tmp/proj-a" "$(jq -r '.record.path' <<<"$out")" "resolve by abs path"

# unknown id -> exit 3.
set +e
bin/wip-plumbing project resolve definitely-not-here >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "resolve unknown -> exit 3"

# ambiguous slug -> exit 4.
bin/wip-plumbing project register "$tmp/proj-b" --slug wip >/dev/null
set +e
bin/wip-plumbing project resolve wip >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "ambiguous slug -> exit 4"

# detect --project <abs-path> from /tmp.
out="$(cd / && "$WIP_BIN" --project "$tmp/proj-a" detect)"
assert_eq "$tmp/proj-a" "$(jq -r '.root' <<<"$out")" "--project abs-path resolves"

# Disambiguate then test --project by slug.
bin/wip-plumbing project forget "$tmp/proj-b" >/dev/null
out="$(cd / && "$WIP_BIN" --project wip detect)"
assert_eq "$tmp/proj-a" "$(jq -r '.root' <<<"$out")" "--project slug resolves"

# detect --project <segment>.
out="$(cd / && "$WIP_BIN" --project "$id_a" detect)"
assert_eq "$tmp/proj-a" "$(jq -r '.root' <<<"$out")" "--project segment resolves"

# forget round-trip.
out="$(bin/wip-plumbing project forget "$id_a")"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "forget ok"
set +e
bin/wip-plumbing project resolve wip >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "after forget: resolve -> exit 3"

# list --prune: register a non-existent path manually.
bin/wip-plumbing project register "$tmp/proj-b" >/dev/null
rm -rf "$tmp/proj-b"
bin/wip-plumbing project list --prune --json >/dev/null
[[ -f "$reg" ]] && count="$(grep -c . "$reg")" || count=0
assert_eq "0" "$count" "prune removes records whose path is gone"

test_summary
