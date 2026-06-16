# `/wip:orchestrate` + `orchestrate prep` (spec)

Status: shipped 2026-06-16.

Closes the "(deferred)" entrypoint that boots orchestration for the active step. Until now
`/wip:start`'s on-`go` "Orchestrate" branch described the boot inline ("become the
Orchestrator and spawn a Coordinator via Solo"); this ships the ergonomic wrapper for
exactly that branch and the deterministic prep beneath it.

Decision: [ADR-0012](../decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md) — the
orchestrate entrypoint is a **plugin command, not a CLI verb**, because spawning Claude
agents happens through the backend's MCP tools, reachable only from Claude Code. Plumbing
contributes one deterministic readiness verb (`orchestrate prep`); the plugin command
spawns. ADR-0001's seam holds (the gate/brief is deterministic plumbing; the plugin does the
LLM + spawn work) and ADR-0007's backend seam holds (the command body names no backend
tool; the Solo binding stays in `roles/backends/solo.md`).

## 1. Plumbing — `orchestrate prep`

```
wip-plumbing orchestrate prep [--initiative <slug>]
```

A pure function of `.wip.yaml` + the initiative's `roadmap.md` + disk. Resolves the
initiative (default `current_initiative`, like `status`) and its `active_step`, gates
orchestration readiness, and emits the "what to orchestrate" brief.

- **Emits facts, not staffing.** Output carries `initiative`, `orchestration {enabled,
  backend}`, `round`, `active_step {id, title, shipped, lane}`, `workplan {path, exists}`,
  `roadmap`, `signals`. It deliberately omits Tier, the Coordinator process name, and any
  `agent_tool_id` — those are Role decisions (`roles/tier-policy.md`, `roles/shared.md`) the
  Orchestrator makes when it adopts the role, not plumbing output. The verb names **no**
  `mcp__solo__*` tool.
- **A missing workplan is not an error.** The Coordinator's Researcher produces the workplan
  in Phase 1 (`roles/coordinator.md`), so `workplan.exists: false` exits 0. The emitted
  `workplan.path` is the existing file when one is present (glob `<step-id>-*.md` under
  `workplans/`), otherwise the canonical path derived from the step title the same way
  `workplan init` derives it.
- **Gates (exit codes per the prtend contract):**
  - `features.orchestration.enabled` not true → exit **3** `orchestration-not-enabled`.
  - no `current_initiative` and no `--initiative` → exit **3** `no-initiative`;
    `--initiative` names an unknown slug → exit **3** `unknown-initiative`.
  - the initiative has no `active_step` → exit **4** `no-active-step` (run `/wip:start`).
  - the `active_step` is not in the roadmap → exit **4** `step-not-in-roadmap`.
- **Signals (advisory):** `active-step-shipped` when the active step is already shipped —
  surfaced, not refused (mirrors `status`' divergence signal).

See `wip-plumbing-cli.md` for the verb-table entry and the canonical stdout shape.

## 2. Plugin — `/wip:orchestrate [--initiative <slug>]`

```
argument-hint: "[--initiative <slug>]"
allowed-tools: [Bash, Read, Task]
```

Procedure:

1. **Resolve `wip-plumbing`** — the `${CLAUDE_PLUGIN_ROOT}/bin/wip-plumbing` idiom copied
   from `commands/start.md`.
2. **Parse `$ARGUMENTS`** — only `--initiative <slug>` is accepted.
3. **Prep** — run `wip-plumbing orchestrate prep` (with `--initiative` when given). Surface
   any error envelope verbatim and stop (`orchestration-not-enabled` → enable it;
   `no-active-step` → run `/wip:start <step-id>` first).
4. **Surface the brief** — print the initiative, the `active_step` (id + title), the
   workplan path (noting `exists: false` is fine), and any `signals`. If
   `active-step-shipped` is present, call it out and confirm before continuing.
5. **Establish role, then orchestrate** — do not assume you are the worker:
   - If you already hold a WIP role (check via the backend), defer to it.
   - If you're a plain session, become the Orchestrator (`roles/orchestrator.md` +
     `roles/shared.md` + `roles/tier-policy.md` + `roles/backends/solo.md`): confirm
     identity, **state the concrete spawn action (Coordinator name + Tier) and confirm with
     the user** before spawning (`roles/orchestrator.md` — no silent spawn on an ambiguous
     start), then spawn the Coordinator (which spawns its Researcher) for the active step via
     the backend and run the Orchestrator polling loop.

The command body is the contract; it names no backend MCP tool — spawn mechanics live only
in `roles/backends/solo.md`. `/wip:start`'s on-`go` "Orchestrate" branch hands off here.

## 3. Tests

- `test/test-orchestrate-prep.sh` — `orchestrate prep` JSON shape + the exit-code gate,
  network/MCP-free: ready brief (ok, active_step, `workplan.exists`); existing workplan glob
  vs derived-path fallback; `active-step-shipped` signal; `orchestration-not-enabled` (exit
  3); `no-active-step` (exit 4); `step-not-in-roadmap` (exit 4); `unknown-initiative` (exit
  3); never emits `mcp__solo__`/`agent_tool_id` (seam).
- `test/test-plugin-manifest.sh` — `/wip:orchestrate` present, front-matter, bundled-binary
  resolution, the literal `orchestrate prep` shell-out, the `roles/orchestrator.md` +
  `roles/backends/solo.md` pointers, and **no** `mcp__solo__` tool name in the command body.
