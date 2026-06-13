# Glossary partials

The **layered source** of the `wip` vocabulary. `wip` assembles a project's effective
glossary by concatenating `core.md` with one partial per feature enabled in `.wip.yaml`.

| Partial | Included when | Owns |
|---------|---------------|------|
| `core.md` | always | layers, collections, lifecycle verbs + state machine, composability/detection |
| `solo.md` | `features.solo.enabled` | Roles + Solo substrate (orchestration) |
| `lds.md` | `features.lds.enabled` | LDS terms + the LDS graduation mechanism *(future)* |
| `diataxis.md` | `features.diataxis.enabled` | Diátaxis terms *(future)* |

**Assembly** (eventual `wip glossary` / `wip-plumbing` verb): `core.md` first, then each
enabled feature's partial in declaration order, with a generated header naming the
source partials. A project never hand-edits its assembled `.wip/GLOSSARY.md`.

This repo enables `solo`, so its generated `.wip/GLOSSARY.md` = `core.md` + `solo.md`.
