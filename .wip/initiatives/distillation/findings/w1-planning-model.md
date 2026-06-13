# w1 — Planning Model, Vocabulary & `.wip/` Lifecycle

## TL;DR

- **Collapse "Phase" into "Step".** The stub's `Round → Step → Workplan → Chunk`
  hierarchy is the more developed model; bizapps `phase-a/b/c/d` are *Steps* of a
  single Round inside one *Initiative*. "Phase" stays only as a legacy alias in
  migration tooling — never in new docs.
- **One `.wip/` layout, three legacy homes mapped onto it.** Stub `notes/`,
  bizapps `.wip/<init>/`, and symfony `playbook/notes/` are all the same shape
  (proposal / roadmap / workplan / backlog / archive) at different granularities;
  the unified layout is **initiative-scoped** with a top-level `wip.yaml` + shared
  `GLOSSARY.md`, and every legacy path has a deterministic new path (see §5).
- **Two ideate→spec entry paths, one merge point.** *Structured*:
  `spec-generator` produces a spec → becomes the initiative `brief.md`.
  *Ad-hoc*: an existing handoff/`COMMON.md`-style doc is moved verbatim to
  `brief.md`. Both then enter the same Proposal → Roadmap → Workplan flow.
- **`brief.md` is the single-source-of-truth primitive** (≡ bizapps `COMMON.md`).
  Every Step prompt references the Brief; decisions change *in the Brief* with a
  one-line note in affected Step prompts. This dogfoods what the distillation
  BRIEF itself is doing.
- **`wip.yaml` is the deterministic entry point** w3 (detection) and w5 (CLI)
  build against — lists initiatives, declares feature toggles (LDS, changelog,
  direnv, Solo, prtend, Diátaxis), gitignore policy, and the LDS install path.
  No `find`-the-manifest dance.

---

## Recommendations

### R1. The canonical vocabulary (GLOSSARY-ready)

**Primitive nouns** (collection nouns are PascalCase singular; instances are
kebab-case slugs):

| Term | Definition | Replaces |
|---|---|---|
| **Initiative** | A named unit of in-flight work scoped to one outcome. Has a slug, a Brief, optionally a Proposal, exactly one active Roadmap, and zero or more Workplans. | bizapps `.wip/<dir>/`, symfony `playbook/notes/<feature>/` cluster, stub round-set for one feature |
| **Brief** | The Initiative's single source of truth: conceptual model, locked decisions, conventions/ops, phase order. Every Step prompt reads it first. | bizapps `COMMON.md`; this file |
| **Proposal** | Pre-commitment design doc. Optional. Output of `spec-generator` or hand-authored. Decomposes into a Roadmap when accepted. | stub `notes/proposals/<slug>.md`; symfony `tmp/specs/` |
| **Roadmap** | An ordered set of **Rounds**. A Round is an ordered set of **Steps** that ship together (typically a version cut). | stub `notes/roadmap/roadmap-N.md` |
| **Round** | One shipping cycle inside a Roadmap. Holds N Steps; closes with a Round Retro and (optionally) a release. | stub Round; bizapps implicit "one round" |
| **Step** | The atomic unit of planned work. Has goal, shipping criteria, deferred decisions, deps, risk. Identified `step-NN` (or legacy `phase-a`). | stub Step; bizapps Phase; symfony step-NN |
| **Workplan** | The just-in-time execution detail for one Step: ordered tasks, files, test strategy, DoD. | stub `step-NN-workplan.md`; bizapps `phase-*.md` |
| **Chunk** | A coordinator-sized batch inside a Workplan (one builder run). | symfony `step-NN-chunk-MM.md` |
| **Backlog** | Identified-but-deferred candidates. Append-mostly; strikethrough in place on adoption. | stub `notes/backlog.md` |
| **Archive** | Shipped artifacts kept for history. Per-Initiative `archive/` mirrors the live structure. | stub `notes/roadmap/archive/` |
| **Graduation** | The act of promoting durable knowledge out of `.wip/` into LDS (specs / decisions / explanation). One-way; the source `.wip/` artifact is archived or deleted. | LDS `extract.md`, ad-hoc ADR creation |

**Decision: "Step", not "Phase".** Reasons:
1. The stub's `Round → Step → Workplan → Chunk` hierarchy is already
   four-levels-deep and consistent; "Phase" only exists at one level in bizapps.
2. "Phase" overloads with SDLC phases ("phase A: design, phase B: build") which
   is semantically *across-Step*, not Step-equivalent. Reusing the word hides
   that distinction.
3. `step-NN` is numeric and orderable; `phase-a/b/c` runs out of letters and has
   no natural extension to nested chunks.
4. The symfony playbook already settled on `step-NN`; bizapps is the outlier.

**Actors** (Solo processes, unchanged from playbook):
- **Orchestrator** — human-facing; spawns Coordinators; never writes code.
- **Coordinator** — drives one Step end-to-end; spawns Builders.
- **Researcher** — long-lived per Step; consulted during build.
- **Builder** — ephemeral; scoped to a Chunk/task.

**Lifecycle verbs** (what moves an artifact between states):

| Verb | From → To | Trigger | Effect |
|---|---|---|---|
| `intake` | input docs → Proposal | human asks orchestrator | spec-generator OR proposal-intake produces `proposal.md` |
| `commit` | Proposal → Brief + Roadmap | human approves | proposal archived; brief + roadmap drafted |
| `plan` | Roadmap → Workplan | Coordinator spawn | `workplans/step-NN.md` written |
| `build` | Workplan → execution | human says "go" | Builders run Chunks |
| `ship` | Step → archive | shipping criteria met | move Workplan to `archive/`, retro appended |
| `close-round` | Round → archive | all Steps in Round shipped | round retro, optional release |
| `graduate` | `.wip/<init>/` → LDS | human approves | extract durable spec/ADR to LDS; archive source |
| `defer` | any → Backlog | scope-cut decision | line item in `backlog.md` with link |
| `archive` | any → archive | declared inert | move under `<init>/archive/` |

### R2. Lifecycle state machine

```
                idea (chat / notes / Slack)
                  │
                  │ intake  (spec-generator OR ad-hoc handoff)
                  ▼
              Proposal ◄────────── (optional)
                  │
                  │ commit
                  ▼
             ┌─ Brief (long-lived; mutated in place) ─┐
             │                                         │
             ▼                                         │
         Roadmap                                       │
        (N Rounds)                                     │
             │                                         │
             │ plan (just-in-time per Step)            │
             ▼                                         │
         Workplan ──────► execution (Coordinator/Builders)
             │                          │
             │                          ├─ defer ──► Backlog
             │                          │
             │ ship                     │
             ▼                          │
          archive ◄───── close-round ◄──┘
             │
             │ graduate (durable knowledge only)
             ▼
            LDS (specs/decisions/explanation)
```

Triggers and file moves are summarized in the R1 verb table.

### R3. The unified `.wip/` layout

```
.wip/
  wip.yaml                       # manifest (see R5)
  GLOSSARY.md                    # canonical primitives (R1)
  PLAYBOOK.md                    # pointer to playbook/ (orchestrator/coordinator/…)
  backlog.md                     # cross-initiative deferred items
  <initiative-slug>/
    brief.md                     # ≡ bizapps COMMON.md
    proposal.md                  # optional, archived after commit
    roadmap.md                   # rounds + step list (one file)
    workplans/
      step-NN-<slug>.md          # one per planned Step
      step-NN-<slug>-chunk-MM.md # optional, per Coordinator batch
    notes/                       # process retros, decisions log
    archive/
      <mirrors live tree>
  distillation/                  # this very initiative
    BRIEF.md
    prompts/
    findings/
```

Conventions:
- **One Initiative = one slug = one directory.** No cross-initiative coupling
  in file paths.
- **Round is not a directory** — it's a section inside `roadmap.md`. (Stub
  used `roadmap-N.md` per Round; we collapse since a single file scrolls fine
  and avoids "where's the active round?" hunts.)
- **Chunks are flat siblings of Workplans**, not a subdirectory, to keep the
  symfony precedent (`step-NN-workplan.md`, `step-NN-chunk-MM.md`).
- **`backlog.md` lives at `.wip/` root**, not per-initiative — backlog is
  pre-Initiative by definition.

### R4. The ideate → spec fork

Two entry paths, both end at a Brief:

```
                ┌── (A) structured ──┐
   idea/need ───┤                    ├──► proposal.md ──► commit ──► brief.md
                └── (B) ad-hoc ──────┘                                  │
                                                                       ▼
                                                            roadmap.md + workplans/
```

**Path A — Structured (`spec-generator`):** use when the work is greenfield
enough that the design space is open, when the team needs alignment before
build, or when output will graduate to LDS as a spec. The skill runs a
discovery interview, drafts `proposal.md` against the LDS spec template, and
flags conflicts with higher-authority LDS docs. **Heuristic for choosing A:**
no existing handoff doc; or the answer to "what are we building?" is "still
TBD on key decisions"; or the destination is a public-stakes feature.

**Path B — Ad-hoc handoff:** use when an existing prose doc (Slack thread,
`~/.claude/plans/*.md`, a hand-authored `COMMON.md`) already captures the
design and the next step is execution, not discovery. The handoff doc is
moved verbatim to `brief.md`; if it had per-Step structure (bizapps
`phase-*.md`), those become `workplans/step-NN-*.md`. **No proposal step.**
**Heuristic for choosing B:** decisions are locked; the doc reads like a
COMMON; you'd reject most of spec-generator's interview as already-answered.

The fork is human-driven; the orchestrator's first question on a new
Initiative is "is this an A or B intake?" with the heuristic above as
guidance. **The wrong choice is cheap to fix**: an A-intake that turns out
to already be locked just produces a thin proposal, and a B-intake that
surfaces unanswered questions can spawn a `spec-generator` mid-stream.

### R5. `wip.yaml` manifest schema

```yaml
# .wip/wip.yaml — manifest. Single source of truth for tooling.
version: 1

# Gitignore policy. If `commit: false` (default), all of .wip/ except this file
# and GLOSSARY.md is gitignored. If `commit: true`, .wip/ is tracked.
# Per-initiative override available.
gitignore:
  commit: false                  # default: ephemeral
  always_commit:                 # files tracked even when commit=false
    - wip.yaml
    - GLOSSARY.md
    - PLAYBOOK.md

# Feature toggles. Tooling (w3 detection) reads these to know what's installed.
# Absent key = feature not present. Path is relative to repo root.
features:
  lds:
    enabled: true
    path: engineering/           # OR docs/ — explicit, no find required
    manifest: engineering/.lds-manifest.yaml
  changelog:
    enabled: true
    style: keepachangelog        # or "cliff"
    config: cliff.toml           # optional
  direnv:
    enabled: true
    loader: direnv-session-loader
  diataxis:
    enabled: false
  solo:
    enabled: true
    agent_tier_default: medium   # small | medium | large
  prtend:
    enabled: false
  playbook:
    enabled: true
    path: .wip/PLAYBOOK.md       # OR external playbook/ dir

# Initiative registry. One entry per directory under .wip/.
# `status` drives orchestrator status reporting.
initiatives:
  - slug: conversations
    title: Conversation Triage
    status: in-flight            # proposed | in-flight | shipped | archived | paused
    intake: ad-hoc               # ad-hoc | spec-generator
    brief: .wip/conversations/brief.md
    roadmap: .wip/conversations/roadmap.md
    active_step: step-02
    gitignore:                   # optional override
      commit: true               # this one IS tracked even if global=false
  - slug: distillation
    title: Workflow Distillation
    status: in-flight
    intake: ad-hoc
    brief: .wip/distillation/BRIEF.md

# Optional: external integrations
integrations:
  github:
    pr_body_source: .wip/${initiative}/roadmap.md
```

**Schema constraints w3/w5 can rely on:**
- `version` is required and pinned at 1 for now.
- `features.*.enabled` is the *only* truthy detection signal — no scanning.
- Every `initiatives[].slug` MUST match a directory `.wip/<slug>/`.
- Every initiative MUST have `brief`; `roadmap` becomes required once
  `status: in-flight`.
- Unknown keys are preserved (forward-compat) but ignored by tooling.

---

## Evidence

- **Stub planning model** (Round/Step/Workplan/Chunk):
  `workflow-portable-stub/notes/project-planning-workflow-notes.md:32-86`,
  `workflow-portable-stub/notes/roadmap/README.md:1-15`,
  `workflow-portable-stub/notes/proposals/README.md:1-11`.
- **Stub backlog hygiene policy** (strikethrough-in-place):
  `workflow-portable-stub/notes/backlog.md:5-12`.
- **Stub broken self-reference**: playbooks say
  `notes/playbook/shared-static.md` but files live at `playbook/`:
  `workflow-portable-stub/playbook/orchestrator.md:5`,
  `workflow-portable-stub/playbook/coordinator.md:5`.
- **Bizapps Phase model** (= Step) and `COMMON.md` (= Brief):
  `bizapps-symfony-bot/.wip/conversations/README.md:6-16`,
  `bizapps-symfony-bot/.wip/conversations/COMMON.md:1-6` ("If a decision
  changes, change it **here** and note the change in the affected phase
  prompt(s)" — exact pattern the distillation BRIEF copies).
- **Planning escapes the repo** (the problem `.wip/` exists to fix):
  `bizapps-symfony-bot/.wip/conversations/README.md:24` —
  "The full meta-plan lives at `~/.claude/plans/...md`."
- **Symfony playbook** uses `step-NN-workplan.md` + `step-NN-chunk-MM.md`:
  `playbook/notes/workplans/symfony-secrets-vault-step-01-workplan.md`,
  `playbook/notes/roadmap/symfony-secrets-vault.md`.
- **Solo-process actor names** unchanged from playbook:
  `workflow-portable-stub/playbook/shared-static.md:50-58`.
- **spec-generator entry path & scope**:
  `bizapps-symfony-bot/.claude/skills/spec-generator/SKILL.md:1-30`.

---

## Migration mapping (old path → new path)

| Source | Old path | New path |
|---|---|---|
| stub | `notes/proposals/<slug>.md` | `.wip/<slug>/proposal.md` |
| stub | `notes/proposals/archive/<slug>.md` | `.wip/<slug>/archive/proposal.md` |
| stub | `notes/roadmap/roadmap-N.md` | section `## Round N` inside `.wip/<slug>/roadmap.md` |
| stub | `notes/roadmap/step-NN-workplan.md` | `.wip/<slug>/workplans/step-NN-<title>.md` |
| stub | `notes/roadmap/archive/roadmap-N.md` | `.wip/<slug>/archive/roadmap.md` (consolidated) |
| stub | `notes/backlog.md` | `.wip/backlog.md` (cross-initiative root) |
| stub | `notes/project-planning-workflow-notes.md` | `.wip/<slug>/notes/process.md` |
| stub | `playbook/{orchestrator,coordinator,…}.md` | `playbook/` stays (it's the *role* playbook, not initiative content); `.wip/PLAYBOOK.md` points to it; fix the broken `notes/playbook/shared-static.md` references to `playbook/shared-static.md` |
| bizapps | `.wip/<init>/COMMON.md` | `.wip/<init>/brief.md` |
| bizapps | `.wip/<init>/phase-a-*.md` | `.wip/<init>/workplans/step-01-*.md` |
| bizapps | `.wip/<init>/README.md` index | regenerated from `wip.yaml` + `roadmap.md` |
| bizapps | `~/.claude/plans/<meta>.md` | `.wip/<init>/proposal.md` if pre-commitment, else `.wip/<init>/notes/meta-plan.md` |
| symfony | `playbook/notes/roadmap/<feature>.md` | `.wip/<feature>/roadmap.md` |
| symfony | `playbook/notes/workplans/<feature>-step-NN-workplan.md` | `.wip/<feature>/workplans/step-NN.md` |
| symfony | `playbook/notes/workplans/<feature>-step-NN-chunk-MM.md` | `.wip/<feature>/workplans/step-NN-chunk-MM.md` |
| symfony | `playbook/notes/audit/`, `migration-kickstart/` | `.wip/<dedicated-initiative>/notes/` per topic |
| symfony | `tmp/specs/<feature>.md` (proposal source) | `.wip/<feature>/proposal.md` then graduate to LDS spec on ship |

**Reverse — what does NOT move:**
- `playbook/` (the role-playbook stub) stays where it is; it's the tooling, not the artifacts. Only its internal pointer paths get fixed.
- `engineering/` / `docs/` (LDS) stays. `.wip/` only *graduates into* LDS.

---

## Open questions / escalations for the human

- **Q1 — single `backlog.md` vs per-initiative backlog?** I recommend root-level
  `.wip/backlog.md` because backlog items are by definition not yet attached to
  an Initiative. Per-initiative backlogs reintroduce the cross-cutting hunt the
  manifest is supposed to eliminate. Confirm.
- **Q2 — `roadmap.md` as one growing file vs `roadmap-N.md` per Round?** I
  picked one growing file (Rounds as `##` sections) to give the orchestrator a
  single read for status. Stub precedent splits per Round. **Trade-off:** one
  file scales worse past ~10 Rounds but reads better for the common case (≤3
  Rounds per Initiative). Confirm or flip.
- **Q3 — Does the Initiative slug appear in workplan filenames?** I said no
  (`step-NN-<title>.md`, no slug prefix), since the directory already scopes
  it. Symfony precedent prefixes (`symfony-secrets-vault-step-01-…`) because
  files were flat under `playbook/notes/workplans/`. Confirm the dir-scoping
  is sufficient.
- **Q4 — `wip.yaml` location for monorepos**: do we want `.wip/wip.yaml`
  *per workspace* (current design), or a repo-root `wip.yaml` that lists
  workspaces? Out of scope for the slice projects here, but w5 will hit it
  in the CLI design.
- **NOT escalated**: the Step-vs-Phase decision. Resolved as **Step** above
  with reasoning; if the human overrides, only the verb table and migration
  mapping change.

---

## Dependencies on other workstreams

- **w2 (LDS / graduation):** consumes the `graduate` verb (R1) and the
  `features.lds.{path,manifest}` schema (R5). Needs to define what shape an
  artifact takes when it leaves `.wip/<init>/` and lands in LDS — this file
  fixes the *exit* contract but not the *landing* contract.
- **w3 (detection / discoverability):** consumes `wip.yaml` (R5) as the
  *only* detection input. Schema constraints listed in R5 are the ones w3
  can rely on. If w3 needs additional fields, escalate back here so we
  keep one schema.
- **w4 (baseline tooling / composability):** the `features.*` toggles in
  `wip.yaml` are the inventory w4 owns the implementation of (changelog,
  direnv, prtend, etc.). w4 defines what each feature *means* to install;
  this file defines how presence is *declared*.
- **w5 (CLI / `/wip:*` slash commands):** consumes the full vocabulary (R1),
  state machine (R2), layout (R3), and manifest (R5). The verb table in R1
  is the command surface (`wip intake`, `wip commit`, `wip plan`, `wip ship`,
  `wip graduate`, `wip defer`). The two intake paths in R4 are the
  `wip intake --structured | --ad-hoc` flag.
- **Upstream from all of the above:** if any workstream needs to introduce a
  new noun or verb, it lands here and propagates — do not coin parallel
  vocabulary in your own findings file.
