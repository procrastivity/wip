---
description: Boot orchestration for the active step — become the Orchestrator and spawn its Coordinator.
argument-hint: "[--initiative <slug>] [--agent <name|id>]"
allowed-tools: [Bash, Read, Task]
---

# /wip:orchestrate — boot orchestration for the active step

The ergonomic entrypoint for `/wip:start`'s on-`go` **Orchestrate** branch.
It does the deterministic prep (resolve the initiative + its `active_step`,
gate orchestration readiness), then hands the actual work to the **Roles**:
you become the **Orchestrator** and spawn a **Coordinator** for the active
step via the active backend.

Spawning Claude agents is a plugin/MCP concern, so there is **no**
`wip orchestrate` CLI verb — plumbing contributes only the readiness prep
(`wip-plumbing orchestrate prep`); this command spawns. See
[ADR-0012](../engineering/decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md).

## Procedure

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Parse `$ARGUMENTS`.** Accepted arguments are optional
   `--initiative <slug>` and optional `--agent <name|id>`. Anything else:
   stop and show the usage line.

   - `--agent <name|id>` **pins which agent tool every spawn uses this
     run.** The value is a tool **name** or a numeric tool **id**
     (all-digits → id; otherwise a name). Hand the parsed value into the
     Orchestrator flow as the **session spawn pin** — the request
     override at the top of the resolver's fallback ladder. Once set, the
     pin governs the Coordinator→Builder spawns for the rest of the run
     and **bypasses the resolver's interactive fallback prompt**, so an
     operator can pre-select the tool when tier classification would
     otherwise be non-confident. The command body does **not** persist
     the pin or name any backend tool — the live Role flow records it
     (see `roles/backends/active.md` and `roles/tier-policy.md`).

3. **Prep (plumbing).** Run `wip-plumbing orchestrate prep` (append
   `--initiative <slug>` when given). This is deterministic — it resolves the
   initiative + `active_step`, gates readiness, and emits the brief. On exit
   ≠ 0, surface the error envelope **verbatim** and stop. The gates you may
   hit:
   - `orchestration-not-enabled` (exit 3) — `features.orchestration.enabled`
     is not true; the user must enable it (e.g. `wip-plumbing setup agents`).
   - `no-active-step` (exit 4) — no `active_step` is set; tell the user to run
     `/wip:start <step-id>` first.
   - `step-not-in-roadmap` / `unknown-initiative` — surface and stop.

4. **Surface the brief.** From the prep JSON, print a tight summary: the
   initiative, the `active_step` (`id` + `title`), the `workplan.path` (note
   when `workplan.exists` is `false` — that's fine; the Coordinator's
   Researcher produces it), and any `signals`. **If `signals` contains
   `active-step-shipped`, call it out** and ask the user to confirm they mean
   to orchestrate an already-shipped step before going further.

5. **Establish your role, then orchestrate.** Do not assume you are the worker
   or that you should spawn blindly:

   - **Check whether you already hold a WIP role** via the active orchestration
     backend (e.g. Solo `whoami` / your process name; see `roles/backends/`).
   - **If you already hold a WIP role**, defer: keep acting in that role per its
     manual (`roles/<role>.md`). Do not re-drive an orchestrate boot from
     inside a Coordinator/Researcher/Builder.
   - **If you're a plain session**, become the **Orchestrator**: read and follow
     `roles/orchestrator.md` together with `roles/shared.md`,
     `roles/tier-policy.md`, and the active backend binding
     `roles/backends/active.md` (these are the canonical, single-source operating
     instructions — do not paraphrase from memory). Then, per
     `roles/orchestrator.md`:
       - Confirm your identity via the backend.
       - **State the concrete spawn action** — the Coordinator process name and
         Tier for the active step — and confirm with the user before spawning
         (the Orchestrator never spawns silently on an ambiguous start).
       - On confirmation, spawn the Coordinator (which spawns its Researcher)
         for the active step via the backend, and run the Orchestrator's
         polling loop. **If `--agent` was parsed**, hand its value to the
         Orchestrator flow as the session spawn pin before spawning, so
         every Coordinator→Builder spawn this run uses the pinned tool
         without the interactive fallback prompt.

## Notes

- This command body is the contract; do not improvise off-script.
- The command body names **no** backend MCP tools — the spawn mechanics live
  only in the active backend binding `roles/backends/active.md` (regenerated
  from `roles/backends/<backend>.md` by `wip-plumbing orchestrate backend`),
  keeping the backend seam intact (ADR-0007).
- Tier, process naming, and `agent_tool_id` resolution are **Role** decisions,
  not prep output: `orchestrate prep` emits the facts about the work, the Roles
  decide how to staff it.
