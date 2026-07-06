<!-- wip glossary partial: SOLO backend (orchestration binding). Included ONLY when
     features.orchestration.backend is `solo` in .wip.yaml. Binds the abstract
     orchestration terms (see orchestration.md) to concrete Solo primitives. A consumer
     on a different orchestration backend never sees these terms. -->

## Solo backend (orchestration binding)

Solo is the default orchestration **backend**: it binds the backend-agnostic Roles and
abstract substrate defined in `orchestration.md` to concrete Solo primitives and MCP
tools. Swapping backends swaps this partial; the Roles and capability terms are unchanged.

| Abstract term (orchestration.md) | Solo binding |
|------|------|
| **Agent process** → **Process** | A Solo-managed runtime instance (agent or terminal). Agent processes play Roles. |
| **Runtime selection** → `agent_tool_id` | Solo resolves a requested **Role** to an `agent_tool_id` at spawn via the `features.solo.agent_tools` map (Role → tool name) + `mcp__solo__list_agent_tools`. A `default` entry is the fallback for any Role; setting only `default` pins every Role to one tool (e.g. Opus-only). |
| **Task ledger** → **Todo** | The live execution surface (ownership, blockers, comments, locks, status). Tagged `<slug>/step-NN`. A *mirror* of the Roadmap, not a replacement. |

| Solo-specific term | Definition |
|------|------------|
| **Scratchpad** | Rolling shared context for a Step. Not a status store — query Todos for status. |
| **Timer** | A pause/resume signal; on fire, its body is injected as a fresh user turn. Bodies must be self-contained (pid, ids, next action). |

**Source of truth** carries over unchanged from `orchestration.md`: the **Roadmap** is the
durable plan of record (git-tracked); Solo **Todos** are the live execution mirror. Sync is
one-way — Roadmap → Todos at Step kickoff; only the Coordinator's Step-boundary archive
writes back.
