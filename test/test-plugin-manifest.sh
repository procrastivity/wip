#!/usr/bin/env bash
# test-plugin-manifest — smoke-check the plugin layout (step-11): the
# .claude-plugin/ manifest plus root-level commands/ and agents/ dirs.
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

# Commands: next, status, intake, bundle. Each has a description in
# front-matter, each references at least one `wip-plumbing` shell-out.
for cmd in next status intake bundle start; do
  path="commands/$cmd.md"
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
  'template show intake/preamble' \
  commands/intake.md \
  "intake.md fetches preamble via template verb"
assert_grep \
  'template show intake/<kind>' \
  commands/intake.md \
  "intake.md fetches per-kind rules via template verb"

# /wip:intake must instruct against the ---ASK--- fence (Claude asks inline).
# shellcheck disable=SC2016  # literal text we're grepping for, no expansion intended
assert_grep \
  'do NOT emit `---ASK---`' \
  commands/intake.md \
  "intake.md forbids ASK fence"

# /wip:bundle must reference the bundle assembler's prompt-sharing seam — it
# fetches the assembly rules via `wip-plumbing template show bundle/assemble`,
# and the CLAUDE_PLUGIN_ROOT resolution idiom (copied from intake.md).
assert_grep \
  'template show bundle/assemble' \
  commands/bundle.md \
  "bundle.md fetches assembly rules via template verb"
assert_grep \
  'CLAUDE_PLUGIN_ROOT' \
  commands/bundle.md \
  "bundle.md resolves the bundled wip-plumbing"
# It must also instruct against the ---ASK--- fence (Claude asks inline).
# shellcheck disable=SC2016  # literal text we're grepping for, no expansion intended
assert_grep \
  'do NOT emit `---ASK---`' \
  commands/bundle.md \
  "bundle.md forbids ASK fence"

# /wip:start must resolve the bundled wip-plumbing (CLAUDE_PLUGIN_ROOT idiom),
# drive the deterministic activation (workplan init … --activate), end with the
# literal "Say `go`" offer, and explicitly NOT auto-run.
assert_grep \
  'CLAUDE_PLUGIN_ROOT' \
  commands/start.md \
  "start.md resolves the bundled wip-plumbing"
assert_grep \
  'workplan init <slug> <step-id> --activate' \
  commands/start.md \
  "start.md activates via workplan init --activate"
# shellcheck disable=SC2016  # literal offer line we're grepping for, no expansion intended
assert_grep \
  'Say `go` and I'"'"'ll start working on it.' \
  commands/start.md \
  "start.md ends with the literal go offer"
assert_grep \
  'Do NOT begin editing code until the user says' \
  commands/start.md \
  "start.md does not auto-run"

# agents/ contains the four wip role agent files + README (step-12).
# Detailed agent-file contract (front-matter, @-file pointers,
# backend-agnostic invariant) is pinned in test-roles-backend-seam.sh.
assert_file "agents/README.md" "agents/README.md present"
for a in orchestrator coordinator researcher builder; do
  assert_file "agents/$a.md" "agents/$a.md present"
done
extras="$(find agents -mindepth 1 -not -name README.md -not -name 'orchestrator.md' -not -name 'coordinator.md' -not -name 'researcher.md' -not -name 'builder.md' 2>/dev/null | wc -l | tr -d ' ')"
assert_eq "0" "$extras" "agents/ contains only README + the four role files"

# README mentions all four commands (catches a missed-command-in-docs).
for cmd in next status intake bundle start; do
  assert_grep "/wip:$cmd" .claude-plugin/README.md "README mentions /wip:$cmd"
done

test_summary
