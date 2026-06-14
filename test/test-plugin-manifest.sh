#!/usr/bin/env bash
# test-plugin-manifest — smoke-check the .claude-plugin/ layout (step-11).
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="plugin-manifest"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# plugin.json: valid JSON, name=wip, version present.
manifest=".claude-plugin/plugin.json"
assert_file "$manifest" "plugin.json present"
if jq empty <"$manifest" >/dev/null 2>&1; then
  _WIP_PASS=$((_WIP_PASS + 1))
  printf '  ok   %s\n' "plugin.json is valid JSON"
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  printf '  FAIL %s\n' "plugin.json is not valid JSON" >&2
fi
assert_eq "wip" "$(jq -r '.name' <"$manifest")" "plugin.name == wip"
ver="$(jq -r '.version' <"$manifest")"
case "$ver" in
  null | "")
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL plugin.json missing version\n' >&2
    ;;
  *)
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   plugin.version present (%s)\n' "$ver"
    ;;
esac

# Commands: next, status, intake. Each has a description in front-matter,
# each references at least one `wip-plumbing` shell-out.
for cmd in next status intake; do
  path=".claude-plugin/commands/$cmd.md"
  assert_file "$path" "commands/$cmd.md present"
  if head -10 "$path" | grep -qE '^description:'; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s frontmatter has description\n' "$cmd"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s missing description\n' "$cmd" >&2
  fi
  if grep -q 'wip-plumbing' "$path"; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s shells out to wip-plumbing\n' "$cmd"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s does not reference wip-plumbing\n' "$cmd" >&2
  fi
done

# /wip:intake must explicitly reference the prompt-sharing seam — i.e.
# fetch its shape rules via `wip-plumbing template show intake/...`. This
# catches accidental detachment of the plugin from the canonical prompts.
assert_grep \
  'wip-plumbing template show intake/preamble' \
  .claude-plugin/commands/intake.md \
  "intake.md fetches preamble via template verb"
assert_grep \
  'wip-plumbing template show intake/<kind>' \
  .claude-plugin/commands/intake.md \
  "intake.md fetches per-kind rules via template verb"

# /wip:intake must instruct against the ---ASK--- fence (Claude asks inline).
# shellcheck disable=SC2016  # literal text we're grepping for, no expansion intended
assert_grep \
  'do NOT emit `---ASK---`' \
  .claude-plugin/commands/intake.md \
  "intake.md forbids ASK fence"

# agents/ stub must be README-only (no role files yet — those land step-12).
assert_file ".claude-plugin/agents/README.md" "agents/README.md stub"
extras="$(find .claude-plugin/agents -mindepth 1 -not -name README.md 2>/dev/null | wc -l | tr -d ' ')"
assert_eq "0" "$extras" "agents/ only contains README (step-12 lands files)"

# README mentions all three commands (catches a missed-command-in-docs).
for cmd in next status intake; do
  assert_grep "/wip:$cmd" .claude-plugin/README.md "README mentions /wip:$cmd"
done

test_summary
