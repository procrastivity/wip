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
under `features.<backend>.agent_tier_policy`. Two sibling knobs live
there:

- `force_tier`: when set, every spawn binds to that Tier regardless of
  the Role's preference (the Role's preference is still recorded as the
  `selection_reason` for audit).
- `fallback_tool`: a per-project **default tool**, consulted **only**
  when Tier resolution is **non-confident** — the backend's resolver
  found zero candidates for the requested Tier, or candidates it cannot
  disambiguate. It holds a tool **name** (never a runtime id — ids are
  operational and change as the substrate's tool inventory shifts); the
  backend resolves that name to its concrete runtime. When resolution
  **is** confident, `fallback_tool` is ignored.

Example: this repo runs `features.solo.agent_tier_policy.force_tier:
large` — every spawn under the Solo backend resolves to the largest
available runtime, regardless of which Role requested it.

The override is **policy, not surface**: it changes what the resolver
returns, not what callers ask for. Callers always request a Tier; the
backend always decides whether the project forces a single one.

### Non-confident resolution: the fallback ladder

When a backend's resolver cannot confidently map the requested Tier to
a runtime, it consults the following sources in **precedence order**
(first match wins). This is the abstract decision rule; the concrete
mechanism each rung uses (where a pin is stored, how a request carries
a tool, the manifest read) lives in the active backend's binding under
[`backends/`](./backends/).

1. **Request pin** — a tool named on the **spawn request** itself (a
   per-invocation override). Highest precedence. The resolved tool is
   also recorded as the session pin (rung 2) so it governs the rest of
   the run.
2. **Session pin** — a tool **chosen once** and applied to **every**
   spawn for the remainder of the run. This is the durable propagation
   channel: a choice made by the request pin (rung 1) or the ask (rung
   4) reaches all downstream spawns without threading it through
   prompts.
3. **Configured fallback** — the project's `fallback_tool` (above). The
   per-project permanent fix: set once, every non-confident resolution
   honors it.
4. **Ask the human, then pin** — the interactive last resort, reached
   only when rungs 1–3 are empty **and a human is present**. The live
   human-facing Role performs the ask (a Role file is inert text; the
   agent following it prompts), then writes the answer as the session
   pin (rung 2) so the one choice applies to this spawn and all future
   spawns this session. It may also offer to persist the choice as the
   project's `fallback_tool`.
5. **Hard-fail** — reached only when resolution is non-confident, rungs
   1–3 resolved nothing, **and** the session is **non-interactive** (no
   human to ask at rung 4). The backend never silently falls back to an
   arbitrary tool.

This ladder is a **backend capability**: a backend with native Tier
support may own resolution end-to-end and never engage it. It exists so
a backend without native Tier resolution stays usable — with a request
pin, with `fallback_tool`, or with an interactive ask — instead of
mis-routing silently or failing blindly. See [`backends/`](./backends/)
for whether and how the active backend binds it.
