<!-- wip glossary partial: ORCHESTRATION (backend-agnostic). Included ONLY when
     features.orchestration.enabled is true in .wip.yaml. Defines the Roles capability;
     a concrete backend (e.g. solo.md) binds the abstract terms below. A consumer not
     using orchestration never sees these terms. -->

## Roles

**Roles** are behavioral operating-manuals for the agent processes that do
orchestrated work. They are **tooling, not work product**: project-independent and
**shipped by the `wip` plugin** (a repo references them; it does not copy/maintain
them). Distinct from a Playbook (which is an Initiative's *plan*, see core). Roles are
**backend-agnostic** — the active orchestration backend (Solo today) binds them; see the
backend partial (e.g. `solo.md`).

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

## Abstract substrate

The capability is defined against abstract primitives; each backend binds them to a
concrete runtime (see the backend partial).

| Term | Definition |
|------|------------|
| **Tier** | Capability level requested when spawning an agent (`small`/`medium`/`large`) — a *semantic* request, never a runtime tool id. The backend resolves a Tier to whatever runtime it offers. A `.wip.yaml` policy may force a Tier (e.g. Opus-only). |
| **Task ledger** | The live execution surface (ownership, blockers, comments, locks, status), scoped `<slug>/step-NN`. A *mirror* of the Roadmap, not a replacement. The backend provides the concrete store. |
| **Agent process** | A backend-managed runtime instance that plays a Role. |
| **Operator hold** | A flag an operator places on a spawned agent to take it over directly. While held, no Role closes it or injects into it; timer-delivered turns must check the hold and back off/re-arm before acting. Cleared only by the operator. The backend provides the concrete hold + engagement signal. |
| **Operator-engagement guard** | The rule that gates *both* closing and injecting into any watched agent on the operator: an explicit hold (deterministic) plus a passive engagement re-check (best-effort) before either action. N/A for synchronous one-shot backends. |

**Source of truth for "what's next" (split, deliberate):** the **Roadmap** is the
durable plan of record (git-tracked); the **task ledger** is the live execution mirror.
Sync is one-way — Roadmap → ledger at Step kickoff; only the Coordinator's Step-boundary
archive writes back. Planning must never live backend-only (the `~/.claude/plans/`
failure mode).
