# Solo backend binding

This file binds the Roles capability to the **Solo** orchestration
backend. The Role behavior in the sibling files
([`orchestrator.md`](../orchestrator.md), [`coordinator.md`](../coordinator.md),
[`researcher.md`](../researcher.md), [`builder.md`](../builder.md),
[`shared.md`](../shared.md), [`tier-policy.md`](../tier-policy.md)) is
backend-agnostic; **this is the only file that names Solo MCP tools.**

A future backend (`backends/<name>.md`) supplies the same surface
(identity, substrate bindings, role resolver, tag glossary,
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
| **Operator hold** (engagement guard) | KV flag + cooperative timer guard | `mcp__solo__kv_set` / `mcp__solo__kv_get` / `mcp__solo__kv_delete` (the canonical hold flag, keyed by the held process); timer bodies check the hold before acting; `mcp__solo__timer_pause` / `mcp__solo__timer_resume` only for timers owned by the current actor when a Role deliberately pauses its own pending timer |

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

## Operator-engagement guard

The abstract **operator-engagement guard** (see [`shared.md`](../shared.md)
§Pause and Resume) binds to a KV hold flag plus the passive engagement
read below. In Solo a human can type directly into any agent process via
`mcp__solo__send_input` or the Solo UI, so closing or injecting into a
process needs more than the agent-centric liveness read.

**Hold (explicit, deterministic).**

- A hold is a KV flag keyed by the held process — e.g.
  `mcp__solo__kv_set(key="wip/hold/<process_id>", value=<who+why>)`. Any
  Role reads it with `mcp__solo__kv_get` before closing or injecting, and
  the operator clears it with `mcp__solo__kv_delete`.
- Solo timer pause/resume is owner-scoped: `mcp__solo__timer_list`,
  `mcp__solo__timer_pause`, and `mcp__solo__timer_resume` operate on timers
  owned by the current actor. Therefore a hold cannot rely on pausing
  another agent's timers. Timer bodies must be written defensively: on wake,
  re-read the hold for the delivery process and any watched process they
  would act on; if held, take no action and re-arm.
- A Role may pause/resume its own pending timers when it places or observes
  a hold, but this is an optimization only. The required safety property is
  the KV hold check immediately before close/inject, including timer-fired
  actions.
- Locks are not the v1 hold mechanism. They remain useful for short-lived
  edit/ownership leases, but their owner-scoped release and TTL semantics
  do not match an operator-cleared hold flag.

**Passive engagement re-check (best-effort).** Immediately before closing
or injecting into a watched process, read:

- `mcp__solo__get_process_status` → `agent_state.idle_seconds`: a small
  value the watcher did not cause means the agent went active again
  (recent human-driven turn) — back off and re-arm.
- `mcp__solo__get_process_output` rendered tail (heuristic): an un-submitted draft in
  the input line means the operator is *composing* a prompt — a state
  `idle_seconds` alone misses (the agent is idle while the human types).
  Back off and re-arm.

> Verify against the live payload: if `mcp__solo__get_process_status`
> exposes a cleaner human-input field (e.g. an `awaiting_input` /
> pending-input / last-input-actor signal), prefer it over the
> output-tail heuristic and bind to it here instead.

**Inject guard.** Run the same hold + engagement check before any
`mcp__solo__send_input` to a watched process (bootstrap, status-check, or
retry prompt), not only before `mcp__solo__close_process`. Timer bodies are
injected as fresh turns too; each timer body must begin by checking the hold
for the delivery process and any watched process it is about to route.

## Role resolver

Solo resolves a requested **Role** (`orchestrator` / `coordinator` /
`researcher` / `builder`, plus optional `<role>-escalated` targets — see
[`tier-policy.md`](../tier-policy.md)) to a concrete `agent_tool_id` at spawn
time. Resolution is an explicit **config lookup**, not classification: the
`features.solo.agent_tools` map in `.wip.yaml` names a Solo tool per Role, and
`mcp__solo__list_agent_tools` turns that name into the operational id.

There is **no token classification** — a Role maps to a tool by explicit config,
never by parsing model names out of a tool's `command`. This removes the
duplicate-tools-with-model-names-in-commands coupling entirely (ADR-0025).

### Inputs

Required:

- `role`: the requesting Role, or its escalation target (e.g. `builder` vs
  `builder-escalated`). The caller decides *which* Role key to request; the
  escalation policy is in [`tier-policy.md`](../tier-policy.md).

Optional:

- `name`: process name to use at spawn time.
- `purpose`: one-line reason for auditability.

### Source of truth

Two reads, combined:

1. **`features.solo.agent_tools`** (`.wip.yaml`) — a map from Role key to a Solo
   tool **name** (never an id — ids are operational and change as tools are
   added, removed, or renamed). A `default` entry is the fallback for any Role
   without an explicit key. Example:

   ```yaml
   features:
     solo:
       agent_tools:
         default: Claude           # fallback for every Role
         builder: Pi               # a cheaper local runtime for routine build
         builder-escalated: Claude # stronger runtime on escalation
   ```

2. **`mcp__solo__list_agent_tools()`** — resolves a configured tool *name* to
   the `agent_tool_id` used by
   `mcp__solo__spawn_process(kind="agent", agent_tool_id=N)`. Returns, per tool:
   `id`, `name`, `command`, `tool_type`, `enabled`. Do **not** maintain a
   project-static name→id mapping; resolve fresh each spawn. An all-digits
   config value is treated directly as an `agent_tool_id`.

### Resolution precedence (the ladder)

Resolution walks a single **precedence chain** (first match wins). The config
map is the normal path; the pins above it and the ask/hard-fail below it exist
so the run stays unblocked when the map yields nothing.

1. **Request override — `--agent <name|id>`.** A per-invocation value passed at
   orchestrate/start time. An **all-digits** value is an `agent_tool_id`;
   otherwise it is a **name**, matched against
   `mcp__solo__list_agent_tools()[].name` (and `command` as a secondary signal).
   The resolved tool is used for this spawn and written to the session pin (rung
   2) so it governs the rest of the run. The flag is **parsed in the command
   bodies** (`commands/orchestrate.md` / `commands/start.md`); those bodies name
   no MCP tool — they hand the parsed value into the Role flow, and it is **this**
   Role flow that performs the `mcp__solo__list_agent_tools` match and the
   `mcp__solo__kv_set` pin. Highest precedence.

2. **Session pin — KV `wip/<slug>/agent-pin`.** Read via
   `mcp__solo__kv_get(key="wip/<slug>/agent-pin")` where `<slug>` is the
   initiative slug. If set, its value (a tool name or id, already resolved when
   written) is used **verbatim**. The pin is the durable propagation channel: the
   Coordinator and **every** Builder spawn read it *before* the config lookup, so
   one choice — made by a `--agent` flag (rung 1) or by the ask (rung 4) —
   applies to all downstream spawns without prompt-threading. No TTL; cleared or
   overwritten at the operator's discretion via
   `mcp__solo__kv_set` / `mcp__solo__kv_delete`.

3. **Configured map — `features.solo.agent_tools`.** Look up
   `agent_tools[<role>]`; if the Role has no explicit entry, fall through to
   `agent_tools.default`. Resolve the resulting tool **name** against
   `mcp__solo__list_agent_tools()` (match on `name`, then `command` as a
   secondary signal; drop tools where `enabled != true`). This is the normal
   resolution path — set once, every spawn honors it. On this repo `default:
   Claude` resolves every Role to the Claude runtime (id 3) with no manual pin.

4. **Ask the human, then pin.** The interactive last resort, reached only when
   rungs 1–3 are empty (no `--agent`, no session pin, and neither the Role key
   nor `default` resolves to an enabled tool) **and a human is present**. The
   live human-facing agent (the **Orchestrator**) asks which tool to use — the
   doc text does not prompt; the agent following this rule does. On the answer it:
   - writes `mcp__solo__kv_set(key="wip/<slug>/agent-pin", value=<the chosen
     tool>)`, so the choice applies to **this spawn and all future spawns this
     session** (it becomes the rung-2 pin); and
   - **offers** to persist the choice permanently — if the human says "always use
     this", the Orchestrator edits `features.solo.agent_tools` in `.wip.yaml`
     **inline** with the existing `yq` idiom (setting the Role key, or `default`
     when the choice should apply to every Role). (The dedicated hardened persist
     write-verb is **deferred**; inline edit only for now.)

5. **Hard-fail.** Reached only when rungs 1–3 resolved nothing **and** the
   session is **non-interactive** (no human to ask at rung 4). Emits the §Failure
   modes message — enumerating the `--agent`, `agent-pin` KV, and
   `features.solo.agent_tools` escape hatches. Never a silent fall-back to an
   arbitrary enabled tool.

**When rungs 4–5 apply.** Only when config resolution is **non-confident**
(neither the Role key nor `default` resolves to an enabled tool) **and Duo is not
in use** — `solo` is the active orchestration backend
(`features.orchestration.backend: solo`, no Duo backend bound). Runtime selection
is the Duo backend's job when it is active; this Solo-alone bridge does not engage
(the guard is exactly "active backend == `solo`"). When the config map yields an
enabled tool, resolution is confident and rungs 4–5 are skipped.

### Spawn behavior

- Spawn via `mcp__solo__spawn_process` with `kind="agent"` and the resolved
  `agent_tool_id`.
- Return the created process metadata.
- Do **not** send a bootstrap message unless explicitly requested by the caller.

### Return shape

- `process_id`
- `agent_tool_id`
- `tool_name`
- `tool_type`
- `command`
- `role` (the requested Role key, including any `-escalated` suffix)
- `selection_reason` (which ladder rung / config key resolved it)
- `alternatives_considered` (present even when empty)

### Failure modes

Hard-fail with an actionable message when:

- Neither the requested Role key nor `default` resolves to an enabled tool, and
  the session is non-interactive (rung 5 above).
- A configured tool **name** matches no enabled tool in
  `mcp__solo__list_agent_tools()`.
- The spawn fails after resolving a candidate.

Failure message must include: requested Role, the `features.solo.agent_tools`
entry consulted (Role key and `default`), discovered tools, enabled tools after
filtering, and the escape hatches the operator can use to resolve the run: a
`--agent <name|id>` request override, a `wip/<slug>/agent-pin` session pin in the
KV store, and a `features.solo.agent_tools` entry (Role key or `default`) in
`.wip.yaml`.

Do **not** silently fall back to an arbitrary enabled tool.

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
  an agent process; resolved by the Role resolver above.

When in doubt, refer to units as **processes** and qualify as
**agent process** or **terminal process** for clarity.
