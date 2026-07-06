# Glossary partials

The **layered source** of the `wip` vocabulary. `wip` assembles a project's effective
glossary by concatenating `core.md` with one partial per feature enabled in `.wip.yaml`.

| Partial | Included when | Owns |
|---------|---------------|------|
| `core.md` | always | layers, collections, lifecycle verbs + state machine, composability/detection |
| `orchestration.md` | `features.orchestration.enabled` | Roles + abstract substrate (backend-agnostic) |
| `solo.md` | `features.orchestration.backend: solo` | Solo backend binding for orchestration |
| `task.md` | `features.orchestration.backend: task` | Task-tool / native-subagent backend binding for orchestration |
| `duo.md` | `features.orchestration.backend: duo` | Duo backend binding (a spawner layered on Solo; delegates runtime selection to Duo presets) |
| `lds.md` | `features.lds.enabled` | LDS terms + the LDS graduation mechanism *(future)* |
| `diataxis.md` | `features.diataxis.enabled` | Diátaxis terms *(future)* |

**Assembly** is `wip-plumbing glossary assemble` (regen) and `wip-plumbing glossary
check` (drift gate): `core.md` first, then each enabled feature's partial in
declaration order, with a generated header naming the source partials. A project
never hand-edits its assembled `.wip/GLOSSARY.md`. Regenerate with
`bin/wip-plumbing glossary assemble > .wip/GLOSSARY.md` (or `make glossary`).

This repo enables `orchestration` with the `solo` backend, so its generated
`.wip/GLOSSARY.md` = `core.md` + `orchestration.md` + `solo.md`.
