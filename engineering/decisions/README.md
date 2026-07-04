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
| [0020](./0020-vendored-orchestration-ships-flattened-agents.md) | Vendored orchestration ships flattened, self-contained agent files in **project** scope (`.claude/agents/wip/{orchestrator,coordinator,researcher,builder}.md`), not a plugin tree — fixes F1 (plugin-in-plugin collision), F2 (`source` mismatch), F3 (vendored `roles/`), F4 (phantom README agent); `source: vendored` = flattened local copy, `source: plugin` = vendor nothing; inert cross-links left in place + conservative-write guard on a foreign root `.claude-plugin/plugin.json` |
| [0021](./0021-setup-porcelain-for-backend-features.md) | Guided `setup` porcelain for the backend features: `setup solo` / `setup forge` / `setup issue-tracker` config-echo writers (verb name == feature key), tier policy via optional flags (never defaulted, ADR-0007), no setup-time liveness probe |
| [0022](./0022-forge-backend-config-primary-selector.md) | `features.forge.backend` config pin is the **primary** forge selector; remote-blind binary probe demoted to zero-config fallback, `WIP_FORGE_CLI` env stays highest (fixes BDS-60 mixed-env gh mis-selection); records demotion only, amends ADR-0018 §3 + ADR-0021 §2 |
| [0024](./0024-node-level-tracker-granularity.md) | node-level tracker granularity: boundary-local lifecycle emission at {step, round, initiative}, **lane excluded** (ADR-0010); node addressing `<slug>/{step-NN,round-N,initiative}`; round `[tracker: ID]` roadmap-authored, initiative intake-anchored via a top-level `tracker_anchor` sibling of `tracker_map`; ships addressing + initiative-START emission, defers the round/initiative `done` writers; resolves ADR-0019 §Deferred "Round/lane auto-transition" |
