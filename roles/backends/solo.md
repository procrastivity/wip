# Solo backend binding

This file binds the Roles capability to the **Solo** orchestration
backend. The Role behavior in the sibling files
([`orchestrator.md`](../orchestrator.md), [`coordinator.md`](../coordinator.md),
[`researcher.md`](../researcher.md), [`builder.md`](../builder.md),
[`shared.md`](../shared.md), [`tier-policy.md`](../tier-policy.md)) is
backend-agnostic; **this is the only file that names Solo MCP tools.**

A future backend (`backends/<name>.md`) supplies the same surface
(identity, substrate bindings, tier resolver, tag glossary,
anti-patterns) bound to its own primitives — and nothing else moves.

Active when `.wip.yaml` has `features.orchestration.backend: solo`.

## Identity & process naming

On activation, every Role:

1. Calls `mcp__solo__whoami()` to confirm its own `process_id`,
   `actor_id`, and project scope.
2. Renames itself via `mcp__solo__rename_process` to its role-scoped
   name (see [`shared.md`](../shared.md) for the patterns —
   `orchestrator`, `<slug>-step-NN-coordinator`,
   `<slug>-step-NN-researcher`, `<slug>-step-NN-builder-MM`).

If `whoami()` cannot identify a Solo-managed child process, call
`mcp__solo__identify_session` with the Role's own `SOLO_PROCESS_ID`
as an identity assertion (never use it to target another process).

## Substrate bindings

Each abstract primitive from
[`templates/glossary/orchestration.md`](../../templates/glossary/orchestration.md)
binds to a concrete Solo primitive + one or more `mcp__solo__*` tools.
(This mirrors the table in
[`templates/glossary/solo.md`](../../templates/glossary/solo.md) and
adds a column naming the runtime tools — the glossary partial is the
vocabulary, this row-set is the behavior binding.)

| Abstract primitive | Solo primitive | MCP tool(s) |
|---|---|---|
| **Agent process** | Process (`kind="agent"`) | `mcp__solo__spawn_process` (with `kind="agent"` + `agent_tool_id`); `mcp__solo__rename_process`; `mcp__solo__get_process_status`; `mcp__solo__close_process` |
| **Task ledger** | Todo | `mcp__solo__todo_create` / `mcp__solo__todo_list` / `mcp__solo__todo_update` / `mcp__solo__todo_complete` / `mcp__solo__todo_get`; `mcp__solo__todo_comment_create` / `mcp__solo__todo_comment_list`; `mcp__solo__todo_add_tag` / `mcp__solo__todo_remove_tag`; `mcp__solo__todo_lock` / `mcp__solo__todo_unlock` |
| **Shared note** (rolling context) | Scratchpad | `mcp__solo__scratchpad_write` / `mcp__solo__scratchpad_read` / `mcp__solo__scratchpad_append` / `mcp__solo__scratchpad_append_section` / `mcp__solo__scratchpad_edit` / `mcp__solo__scratchpad_archive` |
| **Idle timer** (pause/resume) | Timer | `mcp__solo__timer_set`; `mcp__solo__timer_fire_when_idle_any` / `mcp__solo__timer_fire_when_idle_all`; `mcp__solo__timer_cancel` / `mcp__solo__timer_list` |
| **Service readiness** wait | Port-bound wait | `mcp__solo__wait_for_bound_port` |
| **Shared state** | KV store | `mcp__solo__kv_set` / `mcp__solo__kv_get` / `mcp__solo__kv_list` / `mcp__solo__kv_delete` |

Use the task ledger (Todos) as the **primary durable coordination
surface**. Use the shared note (Scratchpad) for rolling context, not
as a replacement for ledger state.

## Liveness signal (re-check before routing)

The abstract **liveness signal** (see [`shared.md`](../shared.md)
§Pause and Resume — the liveness-and-report gate) binds to
`mcp__solo__get_process_status`:

- `agent_state.idle` — `false` means the agent is actively producing
  (thinking/planning/emitting); **re-arm and wait**, do not route.
- `agent_state.idle_seconds` — how long the agent has been quiet. A small
  value after an idle-timer fire indicates a **between-step lull**, not
  completion. There is no magic threshold: the explicit final-report
  comment is the real completion signal; this read is the cheap fast-path
  that catches a premature wake.
- `status` — `"Running"` distinguishes a live process from a dead one
  (the Wake-up Routing "dead process" branch).

Re-check this signal on every idle-timer fire before treating a watched
agent as done.

## Tier resolver

Solo resolves a requested **Tier** (`small` / `medium` / `large` —
see [`tier-policy.md`](../tier-policy.md)) to a concrete
`agent_tool_id` at spawn time via `mcp__solo__list_agent_tools`. The
resolver is **command-first**: model/runtime tokens in the tool's
`command` win over display-name tokens.

### Inputs

Required:

- `tier`: one of `small`, `medium`, `large`.

Optional:

- `name`: process name to use at spawn time.
- `purpose`: one-line reason for auditability.
- `strategy`: `deterministic` (default) or `random`.
- `exclude_ids`: list of `agent_tool_id` values to avoid.

### Source of truth

`mcp__solo__list_agent_tools()` returns, for each tool:

- `id`: the `agent_tool_id` used by
  `mcp__solo__spawn_process(kind="agent", agent_tool_id=N)`.
- `name`: human-readable tool name.
- `command`: command Solo will execute.
- `tool_type`: runtime family (`codex`, `opencode`, `generic`, …).
- `enabled`: whether the tool is enabled.

Do **not** maintain a project-static `agent_tool_id` mapping. Ids are
operational details and change as tools are added, removed, or
renamed.

### Resolution order

1. Query `mcp__solo__list_agent_tools()`.
2. Drop tools where `enabled != true`.
3. Drop tools whose `id` is in `exclude_ids`.
4. Classify remaining tools by `command` tokens.
5. Use `name` tokens only as fallback or tie-break signal.
6. Hard-fail with a clear error if no confident candidates exist.

### Command-first classification

Initial token policy (case-insensitive):

- `small`: `haiku`, `mini`, `flash`, `fast`, `cheap`, `small`
- `medium`: `sonnet`, `standard`, `medium`, `default`, `gpt-5.2`,
  `gpt-5.3-codex`, `gpt-5.4`
- `large`: `opus`, `flagship`, `max`, `large`, `gpt-5.5`

If a token appears in both `command` and `name`, `command` wins. If
`command` is unclassifiable, `name` may be used as fallback — and the
`selection_reason` must say "name fallback".

### Name fallback

For compatibility, not the main contract:

- `small`: `haiku`, `mini`, `flash`, `fast`, `cheap`, `small`
- `medium`: `sonnet`, `standard`, `medium`, `default`
- `large`: `opus`, `flagship`, `pro`, `max`, `large`

Treat `pro` as a **weak** large-tier signal; prefer stronger
command/model tokens when present.

### Selection strategy

Default: `deterministic`

- Sort matching candidates by `agent_tool_id` ascending.
- Select the first candidate.

Optional: `random` — choose uniformly from matching candidates. Use
only when intentional spread is desired.

### Spawn behavior

- Spawn via `mcp__solo__spawn_process` with `kind="agent"` and the
  selected `agent_tool_id`.
- Return the created process metadata.
- Do **not** send a bootstrap message unless explicitly requested by
  the caller.

### Return shape

- `process_id`
- `agent_tool_id`
- `tool_name`
- `tool_type`
- `command`
- `tier`
- `selection_reason`
- `alternatives_considered` (present even when empty)

### Failure modes

Hard-fail with an actionable message when:

- No enabled tools map confidently to the requested tier.
- Multiple tools produce conflicting classification signals that
  cannot be resolved deterministically.
- The spawn fails after selecting a candidate.

Failure message must include: requested tier, discovered tools,
enabled tools after filtering, `exclude_ids` (if any), classification
source used (`command`, `name`, or none), token policy checked.

Do **not** silently fall back to an arbitrary enabled tool.

### Manifest override

`.wip.yaml`'s `features.solo.agent_tier_policy.force_tier` (see
[`tier-policy.md`](../tier-policy.md)) pins every spawn to the named
tier regardless of the Role's preference — the resolver still records
the Role's preference under `selection_reason` for audit. On this
repo: `force_tier: large` (Opus-only).

## Tag glossary

Ledger (Todo) tags used by Solo:

- `roadmap`
- `step-NN` (scoped: prefix with the initiative slug per
  [`shared.md`](../shared.md))
- `task`
- `needs-human`
- `escalation`
- `coordinator-context`

## Anti-pattern — do not use `mcp-cli` from bash

❌ **Wrong**: do not call `mcp-cli solo ...` from bash scripts or
`Monitor` commands.

```bash
# WRONG — do not do this
mcp-cli solo get_process_output --process-name orchestrator
mcp-cli solo spawn_process kind=agent agent_tool_id=3
```

✅ **Right**: call the Solo MCP tools directly.

```
mcp__solo__get_process_output(process_name="orchestrator")
mcp__solo__spawn_process(kind="agent", agent_tool_id=3)
```

**Why**: `mcp-cli` is for CLI usage outside MCP. Inside an agent you
have direct access to the MCP tools; routing through `mcp-cli` adds
shell-escaping complexity and slower poll loops. Instead:

- **One-shot queries**: call the MCP tool directly
  (e.g. `mcp__solo__get_process_output()`).
- **Polling / waiting**: use `mcp__solo__timer_fire_when_idle_any` /
  `mcp__solo__timer_fire_when_idle_all` for process-idle detection,
  or a local `Monitor` bash loop for file/exit-code conditions —
  never `mcp-cli`.
- **Coordination**: use `mcp__solo__kv_*`, `mcp__solo__todo_*`,
  `mcp__solo__scratchpad_*` — not bash-based state files.

## Solo control-plane terminology

- **process** — a Solo-managed runtime instance (agent or terminal).
- **agent process** — a process spawned with `kind="agent"`; used for
  every Role.
- **terminal process** — an interactive shell process (when shell
  execution is needed).
- **spawn** — create a new process via Solo MCP.
- **`agent_tool_id`** — the runtime/tool selection used when spawning
  an agent process; resolved by the Tier resolver above.

When in doubt, refer to units as **processes** and qualify as
**agent process** or **terminal process** for clarity.
