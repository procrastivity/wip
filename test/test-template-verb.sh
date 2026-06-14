#!/usr/bin/env bash
# test-template-verb — `wip-plumbing template show|list` (step-11).
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="template-verb"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# `show` happy path — emits the file bytes verbatim (no envelope).
out="$(bin/wip-plumbing template show intake/preamble)"
expected="$(cat templates/prompts/intake/preamble.md)"
assert_eq "$expected" "$out" "show intake/preamble bytes"

# Byte-identical (md5) is the equivalence acceptance criterion.
file_md5="$(md5sum templates/prompts/intake/preamble.md | awk '{print $1}')"
verb_md5="$(bin/wip-plumbing template show intake/preamble | md5sum | awk '{print $1}')"
assert_eq "$file_md5" "$verb_md5" "show byte-equiv md5"

# `list` (JSON, default) — returns an array of {id, path} sorted by id.
list_json="$(bin/wip-plumbing template list)"
assert_eq "true" "$(jq -r '.ok' <<<"$list_json")" "list ok"
ids="$(jq -r '.templates[].id' <<<"$list_json" | tr '\n' ',' | sed 's/,$//')"
assert_eq \
  "intake/amendment,intake/brief,intake/handoff,intake/preamble,intake/spec,intake/workplan-seed" \
  "$ids" "list ids sorted"

# `list --no-json` — TSV-style fallback.
list_tsv="$(bin/wip-plumbing --no-json template list)"
first="$(printf '%s\n' "$list_tsv" | head -1 | awk -F'\t' '{print $1}')"
assert_eq "intake/amendment" "$first" "list --no-json id col"

# `show` with no arg → exit 2 (usage).
set +e
out="$(bin/wip-plumbing template show 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "show without id exit 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "show without id kind"

# `show` unknown id → exit 4 (unknown-template).
set +e
out="$(bin/wip-plumbing template show intake/bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "show unknown id exit 4"
assert_eq "unknown-template" "$(jq -r '.error.kind' <<<"$out")" "show unknown id kind"
assert_eq "no template at id intake/bogus" "$(jq -r '.error.message' <<<"$out")" "unknown id msg"

# `show` rejects path traversal.
for bad in /etc/passwd intake/../passwd ../foo; do
  set +e
  out="$(bin/wip-plumbing template show "$bad" 2>/dev/null)"
  rc=$?
  set -e
  assert_eq "2" "$rc" "show $bad rejected as usage"
done

# Bogus subcommand → exit 2.
set +e
out="$(bin/wip-plumbing template bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "bogus subcommand exit 2"

# `template` with no subcommand → exit 2.
set +e
out="$(bin/wip-plumbing template 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "template without sub exit 2"

# WIP_TEMPLATES_DIR override resolves a custom dir.
tmp="$(mktemp -d)"
mkdir -p "$tmp/prompts/intake"
printf 'OVERRIDE\n' >"$tmp/prompts/intake/preamble.md"
got="$(WIP_TEMPLATES_DIR="$tmp" bin/wip-plumbing template show intake/preamble)"
assert_eq "OVERRIDE" "$got" "WIP_TEMPLATES_DIR override honored"
rm -rf "$tmp"

test_summary
