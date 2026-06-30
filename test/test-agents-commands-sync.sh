#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="agents-commands-sync"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# The setup-agents command copies are generated-but-committed from the canonical
# plugin commands/ (ADR-0015). This test is the drift gate: the committed copies
# must equal `contrib/sync-agents-commands` output, exist for every plugin
# command, and never reference the bundled binary path.

# 1. Committed copies are in sync with the generator output.
set +e
contrib/sync-agents-commands --check >/tmp/wip-sync-check.$$ 2>&1
rc=$?
set -e
assert_eq "0" "$rc" "agents commands in sync (run \`make agents-commands\` if this fails)"
[[ "$rc" == "0" ]] || cat /tmp/wip-sync-check.$$ >&2
rm -f /tmp/wip-sync-check.$$

# 2. Every plugin command has a generated copy (set parity, not a subset).
for src in commands/*.md; do
  name="$(basename "$src")"
  assert_file "templates/setup/agents/commands/$name" "agents copy exists for $name"
done

# 3. The consumer tree must not reference the bundled binary (consumers use
# PATH; test-setup.sh enforces the same invariant for the wider tree).
assert_eq "0" \
  "$(grep -rl 'bin/wip-plumbing' templates/setup/agents/commands/ 2>/dev/null | wc -l | tr -d ' ')" \
  "no bin/wip-plumbing refs in generated copies"

# 4. The generated copies carry the PATH resolver, not the $CLAUDE_PLUGIN_ROOT one.
assert_not_grep 'CLAUDE_PLUGIN_ROOT' "templates/setup/agents/commands/next.md" \
  "generated copy uses PATH resolver, not CLAUDE_PLUGIN_ROOT"
assert_grep 'command -v wip-plumbing' "templates/setup/agents/commands/next.md" \
  "generated copy has command -v wip-plumbing resolver"

# 5. Regeneration is idempotent — writing again leaves the tree in sync.
contrib/sync-agents-commands >/dev/null
set +e
contrib/sync-agents-commands --check >/dev/null 2>&1
rc2=$?
set -e
assert_eq "0" "$rc2" "regeneration is idempotent"

# The renderer's INPUT templates under templates/setup/agents/agents/ are
# byte-for-byte copies of the canonical plugin agents/<role>.md (the renderer
# reads them as thin-pointer templates — see wip-plumbing-flatten-lib.bash). They
# are identical today but NO gate keeps them so, so silent drift in the renderer's
# input would otherwise hide. This is the agent-side sibling of the command-copy
# gate above: the two copies must be byte-identical with set parity (every plugin
# role has a template copy and vice-versa — no orphans on either side).

ROLES=(orchestrator coordinator researcher builder)

# 6. Each render-input template is byte-identical to the canonical plugin agent.
for role in "${ROLES[@]}"; do
  assert_cmp "templates/setup/agents/agents/$role.md" "agents/$role.md" \
    "render-input template $role.md matches canonical plugin agents/$role.md"
done

# 7. Set parity — both trees hold exactly the four role files (no orphan on either
#    side: a new plugin role without a template copy, or a stray template without a
#    canonical agent). README.md is documentation, not a render-input role.
expected="$(printf '%s\n' "${ROLES[@]}" | sort | tr '\n' ' ')"
plugin_set="$(printf '%s\n' agents/*.md | xargs -n1 basename | grep -vxF README.md | sort | tr '\n' ' ')"
template_set="$(printf '%s\n' templates/setup/agents/agents/*.md | xargs -n1 basename | grep -vxF README.md | sort | tr '\n' ' ')"
assert_eq "$expected" "${plugin_set//.md/}" "plugin agents/ holds exactly the four roles (no orphan)"
assert_eq "$expected" "${template_set//.md/}" "render-input templates hold exactly the four roles (no orphan)"

test_summary
