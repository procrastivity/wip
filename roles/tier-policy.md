# Role Policy

Per-Role runtime selection + escalation. Backend-agnostic — a spawn requests
a **Role** (`Orchestrator` / `Coordinator` / `Researcher` / `Builder`); the
active orchestration backend resolves each Role to a concrete runtime. See
[`backends/`](./backends/) for the resolver.

> **Filename note.** This file is named `tier-policy.md` for historical reasons
> — it is the stable `@`-include path referenced by the plugin agents. Its
> content is the **role policy**: the old tier (`small`/`medium`/`large`)
> capability axis was retired in ADR-0025 in favor of role-centric selection.
> The eventual file rename is a cross-cutting cleanup deferred to the backend
> rounds (it touches the agents, setup mirrors, commands, and the flatten lib).

## Request a Role, not a runtime id

Callers — Orchestrator, Coordinator, the orchestrate entrypoint
(`/wip:orchestrate`; ADR-0012), and any future spawn helper — request by
**Role**. The backend owns the mapping from Role to runtime. This is the
abstract substrate row from
[`templates/glossary/orchestration.md`](../templates/glossary/orchestration.md);
it exists so role files and porcelain verbs never hardcode backend-specific ids
that change as the substrate's tool inventory shifts.

Role is the **only** selection signal — the shared contract carries no separate
capability/tier hint (ADR-0025). "How strong a runtime" is expressed entirely
through which Role (and, on escalation, which escalation target) is requested;
the backend's config decides what each Role maps to.

## Per-Role assignment + escalation target

Each Role has a **default assignment** — the runtime the backend uses for that
Role — and an optional **escalation target**: a stronger, opt-in second
assignment the Coordinator switches to on repeated same-shape failure or for
load-bearing surfaces. The concrete assignments live in the active backend's
config (`.wip.yaml` under `features.<backend>`; see [`backends/`](./backends/));
this table is the abstract policy.

| Role | Default assignment | Escalation target | Notes |
|---|---|---|---|
| **Orchestrator** | Role default | — | Orchestrator does not write code; control-plane chatter needs no stronger runtime. |
| **Coordinator** | Role default | escalated | Switches to its escalation target when handling active escalations / retries (judgment-heavy routing). |
| **Researcher** | Role default | — | Workplan production is load-bearing, so the operator maps `researcher` to a capable runtime; there is no automatic escalation above it. |
| **Builder** | Role default | escalated | Switches to its escalation target for load-bearing / high-risk surfaces, novel or hard-to-reverse paths, and repeated same-shape failures (see below). |

A Role with no explicit assignment falls through to the backend's `default`
entry. An escalation target is named per Role (e.g. a `builder` assignment and a
`builder-escalated` assignment) — see [`backends/`](./backends/) for the
concrete key shape in the active backend.

## Escalation guardrails

**Repeated same-shape failures.** If a Builder hits the same failure shape twice
on its default assignment, the next attempt is either the Builder's **escalation
target** or an escalation up to the Coordinator. Do not retry the same shape on
the default assignment a third time.

**Load-bearing surfaces — start escalated.** For Builder tasks that touch:

- data-store / async-runtime boundaries
- file watching, debouncing, scheduling
- MCP protocol behavior (or any cross-process protocol surface)
- cross-cutting refactors
- schema, migration, or indexing changes
- novel or hard-to-reverse implementation paths

…start at the Builder's **escalation target** rather than its default. The cost
of a stronger runtime is reliably cheaper than debugging subtle orchestration or
runtime failures later.

## Manifest policy

The active backend may honor a per-project Role → runtime policy in `.wip.yaml`
under `features.<backend>`. Two abstract knobs:

- **A `default` assignment** — the fallback runtime for any Role without an
  explicit assignment. Setting only `default` pins every Role to one runtime
  (e.g. an Opus-only project); adding per-Role entries overrides it selectively.
- **A configured fallback** — a per-project default tool consulted **only** when
  resolution is **non-confident** (the backend cannot confidently map the
  requested Role to a runtime). It holds a tool **name** (never a runtime id —
  ids are operational and shift as the substrate's tool inventory changes); the
  backend resolves that name to its concrete runtime.

The concrete config keys are the active backend's — see
[`backends/`](./backends/). This section is **policy, not surface**: it changes
what the resolver returns, not what callers ask for. Callers always request a
Role; the backend decides how the project's config binds it.

### Non-confident resolution: the fallback ladder

When a backend's resolver cannot confidently map the requested Role to a
runtime, it consults the following sources in **precedence order** (first match
wins). This is the abstract decision rule; the concrete mechanism each rung uses
(where a pin is stored, how a request carries a tool, the manifest read) lives
in the active backend's binding under [`backends/`](./backends/).

1. **Request pin** — a tool named on the **spawn request** itself (a
   per-invocation override). Highest precedence. The resolved tool is also
   recorded as the session pin (rung 2) so it governs the rest of the run.
2. **Session pin** — a tool **chosen once** and applied to **every** spawn for
   the remainder of the run. This is the durable propagation channel: a choice
   made by the request pin (rung 1) or the ask (rung 4) reaches all downstream
   spawns without threading it through prompts.
3. **Configured fallback** — the project's configured fallback tool (above). The
   per-project permanent fix: set once, every non-confident resolution honors
   it.
4. **Ask the human, then pin** — the interactive last resort, reached only when
   rungs 1–3 are empty **and a human is present**. The live human-facing Role
   performs the ask (a Role file is inert text; the agent following it prompts),
   then writes the answer as the session pin (rung 2) so the one choice applies
   to this spawn and all future spawns this session. It may also offer to
   persist the choice as the project's configured fallback.
5. **Hard-fail** — reached only when resolution is non-confident, rungs 1–3
   resolved nothing, **and** the session is **non-interactive** (no human to ask
   at rung 4). The backend never silently falls back to an arbitrary tool.

This ladder is a **backend capability**: a backend that owns Role → runtime
resolution end-to-end (e.g. a backend that delegates to an external selector)
may never engage it. It exists so a backend without native resolution stays
usable — with a request pin, with a configured fallback, or with an interactive
ask — instead of mis-routing silently or failing blindly. See
[`backends/`](./backends/) for whether and how the active backend binds it.
