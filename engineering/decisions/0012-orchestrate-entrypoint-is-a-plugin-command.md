# 0012 — The orchestrate entrypoint is a plugin command, not a CLI verb

- Status: accepted
- Date: 2026-06-16
- Source: build brief `.wip/scratch/orchestrate-verb-brief.md` (2026-06-16); ADR-0001,
  ADR-0005, ADR-0007; `commands/start.md` (commit `ff99f46`, role-aware on-`go` hand-off)

## Context

`/wip:start` activates a roadmap step and, on `go`, offers two ways to run it: **solo
here**, or **orchestrate** — become the Orchestrator (`roles/orchestrator.md` +
`roles/backends/solo.md`) and spawn a Coordinator for the active step via the backend. The
"orchestrate" branch re-describes that boot inline. The roadmap (step-12 retro) and
`roles/tier-policy.md` both anticipated a future **`wip orchestrate`** verb as the
ergonomic wrapper for exactly that branch.

But a literal CLI `orchestrate` verb runs into the layer rule head-on. Booting
orchestration means **spawning Claude agent processes**. Under the only backend that exists
(Solo, ADR-0007) spawning happens through MCP tools — `mcp__solo__spawn_process`,
`mcp__solo__rename_process`, the timer/todo/scratchpad surface — which are reachable **only
from inside Claude Code**. Neither the deterministic bash core (`wip-plumbing`) nor the
OpenAI-compatible porcelain (`wip`) can call them. So a `wip orchestrate` verb that "fans
work across agents" is not a thin wrapper over plumbing — it is architecturally impossible
at the plumbing/porcelain layers. ADR-0001's seam ("pure function of files+git → plumbing;
needs prose/choice/composition → porcelain") and ADR-0007's behavior/backend split already
locate the fan-out where it belongs: in the Roles, driven by an agent that holds the
backend's MCP tools.

## Decision

The **orchestrate entrypoint is `/wip:orchestrate`** — a Claude Code **plugin command** —
not a `wip` / `wip-plumbing` CLI verb. There is **no** `wip orchestrate` (or `wip spawn`)
fan-out verb; the spawning lives where the MCP tools live. Plumbing contributes one
deterministic, LLM-free **prep** verb that the plugin command consumes first — the same
shape as `/wip:start` shelling out to `workplan init --activate` for facts + writes.

- **`/wip:orchestrate [--initiative <slug>]`** (plugin) — resolves the bundled
  `wip-plumbing` (the `CLAUDE_PLUGIN_ROOT` idiom), calls `orchestrate prep`, surfaces the
  brief, then **becomes the Orchestrator** (`@roles/orchestrator.md` +
  `@roles/backends/solo.md`): confirms identity via the active backend and spawns a
  Coordinator (and, per the Roles, its Researcher) for the active step. The command body
  names **no** backend tool — it points at the Roles, which carry the backend binding
  (ADR-0007). It is the ergonomic wrapper for `/wip:start`'s on-`go` "Orchestrate" branch,
  which now hands off to it rather than re-describing the boot inline.

- **`wip-plumbing orchestrate prep [--initiative <slug>]`** (new plumbing verb) — a pure
  function of `.wip.yaml` + the initiative's `roadmap.md` + disk. Resolves the initiative
  and its `active_step`, **gates** orchestration readiness, and emits the "what to
  orchestrate" brief: `initiative`, `orchestration {enabled, backend}`, `round`,
  `active_step {id, title, shipped, lane}`, `workplan {path, exists}`, `roadmap`,
  `signals`. It **never spawns** and **never names a backend tool** — it emits the *facts*
  about the work, not the *staffing* of it (Tier, process names, and `agent_tool_id` stay
  in the Roles + backend binding). Exit codes carry the gate: orchestration feature
  disabled → **3** (`orchestration-not-enabled`); no `active_step` set → **4**
  (`no-active-step`, "run `/wip:start` first"); `active_step` not in the roadmap → **4**
  (`step-not-in-roadmap`). A missing workplan is **not** an error — it reports
  `workplan.exists: false` and exits 0, because the Coordinator's Researcher *produces* the
  workplan in Phase 1 (Roles).

### Why a dedicated prep verb (not just compose `detect` + `status`)

The readiness gate — feature-enabled (exit 3), active-step-set / in-roadmap (exit 4) — is
exactly the exit-code discipline ADR-0001 puts in plumbing, and it is the **one** slice of
`/wip:orchestrate` that can be unit-tested with no LLM and no MCP. Owning it in a verb
maximizes the deterministic, test-pinned surface and keeps the (inherently untestable)
spawn thin. `status` does not emit the workplan path and does not gate on the orchestration
feature; rather than teach the command body to stitch two envelopes and re-derive the
gates, one honest verb owns the contract.

## Consequences

- The `wip orchestrate` / `wip spawn` language in `roles/tier-policy.md` and the
  `orchestrate`/`spawn` line in the `wip-plumbing-cli.md` non-goals are superseded: the
  orchestrate entrypoint is `/wip:orchestrate`; plumbing's only orchestration surface is
  `orchestrate prep`. (`wip spawn` — a single-agent helper — may still arrive later, but it
  too would be a plugin/MCP concern, not a bash verb.)
- New deterministic verb (`lib/wip/wip-plumbing-subcommands/orchestrate.bash`, wired into
  the dispatcher + usage) + plugin command (`commands/orchestrate.md`); the contract lands
  in [`engineering/specs/wip-orchestrate.md`](../specs/wip-orchestrate.md).
- The backend seam (ADR-0007) holds: `orchestrate prep` emits a `backend` *name* but no
  `mcp__solo__*` tool; the command body defers all spawning to the Roles, whose Solo
  binding stays isolated in `roles/backends/solo.md`.
- Additive and reuse-first: `orchestrate prep` leans on the existing roadmap parser and
  manifest helpers; `/wip:orchestrate` reuses the shipped Roles/agents unchanged. Nothing
  about the Roles or the backend binding moves.
- Tests: `test/test-orchestrate-prep.sh` (the plumbing verb's JSON shape + exit-code gate,
  network/MCP-free) and an `/wip:orchestrate` case in `test/test-plugin-manifest.sh`.

## Amendment — the prep gate gains backend-reachability (ADR-0025)

Amended 2026-07-05 (`role-centric-runtime-selection` initiative, Round 1 step-01; ADR-0025).

`wip-plumbing orchestrate prep`'s readiness gate gains a **backend-reachability** check for
the active backend. This is the seam through which the **Duo backend** hard-errors at
preflight when `features.orchestration.backend: duo` but Duo is not installed/reachable —
never a silent fall-back to Solo (ADR-0025 §4). The entrypoint decision is otherwise
preserved: `/wip:orchestrate` stays a plugin command, `orchestrate prep` stays a
deterministic, LLM-free verb that emits a backend *name* but no backend tool. The
Solo-liveness precedent for a warn-and-offer probe is ADR-0014; the Duo probe differs in
that a Duo run hard-errors rather than offering a fallback.
