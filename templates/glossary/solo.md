<!-- wip glossary partial: SOLO orchestration. Included ONLY when features.solo.enabled
     is true in .wip.yaml. A consumer not using Solo never sees these terms. -->

## Roles (Solo orchestration)

**Roles** are behavioral operating-manuals for the agent processes that do
orchestrated work. They are **tooling, not work product**: project-independent and
**shipped by the `wip` plugin** (a repo references them; it does not copy/maintain
them). Distinct from a Playbook (which is an Initiative's *plan*, see core).

| Role | Definition | Lifetime |
|------|------------|----------|
| **Orchestrator** | Human-facing control plane. Spawns Coordinators, surfaces escalations/status, never writes code, never spawns Builders directly. | One per session |
| **Coordinator** | Drives one Step end-to-end. Spawns/manages the Researcher and Builders; routes escalations up. | One per active Step |
| **Researcher** | Long-lived for a Step. Produces the Workplan; consulted during build. Builders reach it only via the Coordinator. | Per Step |
| **Builder** | Ephemeral. Executes one Chunk/task, then closes. | Per Chunk |

**Invariants:** Orchestrator ≠ Coordinator; Coordinator ≠ Researcher; Builders are
ephemeral; never spawn a Coordinator without a Researcher.

**Verb → Role mapping:** `plan` is the Researcher producing a Workplan; `build` is the
Coordinator running Builders over Chunks; escalations route Builder → Coordinator →
Orchestrator → human.

## Substrate (Solo primitives)

| Term | Definition |
|------|------------|
| **Process** | A Solo-managed runtime instance (agent or terminal). Agent processes play Roles. |
| **Tier** | Capability level requested at spawn (`small`/`medium`/`large`), mapped to an `agent_tool_id`. A `.wip.yaml` policy may force a tier (e.g. Opus-only). |
| **Todo** | The live execution surface (ownership, blockers, comments, locks, status). Tagged `<slug>/step-NN`. A *mirror* of the Roadmap, not a replacement. |
| **Scratchpad** | Rolling shared context for a Step. Not a status store — query Todos for status. |
| **Timer** | A pause/resume signal; on fire, its body is injected as a fresh user turn. Bodies must be self-contained (pid, ids, next action). |

**Source of truth for "what's next" (split, deliberate):** the **Roadmap** is the
durable plan of record (git-tracked); **Todos** are the live execution mirror. Sync is
one-way — Roadmap → Todos at Step kickoff; only the Coordinator's Step-boundary archive
writes back. Planning must never live Solo-only (the `~/.claude/plans/` failure mode).
