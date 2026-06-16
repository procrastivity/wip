# Tier Policy

Abstract Tier semantics + per-Role defaults. Backend-agnostic — Tiers
are **semantic requests** (`small` / `medium` / `large`); the active
orchestration backend resolves each request to a concrete runtime. See
[`backends/`](./backends/) for the resolver.

## Request capability, not runtime id

Callers — Orchestrator, Coordinator, the orchestrate entrypoint
(`/wip:orchestrate`; ADR-0012), and any future `wip spawn` helper —
request a **Tier**: a capability level, not a runtime tool id. The
backend owns the mapping. This is the abstract
substrate row from
[`templates/glossary/orchestration.md`](../templates/glossary/orchestration.md);
it exists so role files and porcelain verbs never hardcode
backend-specific ids that change as the substrate's tool inventory
shifts.

| Tier | Intent |
|---|---|
| `small` | Fast / cheap / narrow — control-plane chatter, quick reads, straightforward routing. |
| `medium` | Standard execution — typical Builder work, low-risk Researcher consults. |
| `large` | Strongest available — workplan production, load-bearing or hard-to-reverse implementation paths, repeated-failure escalations. |

## Per-Role default Tiers

| Role | Default | Escalate to | When to escalate |
|---|---|---|---|
| **Orchestrator** | `small` | — | Orchestrator does not write code; control-plane chatter does not need a stronger tier. |
| **Coordinator** | `small` | `medium` | When handling active escalations/retries (judgment-heavy routing). |
| **Researcher** | `large` | — | (May be relaxed to `medium` for low-risk, narrow-decision steps.) |
| **Builder** | `medium` | `large` | Load-bearing/high-risk surfaces, novel or hard-to-reverse paths, repeated same-shape failures (see below). |

## Tier-escalation guardrails

**Repeated same-shape failures.** If a Builder hits the same failure
shape twice on `medium`, the next attempt is either `large` or an
escalation up to the Coordinator. Do not retry the same shape on
`medium` a third time.

**Load-bearing surfaces — start at `large`.** For Builder tasks that
touch:

- data-store / async-runtime boundaries
- file watching, debouncing, scheduling
- MCP protocol behavior (or any cross-process protocol surface)
- cross-cutting refactors
- schema, migration, or indexing changes
- novel or hard-to-reverse implementation paths

…start at `large`. The cost of a stronger model is reliably cheaper
than debugging subtle orchestration or runtime failures later.

## Manifest override

The active backend may honor a per-project Tier policy in `.wip.yaml`
under `features.<backend>.agent_tier_policy`. The standard knob is
`force_tier`: when set, every spawn binds to that Tier regardless of
the Role's preference (the Role's preference is still recorded as the
`selection_reason` for audit).

Example: this repo runs `features.solo.agent_tier_policy.force_tier:
large` — every spawn under the Solo backend resolves to the largest
available runtime, regardless of which Role requested it.

The override is **policy, not surface**: it changes what the resolver
returns, not what callers ask for. Callers always request a Tier; the
backend always decides whether the project forces a single one.
