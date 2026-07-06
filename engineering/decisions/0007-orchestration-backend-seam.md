# 0007 — Orchestration is a capability with pluggable backends

- Status: accepted
- Date: 2026-06-13
- Source: shape-alignment discussion (2026-06-13); ADR-0005, ADR-0006

## Context

ADR-0005 gave `wip` the Roles (Orchestrator/Coordinator/Researcher/Builder) and gated them on
`features.solo.enabled`. That welds the *concept* of orchestration to one backend — Solo — in
three places: the naming ("Solo orchestration Roles"), the feature gate, and `templates/glossary/
solo.md`, which bundles backend-agnostic Role behavior with Solo-specific substrate primitives.

But Solo is one *style* of orchestration. The same Roles workflow could run on a native harness
(e.g. `claude` subagents) or another runtime. Two ideas from `workflow-portable-stub/` are already
backend-agnostic and worth protecting: the **Role behaviors** (what an actor does, how decisions
flow) and the **tier abstraction** (request `small`/`medium`/`large`, never hardcode
`agent_tool_id`). The manifest even half-anticipates this — `.wip.yaml` carried a `playbook:`
feature ("the Roles feature") distinct from the `solo:` block — but the ADRs and glossary
contradicted it. `roles/` is still an unwritten stub, so this is the cheapest moment to set the
shape before distillation (roadmap step-12) bakes the coupling in.

## Decision

Orchestration is a **capability** with **pluggable backends**. Two explicitly separated layers:

- **Orchestration capability** — the Roles, actor topology, invariants, verb→Role mapping; Tier
  *semantics* (`small`/`medium`/`large`); the abstract coordination surface ("task ledger"); and
  the Roadmap-is-plan-of-record / ledger-is-live-mirror split. Backend-agnostic. Gated on
  `features.orchestration.enabled`. Vocabulary: `templates/glossary/orchestration.md`. Behavior:
  `roles/` (behavior files + `tier-policy.md`).
- **Backend binding** — the concrete realization: substrate primitives (Process/Todo/Scratchpad/
  Timer), Tier→`agent_tool_id` resolution, MCP tool names (`mcp__solo__*`), identity (`whoami`).
  One swappable surface per backend. Selected by `features.orchestration.backend` (`solo` today).
  Vocabulary: `templates/glossary/solo.md`. Behavior binding: `roles/backends/solo.md`.

**Solo is the first and default backend**, not the capability itself. A future backend is added by
writing one glossary partial + one `roles/backends/<name>.md` — Role behaviors, capability
vocabulary, and this rationale stay untouched.

This **supersedes the gating clause of ADR-0005**: Roles are gated on `features.orchestration.
enabled`, not `features.solo.enabled`.

## Consequences

- Adding a second orchestration style touches zero behavior/capability files — only a new backend
  binding. The "Roles == Solo" assumption no longer hardens as `roles/` is authored.
- ADR-0006 stays intact and `roles/` stops being its special exception: `wip` owns the
  orchestration *behavior* (genuinely its IP) and the *seam*; the *backend* is the external tool it
  binds to — exactly the seams-not-tools rule.
- `.wip.yaml` gains an `orchestration:` block (`enabled` + `backend`); the stale `playbook:` feature
  is retired. `agent_tier_policy` stays under the backend, since Tier→`agent_tool_id` is
  backend-specific.
- Cost is paid in docs + manifest schema now; no second backend is built (deferred).

## Amendment — the request axis is role, not tier (ADR-0025)

Amended 2026-07-05 (`role-centric-runtime-selection` initiative, Round 1 step-01; ADR-0025).

The capability layer's request-axis vocabulary changes from **Tier semantics**
(`small`/`medium`/`large`) to **role** (Orchestrator / Coordinator / Researcher / Builder):
each backend now maps `role → its own runtime` rather than resolving a tier. Duo retired its
own token-classification tier model for explicit presets with no compat layer, so a tier axis
no longer has a home downstream (see [`../notes/duo-tiers-to-presets.md`](../notes/duo-tiers-to-presets.md)).
Everything else here stands: the two-layer split (capability vs backend binding), the
`features.orchestration.{enabled,backend}` gate, and `agent_tier_policy` living under the
backend. See ADR-0025 for the role model, the per-role escalation target, and the Duo backend.
