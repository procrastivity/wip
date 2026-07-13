# Duo backend binding

This file binds the Roles capability to the **Duo** orchestration backend.
Duo is a spawner **layered on Solo**: it launches Solo agent processes, but
picks *which* runtime to launch from user-configured **presets** (label →
`{agent_tool_id, extra_args, provider}` definitions) instead of wip resolving a
tool itself. The Role behavior in the sibling files
([`orchestrator.md`](../orchestrator.md), [`coordinator.md`](../coordinator.md),
[`researcher.md`](../researcher.md), [`builder.md`](../builder.md),
[`shared.md`](../shared.md), [`tier-policy.md`](../tier-policy.md)) is
backend-agnostic; **this is the only file that names Duo tools.**

Active when `.wip.yaml` has `features.orchestration.backend: duo`.

Authoritative Duo facts (presets, providers, `extra_args`, the exact MCP + CLI
surface, preset selection) live in
[`engineering/notes/duo-tiers-to-presets.md`](../../engineering/notes/duo-tiers-to-presets.md);
the decision to add this backend is ADR-0025.

## The delegation model (read this first)

Duo owns **runtime selection only**. wip requests by **Role**; the binding maps
`role → Duo preset name` and calls Duo to launch it. **Duo** — not wip — then
picks the concrete `agent_tool_id`, applies `extra_args`, honors provider
enable/disable, spreads across a preset's multiple definitions, and selects one
at random among the enabled ones. wip never sees an `agent_tool_id` and never
classifies a tool; it names a preset and Duo does the rest.

Because Duo launches **Solo** agent processes, everything downstream of the
spawn is the **Solo backend's** substrate: identity (`whoami`), the task ledger
(Todos), the shared note (Scratchpad), idle timers, KV, the liveness signal, and
the operator-engagement guard all bind exactly as in
[`solo.md`](./solo.md) §Substrate bindings / §Liveness signal /
§Operator-engagement guard. This binding does **not** restate them — it changes
**only** how an agent is spawned. Read [`solo.md`](./solo.md) for the shared
substrate; read on here for the one thing Duo replaces.

## Identity & process naming

Unchanged from [`solo.md`](./solo.md): a launched agent is a Solo process, so it
confirms identity via `mcp__solo__whoami()` and renames itself to its
role-scoped name (`orchestrator`, `<slug>-step-NN-coordinator`, …) exactly as
under the Solo backend. Duo's `launch_agent` accepts an optional `name`, so the
role-scoped name can be supplied at launch time.

## Role resolver — delegate to Duo

Replaces [`solo.md`](./solo.md)'s Role resolver. wip does **not** resolve a tool;
it resolves a **preset name** and hands the launch to Duo.

### Inputs

Required:

- `role`: the requesting Role, or its escalation target (e.g. `builder` vs
  `builder-escalated`). The caller decides which Role key to request; the
  escalation policy is in [`tier-policy.md`](../tier-policy.md).

Optional:

- `name`: process name to use at launch time.
- `avoid_provider`: a soft preference passed through to Duo so a second launch
  can spread onto a different provider than a prior one (e.g. launch the
  Researcher on a different provider than the Builder).

### role → preset mapping

**Identity by default.** The preset name **is** the Role key: `builder` →
preset `builder`, `researcher` → preset `researcher`, `builder-escalated` →
preset `builder-escalated`. Duo's presets are already role-flavored, so no
configuration is needed in the common case.

An optional override map handles name mismatches — `.wip.yaml`
`features.duo.presets` maps a Role key to a differently-named Duo preset:

```yaml
features:
  duo:
    presets:
      builder-escalated: builder-strong   # Role key → Duo preset name
      # roles not listed use the identity mapping (role name == preset name)
```

Resolution: look up `features.duo.presets[<role>]`; if the Role has no override,
use the Role key itself as the preset name (identity). There is **no `default`
fallback here** — Duo owns its own fallback: a requested preset with zero enabled
definitions falls back to Duo's `default` preset, and an empty `default` is a
clear Duo error (see the reference note §2.4).

### Launch

There is exactly **one** way to start an agent under this backend:

`mcp__duo__launch_agent(preset, name?, avoid_provider?, extra_args?)` resolves and
launches in one call, returning `{process_id, name, preset, agent_tool_id,
extra_args[], provider|null}`.

Any caller-supplied `extra_args` are appended **after** the preset's own
`extra_args` (preset-first, caller-second). Provider load-spreading and the
random pick among a preset's enabled definitions are **Duo's** to make — wip does
not second-guess them.

**Spreading a second launch onto a different provider.** `launch_agent` always
reports the `provider` it actually used (possibly `null`), so chaining needs
nothing but the launch itself: launch the first agent, read `provider` off its
result, and pass that value as `avoid_provider` on the second launch (e.g. put the
Researcher on a different provider than the Builder). `avoid_provider` is a **soft**
preference — if it cannot be honored Duo relents and launches anyway, reporting
`relented_on_avoid_provider`, rather than failing.

### What wip does NOT do under this backend

- No `mcp__solo__list_agent_tools` classification or config-map lookup (that is
  the Solo backend's job; here Duo owns selection).
- No `agent_tool_id` handling, no `extra_args` authoring beyond an optional
  pass-through, no provider bookkeeping. Those are **Duo-only** concepts and
  never appear in the backend-agnostic contract (ADR-0025).
- No `--agent`/`fallback_tool` ladder: preset resolution is Duo's, and when Duo
  cannot resolve a preset it returns a structured error naming the preset and the
  disabled providers — surface it; do not silently substitute a tool.
- No resolve-then-spawn. Duo exposes a resolve call, but it is **not** a preflight
  for a launch: resolution picks **at random** among a preset's enabled definitions
  and `launch_agent` re-resolves independently, so a resolved result does not
  predict what the launch will pick — and there is no way to feed one back into
  `launch_agent` anyway. Naming a preset and launching it is the whole contract. An
  operator who wants to inspect resolution uses the Duo CLI (`duo agent resolve
  <preset>`), outside the agent's surface.

## Preflight: Duo must be reachable (hard error)

Unlike the Solo-unreachable path (which warns and **offers** the Task backend —
ADR-0014), a Duo-backed run that cannot reach Duo **hard-errors at preflight**
and does **not** fall back (ADR-0025 §4). Selecting the Duo backend is a
deliberate choice; silently resolving via Solo would mask the misconfiguration
and re-introduce the exact classifier this backend exists to avoid.

The deterministic gate lives in `wip-plumbing orchestrate prep`: when
`features.orchestration.backend: duo`, prep probes Duo reachability (via the
`duo` CLI — `duo whoami --json` resolving a project) and exits **3
`backend-unreachable`** if Duo is not installed or not answering, before any
spawn. `/wip:orchestrate` consumes that gate and stops the run with the error;
it never switches backends automatically. There is no MCP call in the probe —
the deterministic core cannot reach MCP (ADR-0012), so reachability is a bash
CLI probe, mirroring the Solo liveness probe (ADR-0014).

## Provider controls (optional, Duo-only)

Providers are a Duo concept surfaced here only for the Duo backend, never hoisted
into the shared contract. When a subscription is rate-limited mid-run, an
operator can disable it without editing config — Duo reads provider state fresh
on every launch:

- `mcp__duo__list_providers()` → `{providers: [{provider, enabled}]}`.
- `mcp__duo__set_provider_enabled(provider, enabled)` → toggles it.

These two toggles are the operator's escape hatch, not part of the launch path.
Spreading concurrent Builders across providers is done with `avoid_provider` on the
launch itself (§Launch) — a soft preference; disabling a rate-limited provider
outright is done here. Both stay entirely inside this backend.

## Tag glossary

Unchanged from [`solo.md`](./solo.md): the task ledger is Solo Todos, tagged with
the same vocabulary (`roadmap`, `step-NN`, `task`, `needs-human`, `escalation`,
`coordinator-context`).

## Anti-pattern — do not resolve a tool yourself

❌ **Wrong**: calling `mcp__solo__list_agent_tools` and picking an
`agent_tool_id`, or spawning via `mcp__solo__spawn_process(agent_tool_id=N)`,
under the Duo backend.

❌ **Also wrong**: asking Duo to resolve a preset, taking the `agent_tool_id` off
the result, and handing it to `mcp__solo__spawn_process(...)`. This is the same
mistake wearing a Duo-shaped hat: the resolved tool is a random pick that
`launch_agent` would not have reproduced, and spawning it yourself still bypasses
Duo's provider state and load-spreading.

✅ **Right**: name a **preset** and let Duo resolve + launch — in one call.

```
mcp__duo__launch_agent(preset="builder", name="<slug>-step-NN-builder-01")
```

**Why**: the whole point of the Duo backend is that runtime selection —
tool + provider + extra_args + load-spreading — is user-configured in Duo, not
inferred by wip. Reaching past Duo to Solo's spawn defeats it and desyncs from
the operator's preset/provider config.

## Duo control-plane terminology

- **preset** — a Duo label mapping to one or more definitions; the unit wip
  requests. Freeform and unordered (no built-in ranking).
- **definition** — one `{agent_tool_id, extra_args, provider}` inside a preset;
  Duo picks one at random among the enabled ones.
- **provider** — a freeform label (e.g. `anthropic`, `openai`) whose
  enabled/disabled state Duo reads fresh per launch.
- **launch** — resolve a preset and spawn the Solo agent for it, via
  `mcp__duo__launch_agent`.

When in doubt: wip names a **preset**; **Duo** owns everything the preset
resolves to.
