# 0005 — "Roles" vs "Playbook"

- Status: accepted
- Date: 2026-06-12
- Source: shape-alignment discussion (2026-06-12)

## Context

"Playbook" was overloaded: `workflow-portable-stub/playbook/` held actor behavior specs
(orchestrator/coordinator/…), while the everyday meaning — and the symfony `playbook/`
project — used it for a specific piece of work's execution plan.

## Decision

- **Roles** = the behavioral operating-manuals for actors (Orchestrator, Coordinator,
  Researcher, Builder). They are tooling, **shipped by the `wip` plugin**, gated on
  `features.solo.enabled`; a repo references them, it does not copy/maintain them.
- **Playbook** = an Initiative's executable plan (its Roadmap + Workplans), under
  `.wip/initiatives/<slug>/`, gitignorable.

## Consequences

- Frees "playbook" for its everyday meaning (matches the user's mental model).
- Roles ship centrally → kills the copy-drift seen in hand-vendored playbooks (the
  prtend `CLAUDE.md ≡ AGENTS.md` failure mode generalized).
- Role definitions live in `templates/glossary/solo.md`; role content in `roles/`.
