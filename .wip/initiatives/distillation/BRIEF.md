# Distillation — BRIEF (single source of truth)

> Every agent (`w1`–`w5`) reads this file first. It is the shared context. If a
> cross-cutting decision changes, it changes **here** and the affected prompt(s)
> get a one-line note. This file dogfoods the `brief.md` primitive we are designing.

## The meta-goal

`/Users/beausimensen/Code/wip` is a deliberate collection of *slices* of several real
projects. The owner (Beau) wants to **distill the workflows he's been using** into a
set of **composable, discoverable, individually-useful tools and conventions**, and
eventually a `wip` CLI + `/wip:*` Claude Code slash-command surface that makes
"what's the state of things / what's next" **deterministic**.

This is a RESEARCH + DESIGN task. **Do not modify any of the example projects.**
Produce design artifacts only. Each agent writes ONE findings file (path below).

## Confirmed decisions (do not relitigate)

- **`.wip/` is the home for in-flight / ephemeral planning content.** Default
  **gitignored, opt-in to commit** (a `wip.yaml` toggle). Graduating durable
  knowledge to LDS is the explicit "make it permanent" step.
- Composability is a hard requirement: a repo opts into any subset of features
  (LDS, Diátaxis, changelog, direnv baseline, prtend, Solo orchestration). Each
  feature records its presence so tooling can detect it deterministically.
- Agent runtime: Claude Opus only (Solo `agent_tool_id=3`).

## The verb taxonomy (seven activities; friction is at the SEAMS)

| # | Activity | Lives in |
|---|----------|----------|
| A | Ideate → spec | `spec-generator` skill, `intake-proposal`, `prd-*` templates |
| B | Spec → execution plan (roadmap→step→workplan→chunk) | `workflow-portable-stub`, `playbook/notes/`, bizapps `.wip/` |
| C | Baseline repo setup (flake/direnv/uv/dependabot/pre-commit/changelog/AGENTS+CLAUDE) | `changelog-portable-stub`, `direnv-session-loader`, `xcind` |
| D | Decompose durable knowledge → LDS | `layered-documentation-system/extract.md` + manifests |
| E | Capture in-flight/ephemeral planning | `.wip/` (bizapps) vs `notes/` (stub) vs `~/.claude/plans/` |
| F | Discoverability & config ("is X installed/active, where?") | `.lds-manifest.yaml` (two locations) |
| G | Runtime orchestration substrate (processes/todos/scratchpads/timers/tiers) | Solo + Duo, stub playbooks |

## Confirmed findings (starting facts — extend, don't re-derive)

1. **Positive pattern (north star):** `xcind/CLAUDE.md` is a 3-line pointer to
   `xcind/AGENTS.md`; xcind has a **dual glossary** (user-facing
   `docs/explanation/glossary.md` vs maintainer `engineering/product/glossary.md`)
   and **auto-triggering skills** (`add-installed-file`, `pre-commit-check`).
2. **Counter pattern:** `prtend/CLAUDE.md` and `prtend/AGENTS.md` are byte-identical
   except the H1. Should be the xcind pointer.
3. **Broken self-reference:** `workflow-portable-stub/playbook/{orchestrator,coordinator}.md`
   say *"Read first: `notes/playbook/shared-static.md`"* and the README references
   `notes/playbook/README.md`, but the playbook files actually live at `playbook/`
   (top level); `notes/` holds artifacts. The playbooks describe a layout that does
   not match their own installer.
4. **Three rival homes + vocabularies for the SAME lifecycle:** stub
   `notes/{proposals,roadmap,workplans,backlog}`; bizapps `.wip/<initiative>/{COMMON,phase-*}`;
   symfony `playbook/notes/{roadmap,workplans,audit,migration-kickstart}`. "Step"
   (stub) and "Phase" (bizapps) name the same thing.
5. **Planning escapes the repo:** bizapps `.wip/conversations/README.md` points its
   meta-plan to `~/.claude/plans/...md` — unversioned, machine-local, lost to a team.
   This is the core problem `.wip` exists to solve.
6. **LDS install-location ambiguity:** `hypomnema/docs/.lds-manifest.yaml` (old-style,
   docs/) vs `playbook/engineering/.lds-manifest.yaml` (new-style, engineering/).
   Manifest exists but requires `find` to locate — no deterministic entry point.
7. **LDS lifecycle verbs already exist:** `layered-documentation-system/{create,analyze,
   extract,install,migrate-to-two-track,review}.md` + `maintenance/{refine,sync,update,audit}.md`.

## Proposed primitive vocabulary (SEED — w1 owns finalizing this)

- **Actors (Solo processes):** Orchestrator (human-facing) · Coordinator (drives one
  step) · Researcher (long-lived per step) · Builder (ephemeral).
- **Collections:** Initiative → Brief (single source of truth) → Proposal
  (pre-commitment) → Roadmap (ordered Rounds of Steps) → Workplan (a step's detail,
  split into Chunks) → graduate to LDS (Specs/Decisions) or Backlog (deferred).
- **Open naming question:** collapse **Step ≡ Phase** to one word.

## Proposed `.wip/` layout (SEED — w1 refines, w3/w5 consume)

```
.wip/
  wip.yaml                # manifest: features enabled, gitignore policy, LDS location
  GLOSSARY.md             # canonical primitives
  <initiative-slug>/
    brief.md              # ≡ bizapps COMMON.md — single source of truth
    proposal.md           # pre-commitment design (optional)
    roadmap.md            # committed rounds/steps
    workplans/step-NN.md  # execution detail + chunks
    archive/
```

## Key files to read (per workstream — read targeted, not everything)

- Vocabulary/planning: `workflow-portable-stub/` (playbook/ + notes/), `bizapps-symfony-bot/.wip/`, `playbook/notes/`
- Baseline tooling: `direnv-session-loader/`, `changelog-portable-stub/`, `xcind/{flake.nix,.envrc,Makefile,.pre-commit-config.yaml}`, `prtend/{flake?,.pre-commit-config.yaml,cliff.toml}`, each project's `flake.nix`/`.envrc`
- LDS: `layered-documentation-system/` (all verb files + schemas/ + templates/), the two `.lds-manifest.yaml`, `xcind/engineering/` vs `hypomnema/docs/`
- Orchestration: `workflow-portable-stub/playbook/*.md`, Solo MCP (`help`, `help(topic=...)`), Duo tiers
- Spec/ideation: `spec-generator` skill, `prtend/docs/` (handoff→build-steps example), `bizapps .wip COMMON+phase` pattern

## Output contract (ALL agents)

- Write exactly one file: `.wip/distillation/findings/<your-id>-<topic>.md`.
- Start with a 5-bullet **TL;DR**, then **Recommendations**, then **Evidence**
  (cite `path:line`), then **Open questions / escalations for the human**, then
  **Dependencies on other workstreams** (name w1–w5).
- Be concrete and decision-grade. Prefer a recommendation over a survey.
- **Escalate** (write an `ESCALATION:` line at the top) if you hit a decision only the
  human can make, or you find a contradiction that blocks your deliverable.
- You have ~45 min of wall-clock budget; an idle timer watches you. When done, stop.
