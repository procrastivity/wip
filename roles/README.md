# roles/ — Solo orchestration Roles

Behavioral operating-manuals for the agent processes that run orchestrated work:
**Orchestrator**, **Coordinator**, **Researcher**, **Builder**. These are **tooling**,
shipped by the `wip` plugin and gated on `features.solo.enabled` — a consumer references
them, it does not copy/maintain them (this is what kills the copy-drift seen across
hand-vendored playbooks).

> "Roles" ≠ "Playbook". A Role is *how an actor behaves*; a Playbook is *an initiative's
> plan* (its Roadmap + Workplans, under `.wip/initiatives/<slug>/`). See the glossary.

## Status

🚧 Not yet authored. These will be distilled and path-corrected from
`workflow-portable-stub/playbook/` (a gitignored study slice) — see the distillation
roadmap, Step "Roles". Definitions live in `templates/glossary/solo.md`.

Planned files: `orchestrator.md`, `coordinator.md`, `researcher.md`, `builder.md`,
`shared-static.md`, `agent-tool-selection.md`, `README.md` (index).
