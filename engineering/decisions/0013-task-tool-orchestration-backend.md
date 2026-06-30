# 0013 — Task-tool orchestration backend + active-backend indirection

- Status: accepted
- Date: 2026-06-23
- Source: `orchestration-backends` initiative, Round 1 (step-01/step-02); ADR-0007, ADR-0012

## Context

ADR-0007 made orchestration a capability with pluggable backends and promised a second
backend costs "one glossary partial + one `roles/backends/<name>.md` — Role behaviors,
capability vocabulary, and this rationale stay untouched." But only `solo` was ever built,
so in practice the whole flow is welded to Solo: spawning (`mcp__solo__spawn_process`),
identity (`whoami`), and all live coordination state (Todos / scratchpad / KV / timers).
In an environment **without Solo**, the read/plan surface works but orchestration hits a
wall at the first spawn — there is no Task-tool path and no fallback.

Two facts shape the fix:

1. **Native Task subagents are synchronous, one-shot calls.** A spawn blocks and returns
   exactly once, when the subagent finishes. There is no long-lived process, no idle edge,
   and no out-of-band liveness. The Solo idle-timer / liveness-gate / pause-resume
   machinery is therefore *N/A*, not merely re-bound — the cost moves entirely to **state
   durability**, since subagents don't persist between calls.
2. **ADR-0007's "nothing else moves" was incomplete at the agent layer.** All four
   `agents/*.md` hardcode `@../roles/backends/solo.md`, and Claude Code `@`-includes are
   **static** — an agent file cannot conditionally select a backend. A *live* second
   backend needs a selector, which ADR-0007 did not provide.

## Decision

Ship a **Task backend** and resolve the agent-include gap with an **assembled active
pointer**.

- **`roles/backends/task.md`** binds the backend-agnostic Roles to the built-in Task tool
  + native subagents, with all coordination state on disk under
  `.wip/initiatives/<slug>/orchestration/`: the **task ledger** and **shared note** are
  files; the "long-lived Researcher" is provided by those files, not a process (each
  consult is a *fresh* `wip-researcher` spawn re-hydrated from them). Idle timers, the
  liveness-and-report gate, the tier fallback ladder, KV, and locks are **N/A** under the
  synchronous-spawn model (one writer per file → no locks). Plus `templates/glossary/task.md`
  and one selector row in `wip_glossary_rules` gated on
  `features.orchestration.backend == "task"`.
- **Active-backend indirection.** The four `agents/*.md` `@`-include
  `roles/backends/active.md` — a **generated, committed** pointer regenerated from
  `roles/backends/<backend>.md` by a new `wip-plumbing orchestrate backend <name>` verb
  (and `make active`), exactly as `.wip/GLOSSARY.md` is a generated-but-committed instance.
  This **amends** ADR-0007's "nothing else moves": the agent includes move **once**, to a
  stable indirection file; thereafter a backend is still "one glossary partial + one
  `roles/backends/<name>.md` + one selector row," with no agent edits.

The switch verb sets only the `backend` scalar (`yq -i .features.orchestration.backend`),
not a whole-node rewrite, so the manifest's block style and comments survive repeated
switches.

## Consequences

- Orchestration runs **without Solo**, on native subagents, by setting
  `features.orchestration.backend: task` (or via the switch verb). The Solo path is
  byte-for-byte unchanged: `active.md == solo.md` on a Solo install.
- The synchronous model makes the Coordinator/Orchestrator *simpler* (no polling, no timer
  re-arming, no liveness gate) at the price of file-backed durability for everything
  cross-call. A file ledger satisfies "planning must never live backend-only" directly.
- **Plugin-vs-vendored:** per-project backend switching assumes a vendored install
  (`features.orchestration.source: vendored`); a shared `source: plugin` install switches
  the plugin's `active.md` globally. Documented in `agents/README.md`.
- The ADR-0007 acceptance test (`test/test-roles-backend-seam.sh`) still holds: behavior +
  `tier-policy` files name zero backend tokens; `task.md` names no Solo MCP tool.
- Deferred: a `tier → model` map for the Task backend (small→Haiku / medium→Sonnet /
  large→Opus via the Task tool's `model` override) and a Duo backend.

## Consumer context (vendored, flattened)

Amended 2026-06-30 (BDS-28, *Flatten vendored orchestration agents*, step-04;
ADR-0020 nominates step-04 as owner — `0020-…:126`).

The active-backend indirection above describes the **authoring / `source: plugin`**
path: the four `agents/*.md` `@`-include `roles/backends/active.md`, and the switch
verb regenerates that pointer from `roles/backends/<backend>.md`. A **`source:
vendored`** install (ADR-0020) is shaped differently and switches differently:

- A vendored consumer has **no `active.md` and no `roles/`**. `setup agents` resolved
  each agent's four `@`-includes at install time and emitted four self-contained
  `.claude/agents/wip/<role>.md` files with the backend baked in; there is no pointer
  to regenerate.
- On a vendored install, `wip-plumbing orchestrate backend <name>` flips the manifest
  `backend` scalar (the same surgical `yq -i .features.orchestration.backend` set) and
  then **re-flattens** the four agent files — re-rendering each via
  `wip_flatten_render <role> <name>` (overwrite-iff-differs) — instead of regenerating
  `active.md`. The discriminator is `features.orchestration.source == "vendored"`;
  anything else (`plugin`, or absent) takes the `active.md` path above, byte-for-byte
  unchanged.

The **authoring side is unchanged**: `roles/`, the four thin-pointer agents, the
`active.md` indirection, and the static-`@`-include rationale all stand exactly as
decided above and continue to govern the `source: plugin` path (including this repo).
This note records consumer context only; it does not amend the Decision or
Consequences.
