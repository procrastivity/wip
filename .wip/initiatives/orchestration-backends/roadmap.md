# Roadmap — orchestration-backends

The plan of record for Orchestration Backends. Brief: [`BRIEF.md`](./BRIEF.md). Locked
decisions graduate to `engineering/decisions/` (ADRs) as soon as they lock;
this roadmap holds the plan; each Step gets a
`workplans/step-NN-<slug>.md` when it starts.

Started: 2026-06-23.

---

## Round 1 — A second backend, and honesty about the first

Make orchestration runnable without Solo and tell the user when Solo is down.
Shipping criterion for the round: with `features.orchestration.backend: task` a
Step runs end-to-end on native subagents (no Solo MCP calls), and `/wip:status`
warns + offers a fallback when Solo is enabled but unreachable.

- **step-01 — active-backend indirection seam** (small) — Introduce generated
  `roles/backends/active.md` (default = `solo.md`), repoint the four
  `agents/*.md` `@`-includes to it, make `commands/orchestrate.md` prose
  backend-agnostic, and add a `wip-plumbing` regen + idempotent
  `orchestrate backend <name>` switch verb. No behavior change — the Solo path
  stays byte-for-byte identical (`active.md == solo.md`). Enables step-03's
  fallback switch.
- **step-02 — Task-tool backend binding** (large) — Author
  `roles/backends/task.md` (substrate bound to files: ledger + shared note on
  disk, Researcher re-invoked statelessly; idle-timer/liveness/tier-ladder
  marked N/A under the synchronous-spawn model) + `templates/glossary/task.md`
  + one glossary selector row. A Step runs end-to-end on native subagents.
  Lands ADR-0013 (second backend + the `active.md` indirection).
- **step-03 — Solo liveness check + fallback offer** (small) — Add a bash
  reachability probe (`solo status --json`, guarded by `command -v solo`) to
  `wip-plumbing status`, surfacing a real `solo_reachable` signal; `/wip:status`
  renders "Solo enabled but unreachable" and offers to switch to the Task
  backend via step-01's verb (never automatic). Lands ADR-0014 (bash liveness
  probe; warn-and-offer).

---

## Deferred (decided-not-now)

_Items consciously postponed; keep the why so future-you can re-evaluate._

- **Duo backend** — a `roles/backends/duo.md` with native tier→runtime
  selection. Tiers are Duo's concept; tracked separately (see distillation
  backlog `duo-agent-tier-selection`).
- **`tier → model` map for the Task backend** — small→haiku / medium→sonnet /
  large→opus via the Agent tool's `model` override. Marked N/A in v1; pull in
  if a real tiered native run needs it.

## Backlog (cross-cutting; see also `.wip/backlog.md`)

_Cross-cutting work that hasn't earned a round yet._
