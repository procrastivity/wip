<!-- wip glossary partial: LDS (Layered Documentation System). Included ONLY when
     features.lds.enabled is true in .wip.yaml. Defines the LDS vocabulary and binds
     core's abstract Graduation concept (see core.md) to its LDS realization (Extract).
     A consumer not using LDS never sees these terms. -->

## Layered Documentation System (LDS)

The **LDS** organizes a project's permanent engineering docs into ordered, named
**Layers**, each with a defined purpose. It is the durable destination that core's
**Graduation** concept promotes knowledge *into* (the mechanism is Extract, below).

| Term | Definition |
|------|------------|
| **Layered Documentation System (LDS)** | The framework that organizes a project's engineering docs into the seven ordered Layers below. A composable wip Feature (`features.lds`), opt-in per repo. |
| **Layer** | One documentation category with a defined purpose (and, in the LDS guide, a stability and audience). LDS defines seven; see the table below. |
| **eng docs root** | The single directory the LDS tree lives under — `engineering/` by default, or `docs/` (old-style). Resolved from `features.lds.root`; scalar single root in v1 (monorepo-plural deferred). |
| **`.lds-manifest.yaml`** | The LDS **Sentinel** (core's Detection contract): its existence at `{root}/.lds-manifest.yaml` proves LDS is installed. It also pins the extraction list Extract reads (see below). |
| **ADR** | Architecture/Architectural Decision Record — an immutable Layer 1 document (context / decision / consequences); superseded by a newer ADR, never edited. |
| **Appendix** | Large content offloaded to `{layer}/appendices/{topic}/`, keeping the main Layer documents scannable. |
| **Drift** (LDS sense) | Documentation and implementation out of sync. Distinct from core's Detection-contract drift (stanza-without-sentinel / sentinel-without-stanza); LDS Drift is a docs-vs-code mismatch the LDS maintenance workflows reconcile. |

The seven Layers, in order:

| Layer | Directory | Purpose |
|-------|-----------|---------|
| 1 Decisions | `decisions/` | Capture *why* significant decisions were made (ADRs). |
| 2 Vision | `product/` | Define *what* we're building and *why*, for all stakeholders. |
| 3 Architecture | `architecture/` | Show how components relate; diagrams primary, narrative supporting. |
| 4 Specifications | `specs/` | Detail *how* individual features work; living documents. |
| 5 Reference | `reference/` | Lookup material — CLI commands, config keys, env vars, error catalogs. |
| 6 Behaviors | `behaviors/` | Executable specifications verifying the system meets expectations. |
| 7 Implementation | `implementation/` | Describe *how* to build the system — tech stack, scaffolding, phase plans. |

## Graduation (LDS mechanism)

**Extract** is the LDS realization of core's **Graduation** concept — it promotes durable
knowledge out of `.wip/` into the LDS Layer tree, one-way.

| Term | Definition |
|------|------------|
| **Extract** | The deterministic LDS Extract phase, run by `wip-plumbing extract`. Reads an approved **extraction manifest** and writes the named durable docs into their target Layers. The seam, not the tooling (ADR-0006): the deterministic write is plumbing; the analyze/review that *authors and approves* the manifest is porcelain. |
| **extraction manifest** | The `entries[]` list in `{root}/.lds-manifest.yaml` naming what Extract promotes and where. Extract refuses to run against an unapproved manifest. |

> **Graduation is the core concept; Extract is its LDS binding.** Core (`core.md`) defines
> Graduation as one-way promotion of durable knowledge out of `.wip/`, with the mechanism
> left feature-specific. For LDS that mechanism is Extract against an approved extraction
> manifest, landing knowledge in the seven Layers above.
