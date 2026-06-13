# 0006 — `wip` owns the seams, not the tools

- Status: accepted
- Date: 2026-06-12
- Source: shape-alignment discussion (2026-06-12); findings w2/w3

## Context

LDS, prtend, Duo, changelog, direnv are independently useful and (eventually) ship from
their own repos. `wip` must compose them without absorbing them.

## Decision

`wip` owns the **seams** between tools, not the tools themselves:

- It **detects** a feature via `.wip.yaml` + a sentinel (ADR-0002).
- It **invokes** the tool's own surface (e.g. `wip graduate` calls LDS's existing
  `analyze`/`review`/`extract` verbs; it does not reimplement extraction).
- It **wraps, never reimplements**, existing installers (e.g. `changelog-portable-stub`).

No tool's content lives inside the `wip` repo. The exception is `roles/` — Solo
orchestration *behavior* is genuinely `wip`'s, but it depends on Solo as an external tool.

## Consequences

- LDS can move to its own repo with zero impact on `wip`.
- `wip` core stays small; capability grows by adding detectors/adapters, not features.
- A feature absent from `.wip.yaml` is simply invisible — no hidden coupling.
