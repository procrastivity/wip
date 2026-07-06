# 0025 — Role-centric runtime selection + Duo orchestration backend

- Status: accepted
- Date: 2026-07-05
- Source: `role-centric-runtime-selection` initiative, Round 1 (step-01); planning
  session 2026-07-05; Duo scratchpad `solo://proj/6/scratchpad/tiers-presets-how-du--107`;
  reference note [`engineering/notes/duo-tiers-to-presets.md`](../notes/duo-tiers-to-presets.md);
  ADR-0007, ADR-0012, ADR-0013
- Amends: **ADR-0007** (capability-layer request axis: *Tier semantics* → **role**),
  **ADR-0012** (`orchestrate prep` gate gains backend-reachability), **ADR-0013**
  (Duo backend deferred → planned; the deferred `tier → model` map becomes `role → model`)

## Context

ADR-0007 made orchestration a capability with pluggable backends and named the request
axis **Tier** (`small`/`medium`/`large`), with each backend resolving a tier to a concrete
runtime. The Solo backend (`roles/backends/solo.md`) resolves a tier by **command-first
token classification** of the Solo agent-tool inventory (`haiku→small`, `sonnet→medium`,
`opus→large`). ADR-0013 shipped the Task backend + the `active.md` indirection and named a
**Duo backend** as deferred, alongside a deferred `tier → model` map for the Task backend.

Two facts now force the axis to change:

1. **Duo retired the same pattern with no compatibility layer.** Duo dropped its own
   token-classification tier model in favor of explicit, user-configured **presets**
   (freeform label → `{agent_tool_id, extra_args, provider}` definitions), plus a provider
   enable/disable toggle and per-launch `extra_args`. Duo has **no tier concept** and **no
   tier-named tools** — you cannot ask Duo "give me a large agent." The authoritative Duo
   facts are captured in the reference note. A wip↔Duo bridge must therefore translate a
   wip request into a preset **name**, and Duo's presets are already role-flavored
   (`builder`, `reviewer`).
2. **wip's Solo classifier is the same coupling Duo walked away from.** It works only
   because the user maintains several Solo tools with model names baked into their command
   strings — the exact "models-in-duplicate-tools" hack Duo deleted.

The thing every backend already knows is the **role** (Orchestrator / Coordinator /
Researcher / Builder). Making the role the request axis aligns wip with Duo's preset model
and removes the fragile model-name-in-command coupling. This ADR records the decision;
execution is staged across Rounds 2–4 of this initiative.

## Decision

Retire tier-as-capability. **The role is the runtime-selection request axis**; each backend
maps `role → its own runtime`. Four sub-decisions:

1. **Role is the only signal.** Retire the `small`/`medium`/`large` request vocabulary. The
   shared, backend-agnostic contract requests a **role**, never a tier and never a runtime
   tool id. The contract keeps **no residual capability hint** — role is the sole axis. This
   **amends ADR-0007**: the capability layer's "Tier semantics" is replaced by role as the
   request axis (`roles/tier-policy.md` becomes a role-policy; `templates/glossary/
   orchestration.md`'s Tier row follows).

2. **Escalation becomes a per-role escalation target.** The old tier-based
   `medium → large` ladder has no direct expression once the capability axis is gone.
   Replace it with an **opt-in, role-scoped escalation target** — a second assignment/preset
   per role (e.g. `builder` → `builder-escalated`) that the Coordinator switches to on
   repeated same-shape failure. In the Solo backend that is a second agent-tool; in the Duo
   backend a second preset name. This preserves the *behavior* (a stronger runtime on retry)
   without a global capability ladder or a residual capability hint.

3. **Config home is `.wip.yaml` under `features.<backend>`.** The `role → runtime` maps
   extend the existing `features.solo.agent_tier_policy` / `features.orchestration`
   precedent — one manifest, no new source of truth:
   `features.solo.agent_tools` (role → agent-tool), `features.task.models` (role → model),
   `features.duo.presets` (role → preset). Each map carries a `default` fallback plus
   per-role overrides plus optional `<role>-escalated` targets. The **general contract stays
   minimal**: providers, presets, `extra_args`, and "multiple definitions" are
   **Duo-backend concepts** and MUST NOT be hoisted into `roles/tier-policy.md`, the shared
   role contract, or the abstract glossary.

4. **The Duo backend delegates runtime selection to Duo.** A new `roles/backends/duo.md`
   (per the ADR-0007/0013 seam — one glossary partial + one backend file + one selector row,
   no role edits) does **not** re-implement selection. It requests by **role**, maps
   `role → Duo preset name` (**identity by default** — presets named after roles — with an
   **optional override map** in `features.duo`), and calls Duo's `launch_agent(preset)` (or
   `resolve_preset` for a dry run). **Duo owns** agent_tool + provider + `extra_args` +
   multiples + random selection + provider load-spreading. When
   `features.orchestration.backend: duo` but Duo is not installed/reachable, a run **hard-
   errors at preflight** (`orchestrate prep` / `doctor`), never silently falling back to the
   Solo backend — a silent fall-back would mask misconfiguration and re-introduce the exact
   classifier this decision retires. This **amends ADR-0012**: `orchestrate prep`'s readiness
   gate gains a backend-reachability check for the active backend.

## Consequences

- **Staging (value before Duo coupling).** The Solo simplification (§3 →
  `features.solo.agent_tools`) and the Task simplification (§3 → `features.task.models`,
  which graduates ADR-0013's deferred `tier → model` map to `role → model`) are **Duo-free**
  and mutually independent — they land as parallel lanes (ADR-0010) in Round 3, before any
  Duo backend exists. The Duo backend (Round 4) is the only piece that couples a run to Duo
  being installed and reachable — an accepted trade-off, isolated behind the backend seam.
- **The backend seam holds.** Adding the Duo backend stays "one glossary partial + one
  `roles/backends/<name>.md` + one selector row" (ADR-0007/0013). The acceptance test
  `test/test-roles-backend-seam.sh` continues to assert that the behavior/role-policy files
  name zero backend tokens — updated deliberately in Round 2 to assert the **role** contract
  (not tier).
- **The `--agent <name|id>` run-pin survives in spirit** (KV `wip/<slug>/agent-pin`) as a
  per-run override of the active backend's `role → runtime` map, unchanged.
- **ADR-0013's deferred items graduate.** "Duo backend" flips deferred → planned (owned by
  Round 4); the deferred Task `tier → model` map becomes the `role → model` map (Round 3).
- Cost at this step is docs + decision only; no code moves in step-01.

## Supersedes / Amends

This ADR **surgically amends** three prior ADRs. Only the clauses named below change;
everything else in those ADRs stands.

- **ADR-0007** — the capability layer's request-axis vocabulary changes from *Tier
  semantics* (`small`/`medium`/`large`) to **role**. The two-layer split (capability vs
  backend binding), the `features.orchestration.{enabled,backend}` gate, and
  `agent_tier_policy` living under the backend are **preserved** (the manifest key may be
  renamed as the Solo binding is simplified in Round 3, but its *location* under the backend
  is unchanged).
- **ADR-0012** — `wip-plumbing orchestrate prep`'s readiness gate gains a **backend-
  reachability** check (the Duo backend hard-errors here when Duo is unreachable). The
  entrypoint decision (`/wip:orchestrate` is a plugin command, plumbing owns a deterministic
  prep verb that names no backend tool) is **preserved**.
- **ADR-0013** — the **Deferred** bullet "a Duo backend" flips to **planned** (Round 4), and
  the deferred "`tier → model` map for the Task backend" becomes the **`role → model`** map
  (Round 3). The Task backend + `active.md` indirection decisions are **preserved**.
