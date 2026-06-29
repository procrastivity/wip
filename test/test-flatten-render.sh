#!/usr/bin/env bash
# test-flatten-render — exercise the pure flatten renderer
# (lib/wip/wip-plumbing-flatten-lib.bash) across the matrix
# {orchestrator, coordinator, researcher, builder} × {solo, task}.
#
# Proves the workplan step-02 contract: self-containment (no @-includes),
# all four sources inlined in the fixed order, backend correctness, verbatim
# frontmatter, determinism/idempotence (D6), the D5 drift guard, and bad-input
# rejection.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="flatten-render"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# The renderer reads templates/ via wip_templates_dir and roles/ via the
# WIP_ROLES_DIR seam; wip-plumbing-lib.bash supplies wip_templates_dir.
export WIP_LIB="$PWD/lib/wip"
export WIP_TEMPLATES_DIR="$PWD/templates"
export WIP_ROLES_DIR="$PWD/roles"
# shellcheck source=lib/wip/wip-plumbing-lib.bash
source "$WIP_LIB/wip-plumbing-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-flatten-lib.bash
source "$WIP_LIB/wip-plumbing-flatten-lib.bash"

# FORBIDDEN — the Solo-specific token set, kept in sync VERBATIM with
# test/test-roles-backend-seam.sh (the no-Solo-token invariant). Reused here to
# gate the Task-backend render.
FORBIDDEN='mcp__solo|solo_process_id|agent_tool_id|spawn_process|scratchpad|todo_create|todo_list|whoami|list_agent_tools|mcp-cli|kv_set|kv_get|timer_set|timer_fire_when_idle|rename_process|wait_for_bound_port|kind="agent"|kind=\\"agent\\"'

# --- local assertion helpers (kept out of the shared helpers.sh) ------------
pass() {
  _WIP_PASS=$((_WIP_PASS + 1))
  printf '  ok   %s\n' "$1"
}
fail() {
  _WIP_FAIL=$((_WIP_FAIL + 1))
  printf '  FAIL %s\n' "$1" >&2
}

# expect_pass <msg> <cmd...> — assert the command exits zero.
expect_pass() {
  local msg="$1"
  shift
  if "$@" >/dev/null 2>&1; then pass "$msg"; else fail "$msg (expected zero exit)"; fi
}

# expect_fail <msg> <cmd...> — assert the command exits non-zero.
expect_fail() {
  local msg="$1"
  shift
  if "$@" >/dev/null 2>&1; then fail "$msg (expected non-zero exit)"; else pass "$msg"; fi
}

# assert_order <msg> <file> <pattern>... — assert each pattern first appears on
# a strictly later line than the previous (proves inline emit order).
assert_order() {
  local msg="$1" file="$2"
  shift 2
  local prev=0 ln pat
  for pat in "$@"; do
    ln="$(grep -nE -- "$pat" "$file" 2>/dev/null | head -1 | cut -d: -f1 || true)"
    if [[ -z "$ln" || "$ln" -le "$prev" ]]; then
      fail "$msg (pattern '$pat' at line '${ln:-none}', not after line $prev)"
      return
    fi
    prev="$ln"
  done
  pass "$msg"
}

# fm_block <file> — print the leading ---...--- frontmatter block (both fences
# inclusive). Independent of the lib's own extractor so the verbatim check is
# applied identically to template and render.
fm_block() {
  awk 'NR==1 && $0=="---"{print;f=1;next} f && $0=="---"{print;exit} f{print}' "$1"
}

ROLES=(orchestrator coordinator researcher builder)
BACKENDS=(solo task)

# --- matrix: {role} × {solo, task} -----------------------------------------
for role in "${ROLES[@]}"; do
  case "$role" in
    orchestrator) heading="Orchestrator" ;;
    coordinator) heading="Coordinator" ;;
    researcher) heading="Researcher" ;;
    builder) heading="Builder" ;;
  esac
  template="templates/setup/agents/agents/$role.md"

  for backend in "${BACKENDS[@]}"; do
    case "$backend" in
      solo) backend_sentinel="^# Solo backend binding$" ;;
      task) backend_sentinel="^# Task backend binding$" ;;
    esac

    out="$(wip_mktemp)/render-$role-$backend.md"
    if wip_flatten_render "$role" "$backend" >"$out" 2>/dev/null; then
      pass "render $role/$backend exits zero"
    else
      fail "render $role/$backend exits zero"
      continue
    fi

    # Self-containment: the rendered file carries no @-include syntax.
    assert_not_grep '@[^[:space:]]*\.md' "$out" \
      "$role/$backend render has no @-include syntax"

    # All four sources inlined, in the fixed emit order (D5).
    assert_grep '^# Shared Role Behavior$' "$out" "$role/$backend inlines shared.md"
    assert_grep "^# $heading\$" "$out" "$role/$backend inlines $role.md body"
    assert_grep '^# Tier Policy$' "$out" "$role/$backend inlines tier-policy.md"
    assert_grep "$backend_sentinel" "$out" "$role/$backend inlines backends/$backend.md"
    assert_order "$role/$backend inlines sources in fixed order" "$out" \
      '^# Shared Role Behavior$' "^# $heading\$" '^# Tier Policy$' "$backend_sentinel"

    # Frontmatter byte-identical to the template (D4).
    tpl_fm="$(wip_mktemp)/tpl.fm"
    out_fm="$(wip_mktemp)/out.fm"
    fm_block "$template" >"$tpl_fm"
    fm_block "$out" >"$out_fm"
    assert_cmp "$tpl_fm" "$out_fm" "$role/$backend frontmatter byte-identical to template"

    # Backend correctness.
    if [[ "$backend" == "solo" ]]; then
      assert_grep 'mcp__solo__spawn_process' "$out" "$role/solo render names mcp__solo__spawn_process"
      assert_grep 'list_agent_tools' "$out" "$role/solo render names list_agent_tools"
    else
      # The Task render names ZERO Solo MCP tools. The full FORBIDDEN set has
      # one benign collision: roles/backends/task.md mentions the bare word
      # `whoami` in PROSE ("There is no whoami ...") to explain the Task
      # backend's absence of it — not a Solo-tool reference. (The seam test
      # likewise gates task.md only on `mcp__solo__`, not the full set.) So we
      # assert the hard no-leak gate plus that the flatten introduces no
      # FORBIDDEN token beyond task.md's own.
      assert_not_grep 'mcp__solo__' "$out" "$role/task render names no Solo MCP tool"
      render_hits="$(grep -Eo -- "$FORBIDDEN" "$out" 2>/dev/null | LC_ALL=C sort -u | tr '\n' ' ' || true)"
      src_hits="$(grep -Eo -- "$FORBIDDEN" roles/backends/task.md 2>/dev/null | LC_ALL=C sort -u | tr '\n' ' ' || true)"
      assert_eq "$src_hits" "$render_hits" \
        "$role/task render leaks no Solo token beyond task.md's own prose"
    fi
  done
done

# --- determinism / idempotence (D6) ----------------------------------------
# Same (role, backend) rendered twice must be byte-identical — step-05's
# --check round-trip drift gate depends on this.
det_a="$(wip_mktemp)/det-a.md"
det_b="$(wip_mktemp)/det-b.md"
wip_flatten_render coordinator solo >"$det_a" 2>/dev/null
wip_flatten_render coordinator solo >"$det_b" 2>/dev/null
assert_cmp "$det_a" "$det_b" "coordinator/solo renders byte-identical on re-render"

# --- D5 drift guard fires ---------------------------------------------------
# wip_flatten_parse_template accepts the canonical template...
expect_pass "parse_template accepts the canonical orchestrator template" \
  wip_flatten_parse_template "templates/setup/agents/agents/orchestrator.md" orchestrator
# ...and a template whose @-include set differs from the canonical four is
# rejected loudly (here: tier-policy.md dropped), failing the render non-zero.
drift_dir="$(wip_mktemp)"
mkdir -p "$drift_dir/setup/agents/agents"
cat >"$drift_dir/setup/agents/agents/orchestrator.md" <<'EOF'
---
name: wip-orchestrator
description: drift fixture
tools: Read
---

# Orchestrator (wip)

Act as the Orchestrator for this wip initiative per the linked manuals.

- @../roles/shared.md
- @../roles/orchestrator.md
- @../roles/backends/active.md
EOF
if WIP_TEMPLATES_DIR="$drift_dir" wip_flatten_render orchestrator solo >/dev/null 2>&1; then
  fail "drift guard fails the render on a mismatched @-include set"
else
  pass "drift guard fails the render on a mismatched @-include set"
fi

# --- bad inputs -------------------------------------------------------------
expect_fail "unknown role rejected" wip_flatten_render bogus solo
expect_fail "unknown backend (no backends/<name>.md) rejected" wip_flatten_render orchestrator nosuch
expect_fail "invalid backend name rejected" wip_flatten_render orchestrator BOGUS
expect_fail "missing backend arg rejected" wip_flatten_render orchestrator

test_summary
