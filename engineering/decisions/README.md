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
