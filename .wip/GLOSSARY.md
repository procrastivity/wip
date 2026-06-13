# wip — Effective Glossary (this project)

<!-- GENERATED ARTIFACT — do not hand-edit.
     Will be assembled by `wip glossary` from layered partials:
       templates/glossary/core.md  +  templates/glossary/orchestration.md  +  templates/glossary/solo.md
     (this repo enables features.orchestration with backend: solo). Until that verb
     exists, read the partials directly — they are the canonical source. -->

> **This is this repository's effective glossary**, assembled from `core` +
> `orchestration` + the `solo` backend partial (we dogfood orchestration on Solo). It is
> **not** what a consumer inherits: a
> consumer gets `core` plus only the partials for the features *they* enable in
> `.wip.yaml`. The canonical, editable source is **`templates/glossary/`**; the
> *rationale* behind the terms lives in **`engineering/decisions/`** (ADRs).
>
> Bootstrap note: the `wip glossary` assembler does not exist yet (it's a Step in the
> distillation roadmap). Until it ships, this file points at its inputs rather than
> duplicating them:

- **Core vocabulary** → [`templates/glossary/core.md`](../templates/glossary/core.md)
- **Orchestration (Roles, backend-agnostic)** → [`templates/glossary/orchestration.md`](../templates/glossary/orchestration.md)
- **Solo backend binding** → [`templates/glossary/solo.md`](../templates/glossary/solo.md)
- **How partials combine** → [`templates/glossary/README.md`](../templates/glossary/README.md)
