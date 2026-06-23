# Orchestration Backends — BRIEF (single source of truth)

> The brief is the durable context for this initiative. Every later artifact
> (roadmap, workplans, intake amendments) reads this first. If a cross-cutting
> decision changes, it changes **here**.

- Slug: `orchestration-backends`
- Started: 2026-06-23

## Goal

Make the wip orchestration flow runnable **without Solo**. Today the
Orchestrator → Coordinator → Researcher/Builder loop is hard-bound to the Solo
MCP server: spawning, identity, and all live coordination state (ledger,
scratchpad, KV, locks, timers, liveness) go through `mcp__solo__*`. This
initiative ships a **second orchestration backend bound to the built-in Task
tool + native subagents**, plus an honest Solo **liveness check** that, when
Solo is configured but unreachable, warns the user and offers to fall back to
the Task backend. Outcome a teammate can repeat: "wip can orchestrate on native
agents when Solo isn't there, and it tells you when Solo is down."

## Confirmed decisions (do not relitigate)

- **Standalone initiative.** This is its own initiative (`orchestration-backends`),
  not a new Round on `distillation` — orchestration backends are a distinct,
  ongoing concern (the `distillation` backlog already gestured at this).
- **Assembled active-backend file.** Agents `@`-include
  `roles/backends/active.md`; `wip-plumbing` regenerates it from
  `roles/backends/<backend>.md` (same pattern as the generated, committed
  `.wip/GLOSSARY.md`). This resolves the one gap in ADR-0007's "two new files"
  promise — Claude Code `@`-includes are static, so the four `agents/*.md`
  files cannot conditionally pick a backend; the indirection file does it.
- **Warn + offer, never auto-switch.** When Solo is enabled in config but the
  liveness probe fails, `/wip:status` surfaces it and *offers* to switch to the
  Task backend. Switching is always an explicit user choice.
- **Synchronous-spawn model.** Native Task subagents are blocking one-shot
  calls, so the Solo idle-timer / liveness-gate / pause-resume machinery is
  N/A for the Task backend; the cost moves to **state durability** — ledger,
  shared note, and the "long-lived Researcher" all become files (the Researcher
  is re-invoked statelessly, re-hydrated from those files).
- **Liveness is a bash probe.** The standalone `solo` CLI (`solo status
  --json`) gives `wip-plumbing status` a deterministic reachability signal; the
  `mcp-cli` anti-pattern in `roles/backends/solo.md` does not cover this CLI.

## Constraints

- **ADR-0007 seam holds.** Role behavior files (`roles/*.md`) and
  `roles/tier-policy.md` stay backend-agnostic — adding the Task backend must
  touch zero behavior/capability files (the `test/test-roles-backend-seam.sh`
  acceptance test must stay green).
- **Solo flow unchanged.** After the indirection seam lands, the existing Solo
  path must be byte-for-byte unchanged (`active.md == solo.md` on a Solo
  install).
- **Plugin-vs-vendored load paths.** `@`-includes resolve relative to the
  plugin install dir; vendored installs regenerate their own `active.md`.
  `features.orchestration.source` (`plugin|vendored`) already distinguishes
  these.
- Non-goal: improving Solo's tier resolution or building a Duo backend (Duo
  agent-tier selection stays deferred to its own track).

## Open questions

- Exact on-disk paths/format for the Task-backend ledger & shared note
  (single run-dir vs per-step files) and their gitignore treatment under
  `.wip/`.
- Whether a `tier → model` map (small→haiku, medium→sonnet, large→opus, via
  the Agent tool's `model` override) is worth shipping in v1 or deferred (mark
  N/A initially).
- Whether the backend-switch affordance also belongs in `/wip:start`'s "go"
  branch, or `/wip:status` only.
