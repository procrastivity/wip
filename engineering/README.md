# engineering/ — Layered Documentation (LDS)

The durable engineering canon for **building `wip` itself**: *why* the system is shaped
the way it is. This is the **graduation target** for locked decisions that start life in
`.wip/initiatives/<slug>/`.

> Not user docs. People *using* `wip` read [`../docs/`](../docs/) (Diátaxis).
> People *building* `wip` read here.

## Layers (this project, lean to start)

| Layer | Holds |
|-------|-------|
| [`decisions/`](./decisions/) | ADRs — locked, durable decisions (MADR-minimal). |
| [`specs/`](./specs/) | Feature specs — the contracts to build against (e.g. the `wip-plumbing` CLI contract). |

Additional LDS layers (`architecture/`, `product/`, `reference/`, `behaviors/`) are
added when the content earns them — this project starts with the two that have content.

## Graduation

When a decision in an initiative's Brief/findings is **locked**, its conclusion
graduates here as an ADR (one-way). The working record stays in `.wip/`; the durable
conclusion lives here. This is the `wip graduate` flow, done by hand until that verb ships.
