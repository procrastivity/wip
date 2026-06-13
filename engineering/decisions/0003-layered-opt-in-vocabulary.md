# 0003 — Layered, opt-in vocabulary

- Status: accepted
- Date: 2026-06-12
- Source: shape-alignment discussion (2026-06-12)

## Context

A single canonical glossary conflated universal terms with feature-specific detail
(notably Solo orchestration). Consumers should not inherit vocabulary for features they
have not enabled. Composability is a hard requirement (LDS without Diátaxis, Solo
optional, etc.).

## Decision

The vocabulary is **layered**:

- `templates/glossary/core.md` — universal; every consumer gets it.
- `templates/glossary/<feature>.md` — one partial per feature, included **only** when
  that feature is enabled in `.wip.yaml` (e.g. `solo.md`).

A project's **effective glossary** = `core` + the partials for its enabled features,
**assembled by `wip`** (eventual `wip glossary` verb) into a generated `.wip/GLOSSARY.md`
that is never hand-edited. The *rationale* behind terms lives in ADRs here, not in the
glossary.

## Consequences

- Solo (and any feature) detail is opt-in by construction.
- The glossary becomes a build artifact; the partials are the source of truth.
- Until `wip glossary` exists, a project's `.wip/GLOSSARY.md` points at its source partials.
