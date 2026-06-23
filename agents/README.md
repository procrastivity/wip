# `agents/` — orchestration role bindings

Plugin-side agent definitions for the wip orchestration Roles. Gated on
`features.orchestration.enabled` per
[ADR-0007](../engineering/decisions/0007-orchestration-backend-seam.md).

## Agents

| File | Agent name | Role |
|---|---|---|
| [`orchestrator.md`](./orchestrator.md) | `wip-orchestrator` | Human-facing control plane |
| [`coordinator.md`](./coordinator.md)   | `wip-coordinator`  | Drives one Step end-to-end |
| [`researcher.md`](./researcher.md)     | `wip-researcher`   | Workplan production + sidecar consults |
| [`builder.md`](./builder.md)           | `wip-builder`      | Ephemeral task executor |

## Single source of truth

Each agent file is a **thin pointer**: front-matter declares the agent
to Claude Code (name, description, tools); the body is one sentence of
framing plus `@`-file references into [`roles/`](../roles/) for the
canonical behavior content. Role behavior is **never** duplicated here
— if you find yourself tempted to inline a rule, edit the role file
instead. This mirrors the step-11 prompt-sharing seam (plugin commands
fetch shaper prompts from `templates/prompts/intake/` via
`wip-plumbing template show`) one layer up.

The active orchestration backend binding is referenced via
`@../roles/backends/active.md` — a **generated** pointer regenerated from
`roles/backends/<backend>.md` by `wip-plumbing orchestrate backend <name>`
(default `solo`; `task` is the second backend per ADR-0013). The
indirection exists because Claude Code `@`-includes are static — the agent
files cannot conditionally select a backend — so `active.md` is the single
swappable seam (committed, regenerated on switch, like `.wip/GLOSSARY.md`).
Per-project switching assumes a vendored install (`features.orchestration.source:
vendored`); a shared `source: plugin` install switches the plugin's `active.md`
globally.
