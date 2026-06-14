# `.claude-plugin/agents/` — orchestration role bindings

Plugin-side agent definitions for the wip orchestration Roles. Gated on
`features.orchestration.enabled` per
[ADR-0007](../../engineering/decisions/0007-orchestration-backend-seam.md).

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
framing plus `@`-file references into [`roles/`](../../roles/) for the
canonical behavior content. Role behavior is **never** duplicated here
— if you find yourself tempted to inline a rule, edit the role file
instead. This mirrors the step-11 prompt-sharing seam (plugin commands
fetch shaper prompts from `templates/prompts/intake/` via
`wip-plumbing template show`) one layer up.

The active orchestration backend binding is referenced via
`@../../roles/backends/solo.md` (ADR-0007 makes `solo` the default and
only backend today; a second backend would warrant per-backend agent
variants or a backend-selector hop here).
