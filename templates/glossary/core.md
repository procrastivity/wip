<!-- wip glossary partial: CORE. Universal — every wip consumer gets this.
     Assembled into a project's .wip/GLOSSARY.md regardless of enabled features.
     Feature-specific terms live in sibling partials (orchestration.md, solo.md, lds.md, …) and are
     included only when that feature is enabled in .wip.yaml. -->

## Layers (architecture)

`wip` is shipped as three layers (the git plumbing/porcelain split, plus a second
porcelain for Claude Code):

| Term | Definition |
|------|------------|
| **`wip-plumbing`** | The deterministic core. Never calls an LLM: JSON on stdout, prose on stderr, exit codes 0–4. Owns detection, status, ranking, file writes, staging, atomic moves. |
| **`wip`** (porcelain) | Standalone judgment frontend. Configured to an OpenAI-compatible endpoint (`provider` in `.wip.yaml`); shells out to `wip-plumbing` for facts/writes. Works in a plain terminal. |
| **`/wip:*`** (plugin) | The Claude Code porcelain — same verbs, Claude Code's model is the brain. Shells out to `wip-plumbing`. |

**Layer rule:** if the answer is a pure function of *files + git state*, it's
`wip-plumbing`; if it needs prose, choice, or composition, it's a porcelain.

## Collections (the artifacts)

Collection nouns are **PascalCase singular**; instances are **kebab-case slugs**.
Initiative instances live under `.wip/initiatives/<slug>/` (default gitignored).

| Term | Definition |
|------|------------|
| **Initiative** | A named unit of in-flight work scoped to one outcome. One slug = one directory under `.wip/initiatives/`. Has a Brief, optionally a Proposal, one active Roadmap, zero+ Workplans. |
| **Brief** | The Initiative's single source of truth: conceptual model, locked decisions, conventions. Every Step reads it first; decisions change *in the Brief*. |
| **Proposal** | Optional pre-commitment design doc. Decomposes into a Roadmap on commit. |
| **Playbook** | An Initiative's executable plan — its Roadmap plus Workplans. Umbrella term, not a file. *(Not to be confused with any actor/role docs a feature may ship.)* |
| **Roadmap** | An ordered set of **Rounds**. One file per Initiative: `roadmap.md`. |
| **Round** | One shipping cycle inside a Roadmap; holds N Steps; closes with a retro and optional release. |
| **Step** | The atomic unit of planned work: goal, shipping criteria, deferred decisions, deps, risk. `step-NN`. **Never "Phase."** |
| **Workplan** | The just-in-time execution detail for one Step: tasks, files, test strategy, DoD. `workplans/step-NN-<slug>.md`. |
| **Chunk** | A batch inside a Workplan — one execution unit. `workplans/step-NN-<slug>-chunk-MM.md`. |
| **Backlog** | Identified-but-deferred candidates not yet attached to an Initiative. Root file `.wip/backlog.md`. Within-initiative deferrals go in that Initiative's Roadmap "Deferred" section. |
| **Archive** | Shipped/inert artifacts. Per-Initiative `archive/` mirrors the live tree. |

## Lifecycle verbs

| Verb | From → To | Layer |
|------|-----------|-------|
| **intake** | input docs → Proposal | porcelain |
| **commit** | Proposal → Brief + Roadmap | porcelain |
| **plan** | Roadmap → Workplan | porcelain |
| **build** | Workplan → execution | porcelain |
| **ship** | Step → Archive | plumbing |
| **close-round** | Round → Archive | plumbing |
| **graduate** | `.wip/initiatives/<slug>/` → durable docs | porcelain drives, plumbing stages/moves |
| **defer** | any → Backlog | plumbing |
| **archive** | any → `archive/` | plumbing |

**Two intake paths, one merge point** (both end at a Brief): *structured* (an
interview/spec generator → `proposal.md` → commit → `brief.md`) and *ad-hoc* (an
existing handoff doc moved verbatim to `brief.md`; its per-Step structure becomes
`workplans/`).

## Lifecycle state machine

```
  idea ──intake──► Proposal ──commit──► Brief ──► Roadmap (Rounds)
                                                     │ plan (per Step)
                                                     ▼
                                                  Workplan ──► execution
                                                     │ ship      │ defer ► Backlog
                                                     ▼
                                                  Archive ──graduate──► durable docs
```

## Composability & discoverability

| Term | Definition |
|------|------------|
| **`.wip.yaml`** | Root-level, always-committed manifest (hidden dotfile, paired with `.wip/`). The single deterministic entry point: enabled features + locations, gitignore policy, initiative registry, provider config, current initiative. No `find` walks at steady state. |
| **Feature** | A composable capability a repo opts into (e.g. `lds`, `diataxis`, `changelog`, `direnv`, `orchestration`, …). Each ships independently and advertises itself. |
| **Sentinel** | The file whose existence proves a Feature is really installed (declared per feature). |
| **Detection contract** | A Feature is **active** iff a `.wip.yaml` stanza enables it **and** its Sentinel exists. Stanza-without-sentinel and sentinel-without-stanza are the two drift states `wip doctor` reports. |
| **Graduation** | Promoting durable knowledge out of `.wip/` into a project's permanent docs. One-way. The mechanism is feature-specific (see the relevant partial); the *concept* is core. |

> **The effective glossary for a project = this core + one partial per enabled feature.**
> `wip` assembles it; it is not hand-maintained.
