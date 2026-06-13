# wip — Canonical Glossary

> The single source of truth for `wip` vocabulary. Every tool, role, doc, and
> `/wip:*` command uses these terms with these meanings. If a term needs to change,
> it changes **here first**, then propagates. Coined by the distillation initiative
> (`.wip/initiatives/distillation/`); supersedes the ad-hoc vocabularies in
> `workflow-portable-stub/`, `bizapps-symfony-bot/.wip/`, and the symfony `playbook/`.

Status: **v1 draft** · Decisions locked with the human on 2026-06-12.

---

## 1. The three layers (architecture)

Modeled on `clast` (deterministic `clast` core + judgment porcelain `clast-wake`/
`clast-brief`, planned rename to `clast-plumbing`). `wip` is the same shape.

| Term | Definition |
|------|------------|
| **`wip-plumbing`** | The **deterministic core**. A bash CLI that never calls an LLM: JSON on stdout, prose on stderr, exit codes 0–4 (the prtend/clast contract). Owns detection, status, ranking, file writes, staging, atomic moves. Everything reproducible and CI-safe lives here. |
| **`wip`** (porcelain) | The **standalone judgment frontend**, installable as a package. Configured to talk to an **OpenAI-compatible endpoint** (`wip.provider` in `.wip.yaml`) for the parts that need reasoning; shells out to `wip-plumbing` for facts and writes. `wip setup direnv` works in a plain terminal, no Claude Code required. |
| **`/wip:*`** (plugin) | The **Claude Code porcelain**. Same verbs as `wip`, but Claude Code's own model is the brain; it shells out to `wip-plumbing` for the deterministic parts. Ships as a Claude Code plugin (skills + hooks), exactly like clast's `/wakeup`. |

**Rule of thumb (which layer owns a command):** if the answer is a pure function of
*files + git + Solo state*, it's `wip-plumbing`. If it requires reading prose, choosing
among options, or composing prose, it's a porcelain (`wip` or `/wip:*`).

Distribution mirrors clast: `install.sh`, Nix flake (`nix run github:procrastivity/wip`),
`@procrastivity/wip` on npm, and the Claude Code plugin.

---

## 2. Roles (the actors)

**Roles** are the behavioral operating-manuals for the agent processes that do
orchestrated work. They are **tooling, not work product**: project-independent, static,
and **shipped by the `wip` plugin** — a repo references them; it does not copy or maintain
them (this is what kills the prtend-style copy-drift). Formerly mislabeled "playbook" in
`workflow-portable-stub/playbook/` — that name is now reserved for §3's *Playbook*.

| Role | Definition | Lifetime |
|------|------------|----------|
| **Orchestrator** | The human-facing control plane. Spawns Coordinators, surfaces escalations and status, never writes implementation code, never spawns Builders directly. | One per session |
| **Coordinator** | Drives one **Step** end-to-end. Spawns and manages the Researcher and Builders; routes escalations up to the Orchestrator. | One per active Step |
| **Researcher** | Long-lived for a Step's lifetime. Produces the **Workplan**; remains available for design/spec consultation during build. Builders reach it only via the Coordinator. | Per Step |
| **Builder** | Ephemeral. Executes one **Chunk**/task, then closes. | Per Chunk |

**Invariants:** Orchestrator ≠ Coordinator; Coordinator ≠ Researcher; Builders are
ephemeral; never spawn a Coordinator without a Researcher.

---

## 3. Collections (the artifacts)

The nouns that name planning/execution content. Collection nouns are **PascalCase
singular**; instances are **kebab-case slugs**. Initiative instances live under
`.wip/initiatives/<initiative-slug>/` (default gitignored).

**`.wip/` directory layout** — the top level holds only fixed, known entries so tooling
never has to guess what an entry is:

```
.wip/
  GLOSSARY.md            # canonical vocabulary (always committed)
  backlog.md             # cross-initiative deferred items (always committed)
  roles/                 # optional: vendored Roles (else shipped by the wip plugin)
  initiatives/
    <slug>/              # one dir per Initiative — the only place arbitrary slugs live
      brief.md
      proposal.md        # optional
      roadmap.md
      workplans/step-NN-<slug>.md
      archive/
```

| Term | Definition | Replaces (legacy) |
|------|------------|-------------------|
| **Initiative** | A named unit of in-flight work scoped to one outcome. Has a slug, a Brief, optionally a Proposal, one active Roadmap, zero+ Workplans. One Initiative = one slug = one directory. | bizapps `.wip/<dir>/`; symfony `playbook/notes/<feature>/`; a stub round-set |
| **Brief** | The Initiative's **single source of truth**: conceptual model, locked decisions, conventions/ops. Every Step prompt reads it first; decisions change *in the Brief* with a one-line note in affected prompts. | bizapps `COMMON.md` |
| **Proposal** | Optional pre-commitment design doc. Output of `spec-generator` (structured intake) or hand-authored (ad-hoc intake). Decomposes into a Roadmap on commit. | stub `notes/proposals/<slug>.md` |
| **Playbook** | An Initiative's **executable plan** — its Roadmap plus Workplans, the thing you actually run from. Umbrella term, not a separate file. Per-initiative, in-flight, gitignorable. *(This is the everyday meaning of "playbook"; do not confuse with §2 Roles.)* | symfony `playbook/notes/{roadmap,workplans}` |
| **Roadmap** | An ordered set of **Rounds**. One file per Initiative: `roadmap.md` (Rounds as `##` sections). | stub `notes/roadmap/roadmap-N.md` |
| **Round** | One shipping cycle inside a Roadmap; holds N Steps; closes with a retro and optional release. | stub Round |
| **Step** | The atomic unit of planned work: goal, shipping criteria, deferred decisions, deps, risk. Identified `step-NN`. **Never "Phase"** (Phase survives only as a migration alias). | stub Step; bizapps Phase |
| **Workplan** | The just-in-time execution detail for one Step: ordered tasks, files, test strategy, definition of done. `workplans/step-NN-<slug>.md`. | stub `step-NN-workplan.md`; bizapps `phase-*.md` |
| **Chunk** | A Coordinator-sized batch inside a Workplan — one Builder run. `workplans/step-NN-<slug>-chunk-MM.md`. | symfony `step-NN-chunk-MM.md` |
| **Backlog** | Identified-but-deferred candidates **not yet attached to an Initiative**. Single root file `.wip/backlog.md`; append-mostly; strikethrough in place on adoption. Within-initiative deferrals live in that Initiative's Roadmap "Deferred" section instead. | stub `notes/backlog.md` |
| **Archive** | Shipped/inert artifacts kept for history. Per-Initiative `archive/` mirrors the live tree. | stub `notes/roadmap/archive/` |

---

## 4. Lifecycle verbs

What moves an artifact between states. These are also the porcelain command surface.

| Verb | From → To | Trigger | Layer |
|------|-----------|---------|-------|
| **intake** | input docs → Proposal | human asks Orchestrator | porcelain (judgment) |
| **commit** | Proposal → Brief + Roadmap | human approves | porcelain |
| **plan** | Roadmap → Workplan | Coordinator/Researcher | porcelain |
| **build** | Workplan → execution | human says "go" | porcelain (spawns Builders) |
| **ship** | Step → Archive | shipping criteria met | plumbing (move + stamp) |
| **close-round** | Round → Archive | all Steps shipped | plumbing |
| **graduate** | `.wip/<init>/` → LDS | human approves | porcelain drives, plumbing stages/moves |
| **defer** | any → Backlog | scope-cut | plumbing (append line) |
| **archive** | any → `archive/` | declared inert | plumbing (move) |

**Two intake paths, one merge point** (both end at a Brief):
- **Structured** (`spec-generator`): design space still open / alignment needed / output will graduate to LDS. Produces `proposal.md` → commit → `brief.md`.
- **Ad-hoc**: an existing handoff/COMMON doc already captures the design; moved verbatim to `brief.md`; per-Step structure becomes `workplans/`. No Proposal step.
- The wrong choice is cheap to fix; a thin Proposal or a mid-stream `spec-generator` recovers it.

---

## 5. Lifecycle state machine

```
        idea (chat / notes / Slack)
          │  intake (spec-generator OR ad-hoc handoff)
          ▼
      Proposal ──(optional)
          │  commit
          ▼
     ┌─ Brief (long-lived; mutated in place) ─┐
     ▼                                         │
  Roadmap (N Rounds)                           │
     │  plan (just-in-time per Step)           │
     ▼                                         │
  Workplan ─────► execution (Coordinator/Builders)
     │                       │
     │ ship                  ├─ defer ──► Backlog
     ▼                       │
  Archive ◄── close-round ◄──┘
     │  graduate (durable knowledge only)
     ▼
    LDS (specs / decisions / explanation)
```

---

## 6. Composability & discoverability terms

| Term | Definition |
|------|------------|
| **`.wip.yaml`** | Root-level, **always-committed** manifest (hidden dotfile, paired with `.wip/`). The single deterministic entry point: which features are enabled + where, gitignore policy, initiative registry, the `wip` provider config, the current initiative. No `find` walks at steady state. |
| **Feature** | A composable capability a repo opts into: `lds`, `diataxis`, `changelog`, `direnv`, `prtend`, `solo`, `playbook`(roles), `wip`. |
| **Sentinel** | The file whose existence proves a Feature is really installed (e.g. `{root}/.lds-manifest.yaml` for LDS; a 4-section README for Diátaxis). |
| **Detection contract** | A Feature is **active** iff a `.wip.yaml` stanza enables it **and** its Sentinel exists. *Installed-but-undeclared* (sentinel, no stanza) and *declared-but-broken* (stanza, no sentinel) are the two drift states `wip doctor` reports. |
| **LDS** | Layered Documentation System — the durable engineering canon (`engineering/` new-style or `docs/` old-style). Graduation target. |
| **Diátaxis** | The user-facing docs framework (Tutorials/How-to/Reference/Explanation), conventionally in `docs/`. Independent of LDS; may coexist (xcind) but never in the same root. |
| **Graduation** | Promoting durable knowledge out of `.wip/` into LDS via existing LDS verbs (`analyze → review → extract`, or `create` for single items). One-way; a thin shim, not a parallel engine. The Brief's "LDS cross-references" section records what graduated (bizapps `COMMON.md §11` is the template). |

---

## 7. Orchestration substrate terms (Solo)

| Term | Definition |
|------|------------|
| **Process** | A Solo-managed runtime instance (agent or terminal). Agent processes play Roles. |
| **Tier** | Capability level requested at spawn (`small`/`medium`/`large`), mapped to an `agent_tool_id`. A `.wip.yaml` policy can force a tier (e.g. Opus-only). |
| **Todo** | The live execution surface (ownership, blockers, comments, locks, status). Tagged `<initiative-slug>/step-NN`. **Mirror of**, not replacement for, the Roadmap. |
| **Scratchpad** | Rolling shared context for a Step (decisions-during-build, per-task outcomes). Not a status store — query Todos for status. |
| **Timer** | A pause/resume signal; on fire, its body is injected as a fresh user turn. Used for idle-wait routing and heartbeat surfacing. Bodies must be self-contained (pid, ids, next action). |

**Source of truth for "what's next" (deliberately split):** the **Roadmap** is the durable
*plan of record* (git-tracked, survives machine loss); **Todos** are the live *execution
mirror*. Sync is one-way: Roadmap → Todos at Step kickoff. Todos never silently mutate the
Roadmap — only the Coordinator's Step-boundary archive writes back. This is why planning must
not live Solo-only (the `~/.claude/plans/` failure mode).
