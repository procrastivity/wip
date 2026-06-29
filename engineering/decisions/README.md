# Decisions (ADRs)

Locked, durable decisions behind `wip`, in MADR-minimal form. Graduated from the
`distillation` initiative's Brief / findings / SYNTHESIS.

| ADR | Decision |
|-----|----------|
| [0001](./0001-three-layer-plumbing-porcelain.md) | Three layers: `wip-plumbing` (deterministic) / `wip` (porcelain) / `/wip:*` (plugin) |
| [0002](./0002-wip-yaml-manifest-and-detection.md) | `.wip.yaml` root manifest + sentinel detection contract |
| [0003](./0003-layered-opt-in-vocabulary.md) | Layered, opt-in vocabulary (glossary partials assembled per enabled feature) |
| [0004](./0004-step-not-phase.md) | "Step", not "Phase" |
| [0005](./0005-roles-vs-playbook.md) | "Roles" (plugin-shipped actor manuals) vs "Playbook" (an initiative's plan) |
| [0006](./0006-wip-owns-seams-not-tools.md) | `wip` owns the seams; features ship from their own repos |
| [0007](./0007-orchestration-backend-seam.md) | Orchestration backend seam (`features.orchestration.backend`) |
| [0008](./0008-global-project-registry.md) | Global project registry + `--project` selector |
| [0009](./0009-intake-as-pipeline.md) | Intake is a pipeline, not a verb; closed kind vocabulary |
| [0010](./0010-parallel-lanes-in-roadmaps.md) | Parallel lanes (`### Lane <name>`) as a structural roadmap primitive |
| [0011](./0011-bundle-assembler-porcelain.md) | Bundle assembler is a porcelain verb (`wip bundle` / `/wip:bundle` multi-file front-end) |
| [0012](./0012-orchestrate-entrypoint-is-a-plugin-command.md) | Orchestrate entrypoint is a plugin command (`/wip:orchestrate`), not a CLI verb |
| [0013](./0013-task-tool-orchestration-backend.md) | Task-tool orchestration backend + generated `active.md` backend-indirection |
| [0014](./0014-solo-liveness-bash-probe.md) | Solo liveness is a bash probe (`status --probe-solo`); unreachable → warn + offer Task fallback |
| [0015](./0015-setup-agents-commands-generated-from-plugin.md) | setup-agents command copies are generated-but-committed from the canonical plugin `commands/` |
| [0016](./0016-closeout-write-contract.md) | closeout-write contract: top-level `wip-plumbing ship <slug> <step-id>` marks the step shipped + clears `active_step`, idempotent, two-stub/two-file seam |
| [0017](./0017-test-harness-stay-homegrown-no-bats.md) | Test harness stays on the homegrown bash assert library; do not adopt Bats (parallelism already won, bottleneck is CLI-invocation count not the harness) |
| [0018](./0018-forge-observation-surface.md) | Forge observation surface: wip *observes* `gh`/`glab` state (no `wip push`); a `forge` verb + `status --probe-forge`; observed state → transition intent (merged → Done); Tier-0 `ship` stands down when a forge owns the transition |
| [0019](./0019-tracker-lifecycle-contract.md) | wip ⇄ tracker lifecycle contract: provider-agnostic lifecycle intent `{node,to,reason}` emitted by boundary commands; `tracker:` mapping key (roadmap-authored, writer-generated `.wip.yaml` mirror, doctor-checked); `todo/in-progress/in-review/done` vocabulary; `wip review complete` / `/wip:complete-review`; operator-selected intake |
