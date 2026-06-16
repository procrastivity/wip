#!/usr/bin/env bash
# test-roles-backend-seam — pin ADR-0007's backend-agnostic shape.
#
# Behavior files + tier-policy.md must contain ZERO Solo-specific tool
# names. backends/solo.md must contain them. Plugin agents must be thin
# pointers (@-file references that resolve) and must NOT name Solo tools
# either.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="roles-backend-seam"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# --- Layout --------------------------------------------------------------
for f in shared orchestrator coordinator researcher builder tier-policy; do
  assert_file "roles/$f.md" "roles/$f.md present"
done
assert_file "roles/backends/solo.md" "roles/backends/solo.md present"

# backends/ must contain only solo.md (no unauthored backend files).
extra_backends="$(find roles/backends -mindepth 1 -not -name solo.md 2>/dev/null | wc -l | tr -d ' ')"
assert_eq "0" "$extra_backends" "roles/backends/ only contains solo.md"

# --- Forbidden tokens in behavior + tier-policy --------------------------
# The acceptance shape from roles/README.md:29-30 — a hypothetical
# backends/native.md must require touching ZERO behavior or tier-policy
# files. Mechanically enforced by greping the forbidden Solo-specific
# token set against every behavior file + tier-policy.md.
FORBIDDEN='mcp__solo|solo_process_id|agent_tool_id|spawn_process|scratchpad|todo_create|todo_list|whoami|list_agent_tools|mcp-cli|kv_set|kv_get|timer_set|timer_fire_when_idle|rename_process|wait_for_bound_port|kind="agent"|kind=\\"agent\\"'

BEHAVIOR_FILES=(
  roles/shared.md
  roles/orchestrator.md
  roles/coordinator.md
  roles/researcher.md
  roles/builder.md
  roles/tier-policy.md
)

hits="$(grep -EHn -- "$FORBIDDEN" "${BEHAVIOR_FILES[@]}" 2>/dev/null || true)"
if [[ -z "$hits" ]]; then
  _WIP_PASS=$((_WIP_PASS + 1))
  printf '  ok   behavior + tier-policy files contain ZERO Solo-specific tokens\n'
else
  _WIP_FAIL=$((_WIP_FAIL + 1))
  printf '  FAIL behavior + tier-policy files leak Solo-specific tokens:\n%s\n' "$hits" >&2
fi

# --- Expected tokens in backends/solo.md --------------------------------
# Confirms the extraction landed in the right file rather than being
# silently dropped.
for tok in 'mcp__solo__spawn_process' 'agent_tool_id' 'list_agent_tools' 'whoami' 'timer_' 'mcp-cli'; do
  assert_grep "$tok" "roles/backends/solo.md" "backends/solo.md names $tok"
done

# --- tier-policy.md sanity ----------------------------------------------
# Each Role name appears in a Tier-defaults context.
for role in Orchestrator Coordinator Researcher Builder; do
  assert_grep "$role" "roles/tier-policy.md" "tier-policy.md mentions $role"
done

# --- roles/README.md no longer marks roles/ as not-yet-authored --------
assert_not_grep '🚧 Not yet authored' "roles/README.md" "roles/README.md status flipped from 🚧"

# --- Plugin agent files --------------------------------------------------
PLUGIN_AGENTS=(orchestrator coordinator researcher builder)
for a in "${PLUGIN_AGENTS[@]}"; do
  path="agents/$a.md"
  assert_file "$path" "$path present"
  # Front-matter required fields.
  assert_grep '^name: wip-' "$path" "$a has wip-prefixed name"
  assert_grep '^description:' "$path" "$a has description"
  # Each plugin agent references shared, its own role, tier-policy, and
  # the active backend binding via @-file pointers.
  assert_grep '@../roles/shared.md' "$path" "$a references roles/shared.md"
  assert_grep "@../roles/$a.md" "$path" "$a references roles/$a.md"
  assert_grep '@../roles/tier-policy.md' "$path" "$a references roles/tier-policy.md"
  assert_grep '@../roles/backends/solo.md' "$path" "$a references roles/backends/solo.md"
  # Plugin agent body must NOT name Solo MCP tools — same forbidden set
  # as the behavior files (plugin agents are thin pointers).
  bad="$(grep -En -- "$FORBIDDEN" "$path" 2>/dev/null | grep -v '^[0-9]*:.*roles/backends/solo.md' || true)"
  if [[ -z "$bad" ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s contains no inline Solo-specific tokens\n' "$a"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s inlines Solo-specific tokens:\n%s\n' "$a" "$bad" >&2
  fi
  # Every @-file reference in the plugin agent body resolves to an
  # existing file (relative to the plugin agent file's directory).
  agent_dir="$(dirname "$path")"
  unresolved=""
  while IFS= read -r ref; do
    [[ -z "$ref" ]] && continue
    target="$agent_dir/$ref"
    if [[ ! -e "$target" ]]; then
      unresolved+="${ref} -> ${target}"$'\n'
    fi
  done < <(grep -oE '@[^[:space:]]+' "$path" | sed 's/^@//')
  if [[ -z "$unresolved" ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s @-file references all resolve\n' "$a"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s has unresolved @-file references:\n%s\n' "$a" "$unresolved" >&2
  fi
done

# Plugin agents README mentions every agent.
for a in "${PLUGIN_AGENTS[@]}"; do
  assert_grep "wip-$a" "agents/README.md" "agents/README mentions wip-$a"
done

test_summary
