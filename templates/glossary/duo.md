<!-- wip glossary partial: DUO backend (orchestration binding). Included ONLY when
     features.orchestration.backend is `duo` in .wip.yaml. Binds the abstract
     orchestration terms (see orchestration.md) to Duo — a spawner layered on Solo. A
     consumer on a different orchestration backend never sees these terms. -->

## Duo backend (orchestration binding)

The Duo backend binds the backend-agnostic Roles to **Duo**, a spawner layered on Solo:
Duo launches Solo agent processes, but picks *which* runtime from user-configured
**presets** rather than wip resolving a tool. The coordination substrate (identity, Todos,
scratchpad, timers) is the **Solo backend's** — Duo replaces only the spawn. Swapping
backends swaps this partial; the Roles and capability terms are unchanged.

| Abstract term (orchestration.md) | Duo binding |
|------|------|
| **Runtime selection** → **preset** | wip requests a **Role**; the binding maps `role → Duo preset name` (identity by default, optional `features.duo.presets` override) and calls `mcp__duo__launch_agent(preset)`. **Duo** owns the `agent_tool_id`, `extra_args`, provider enable/disable, and the random pick among a preset's enabled definitions. |
| **Agent process** → **Solo process via Duo** | A Solo agent process launched through a Duo preset. Identity/liveness/coordination are Solo's (see the Solo binding); only the launch path differs. |
| **Task ledger** → **Todo** | The live execution surface, as under Solo — Duo launches Solo agents that share the Solo control plane. Tagged `<slug>/step-NN`. |

| Duo-specific term | Definition |
|------|------------|
| **preset** | A freeform, unordered Duo label mapping to one or more definitions; the unit wip requests. |
| **provider** | A freeform label (e.g. `anthropic`, `openai`) whose enabled/disabled state Duo reads fresh per launch; toggled via `mcp__duo__set_provider_enabled`. |

**Reachability is a hard requirement:** with `features.orchestration.backend: duo`,
`orchestrate prep` hard-errors (exit 3 `backend-unreachable`) when Duo is not reachable —
no silent fall-back to Solo (ADR-0025). **Source of truth** carries over unchanged: the
**Roadmap** is the durable plan of record; Solo **Todos** are the live execution mirror.
