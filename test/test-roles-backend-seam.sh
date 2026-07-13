#!/usr/bin/env bash
# test-roles-backend-seam — pin ADR-0007's backend-agnostic shape.
#
# Behavior files + tier-policy.md must contain ZERO Solo-specific tool
# names. backends/solo.md must contain them. Plugin agents must be thin
# pointers (@-file references that resolve) and must NOT name Solo tools
# either.
#
# The shared contract is role-centric (ADR-0025): a spawn requests by
# Role, not by a small/medium/large tier. tier-policy.md is retained as
# the stable @-include path but now holds the per-Role runtime policy.
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

# backends/ holds the authored bindings (solo.md, task.md, duo.md) plus the
# generated active.md pointer — no other (unauthored) backend files.
# ADR-0007 / ADR-0013 / ADR-0025.
extra_backends="$(find roles/backends -mindepth 1 -type f \
  ! -name solo.md ! -name task.md ! -name duo.md ! -name active.md 2>/dev/null | wc -l | tr -d ' ')"
assert_eq "0" "$extra_backends" "roles/backends/ contains only solo.md, task.md, duo.md, active.md"

# active.md is GENERATED: byte-identical to the configured backend's binding
# (the indirection seam — ADR-0013). Default backend is solo.
seam_backend="$(yq -r '.features.orchestration.backend // "solo"' .wip.yaml 2>/dev/null || printf solo)"
[[ -f "roles/backends/$seam_backend.md" ]] || seam_backend="solo"
assert_cmp "roles/backends/active.md" "roles/backends/$seam_backend.md" \
  "active.md == roles/backends/$seam_backend.md (generated pointer in sync)"

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

# --- backends/task.md is a real second backend (ADR-0013) ---------------
# The Task backend binds to the Task tool + on-disk files, naming NO Solo MCP
# tool — the proof that the seam admits a genuinely different backend.
assert_file "roles/backends/task.md" "roles/backends/task.md present"
assert_not_grep 'mcp__solo__' "roles/backends/task.md" "backends/task.md names no Solo MCP tool"
assert_grep 'subagent_type' "roles/backends/task.md" "backends/task.md names subagent_type"
assert_grep 'Task tool' "roles/backends/task.md" "backends/task.md names the Task tool"

# --- backends/duo.md is a third backend that delegates to Duo (ADR-0025) -
# Duo is a spawner LAYERED ON Solo, so duo.md legitimately names Solo substrate
# tools (identity/ledger are Solo's) — the no-Solo-token proof used for task.md
# does NOT apply. The proof duo.md is a genuinely different binding is that it
# delegates runtime selection to Duo, naming Duo's launch + preset surface.
assert_file "roles/backends/duo.md" "roles/backends/duo.md present"
assert_grep 'mcp__duo__launch_agent' "roles/backends/duo.md" "backends/duo.md delegates via mcp__duo__launch_agent"
assert_grep 'preset' "roles/backends/duo.md" "backends/duo.md names the preset vocabulary"
# launch_agent is the ONLY launch surface (BDS-99). Duo resolves by random pick and
# launch_agent re-resolves, so a resolved result never predicts a launch — naming
# resolve_preset here only invites resolve-then-spawn past Duo.
assert_not_grep 'resolve_preset' "roles/backends/duo.md" "backends/duo.md names no dry-run resolve surface"

# --- role-policy sanity (roles/tier-policy.md) --------------------------
# Each Role name appears in a per-Role assignment context.
for role in Orchestrator Coordinator Researcher Builder; do
  assert_grep "$role" "roles/tier-policy.md" "tier-policy.md mentions $role"
done

# --- role-centric request contract (ADR-0025) ---------------------------
# The shared contract requests by ROLE, not by a small/medium/large tier:
# the §Role Selection section replaced §Tier Selection in shared.md.
assert_grep '## Role Selection' "roles/shared.md" "shared.md requests by Role (§Role Selection)"
assert_not_grep '## Tier Selection' "roles/shared.md" "shared.md no longer has a §Tier Selection"

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
  assert_grep '@../roles/backends/active.md' "$path" "$a references roles/backends/active.md"
  # Plugin agent body must NOT name Solo MCP tools — same forbidden set
  # as the behavior files (plugin agents are thin pointers).
  bad="$(grep -En -- "$FORBIDDEN" "$path" 2>/dev/null | grep -v '^[0-9]*:.*roles/backends/active.md' || true)"
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
