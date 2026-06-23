<!-- wip glossary partial: TASK backend (orchestration binding). Included ONLY when
     features.orchestration.backend is `task` in .wip.yaml. Binds the abstract
     orchestration terms (see orchestration.md) to concrete Task-tool primitives. A consumer
     on a different orchestration backend never sees these terms. -->

## Task backend (orchestration binding)

The Task backend binds the backend-agnostic Roles and abstract substrate defined in
`orchestration.md` to the built-in **Task tool** and native Claude Code subagents, with
all coordination state on disk. It names no MCP server. Swapping backends swaps this
partial; the Roles and capability terms are unchanged.

| Abstract term (orchestration.md) | Task binding |
|------|------|
| **Agent process** → **subagent** | A native subagent invoked via the Task tool (`subagent_type: wip-<role>`). Synchronous: the spawn call blocks and returns the subagent's final result. No persistent process. |
| **Tier** → (advisory) | A native subagent runs on the session's model; there is no tool inventory to resolve a Tier against, so a Tier is an advisory request, recorded for audit. An optional `tier → model` map is deferred. |
| **Task ledger** → **ledger file** | The live execution surface, a markdown file under `.wip/initiatives/<slug>/orchestration/`. Tagged `<slug>/step-NN`. A *mirror* of the Roadmap, not a replacement. The Coordinator is its sole writer. |

| Task-specific term | Definition |
|------|------------|
| **subagent** | A native agent invoked via the Task tool; runs one turn-set and returns a final result, then evaporates. Cross-call state must live in files. |
| **Shared-note file** | Rolling shared context for a Step, on disk. Not a status store — read the ledger file for status. |

**Source of truth** carries over unchanged from `orchestration.md`: the **Roadmap** is the
durable plan of record (git-tracked); the on-disk **ledger file** is the live execution
mirror. Sync is one-way — Roadmap → ledger at Step kickoff; only the Coordinator's
Step-boundary archive writes back. Because the ledger is itself a file (never backend-only
memory), this backend satisfies the "planning must never live backend-only" rule directly.
