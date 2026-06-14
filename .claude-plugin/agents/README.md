# `.claude-plugin/agents/` — orchestration role bindings (step-12)

Reserved for the orchestration Roles bindings (Orchestrator / Coordinator /
Researcher / Builder). The Role behaviors and the abstract Tier policy live
under [`roles/`](../../roles/); this directory holds the *plugin-side*
glue that exposes them as Claude Code agents.

🚧 **Empty in v1 (step-11).** Files land in **step-12 — Roles set**. Gated
on `features.orchestration.enabled` per
[ADR-0007](../../engineering/decisions/0007-orchestration-backend-seam.md).
