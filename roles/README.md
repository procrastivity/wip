# roles/ — Orchestration Roles

Behavioral operating-manuals for the agent processes that run orchestrated work:
**Orchestrator**, **Coordinator**, **Researcher**, **Builder**. These are **tooling**,
shipped by the `wip` plugin and gated on `features.orchestration.enabled` — a consumer
references them, it does not copy/maintain them (this is what kills the copy-drift seen
across hand-vendored playbooks).

> "Roles" ≠ "Playbook". A Role is *how an actor behaves*; a Playbook is *an initiative's
> plan* (its Roadmap + Workplans, under `.wip/initiatives/<slug>/`). See the glossary.

## Orchestration is backend-agnostic (ADR-0007)

Roles describe *what an actor does and how decisions flow* — not the mechanics of any one
runtime. Orchestration is a **capability** with **pluggable backends**; **Solo** is the
first and default backend (`features.orchestration.backend`). The split is structural so a
second orchestration style (e.g. native harness / `claude` subagents) can bind the same
Roles without rewriting them:

- **Behavior files** (`orchestrator.md`, `coordinator.md`, `researcher.md`, `builder.md`,
  `shared.md`) are backend-agnostic. They reference **"spawn a `<Role>` agent"** and **"the
  task ledger"** — never `mcp__solo__*`, `agent_tool_id`, or `whoami`.
- **`tier-policy.md`** owns the abstract per-Role runtime policy: each Role's default
  assignment + optional escalation target (role is the only selection signal — ADR-0025
  retired the `small`/`medium`/`large` tier axis). No runtime tool ids. (The filename is
  retained as the stable `@`-include path; the rename is a deferred cross-cutting cleanup.)
- **`backends/solo.md`** is the **one** place that names Solo MCP tools (`spawn_process`,
  todos, scratchpads, timers, `whoami`) and the Solo runtime resolver. A future backend
  adds `backends/<name>.md`; nothing else moves.

Acceptance shape: adding a hypothetical `backends/native.md` must require touching **zero**
behavior or `tier-policy.md` files.

## Status

✅ Shipped step-12 — distilled from the gitignored
`workflow-portable-stub/playbook/` study slice and decoupled per ADR-0007.
Capability vocabulary lives in `templates/glossary/orchestration.md`; the
Solo binding in `templates/glossary/solo.md`.

Layout:

```
roles/
  README.md           # this index
  orchestrator.md     # behavior (backend-agnostic)
  coordinator.md      # behavior (backend-agnostic)
  researcher.md       # behavior (backend-agnostic)
  builder.md          # behavior (backend-agnostic)
  shared.md           # shared behavior (backend-agnostic; was shared-static.md)
  tier-policy.md      # abstract per-Role runtime policy (assignment + escalation target)
  backends/
    solo.md           # the ONLY doc naming Solo MCP tools + the Solo runtime resolver
```
