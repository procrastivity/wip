# Distillation — Cross-workstream synthesis

Orchestrator reconciliation of `findings/w1`–`w5`. Resolves conflicts; lists the
decisions that are the human's to make.

## Where the five agents AGREE (lock these)

1. **Vocabulary spine.** Initiative → **Brief** (single source of truth, ≡ bizapps
   `COMMON.md`) → **Proposal** (optional, pre-commitment) → **Roadmap** (ordered
   **Rounds** of **Steps**) → **Workplan** (a Step's detail, split into **Chunks**) →
   **graduate** to LDS (Specs/Decisions) or **defer** to Backlog. Actors:
   Orchestrator / Coordinator / Researcher / Builder.
2. **"Step", not "Phase".** w1 decided with reasoning; w4 and w5 deferred to it. Final.
   "Phase" survives only as a legacy alias in migration tooling.
3. **`wip.yaml` is the deterministic manifest** and the single detection entry point —
   no `find`-the-manifest dance at steady state. Feature presence = a `wip.yaml`
   stanza + a sentinel file on disk (w3's general detection contract).
4. **`.wip/` is initiative-scoped, gitignored-by-default** (per your decision).
5. **`wip` ships as a bash CLI + Claude Code plugin**, modeled on prtend: the CLI never
   calls an LLM (deterministic, JSON stdout / prose stderr / exit codes 0–4); judgment
   lives in `/wip:*` skills. Reject a Rust binary for v1.
6. **Playbooks live at `playbook/` (repo root), not under `.wip/`** — they're durable
   how-to, not in-flight artifacts. `wip.yaml` advertises them (`features.playbook`).
7. **Wrap, don't reimplement** `changelog-portable-stub`; **don't wrap**
   `direnv-session-loader` (it's a Claude-runtime plugin, orthogonal to repo bootstrap —
   `wip doctor` recommends it, `wip setup` doesn't install it).
8. **The `.wip → LDS` graduation is a thin shim over existing LDS verbs**
   (`analyze → review → extract`, or `create` for single items) — no parallel
   extraction engine. bizapps `COMMON.md §11` is the template for graduation *output*.

## Conflicts RESOLVED by the orchestrator

| # | Conflict | Resolution |
|---|----------|------------|
| C1 | **`wip.yaml` location**: w1/w5 put it at `.wip/wip.yaml`; w3 argues it must be committed/discoverable even when `.wip/` is gitignored. | **Escalate to human (D1).** Both work; tradeoffs differ. Default recommendation: repo-root `wip.yaml` (simplest, always committed). |
| C2 | **`features` schema shape** differs across w1/w2/w3/w5 (scalar `path` vs plural `installs[]` vs `{type,version}`). | **w1's `features.<name>.enabled` + per-feature extra keys is canonical.** Adopt w2's per-feature `version` + `installed_files[]` (enables `wip doctor`/uninstall). Use **scalar single-root LDS** for v1 (defer w3's plural `installs[]` to monorepo support). |
| C3 | **Backlog location**: w1 root-level `.wip/backlog.md`; w4 per-initiative. | **Root-level `.wip/backlog.md`** for unattached ideas (backlog is pre-Initiative). Within-initiative deferrals go in that initiative's `roadmap.md` "Deferred" section. |
| C4 | **Multi-initiative concurrency**: Solo `step-NN` todo tags collide across initiatives (w4); `wip next` ambiguity (w5). | **Namespace by initiative slug everywhere**: todo tag `<slug>/step-NN`, process name `<slug>-step-NN-coordinator`. `wip.yaml` carries `current_initiative`; `--initiative` overrides. |
| C5 | **Diátaxis detection ownership** fell between w3 and w4. | **README-with-4-canonical-sections heuristic** for v1 (cheap, deterministic); revisit a `.diataxis-manifest.yaml` later. |

## Decisions that are YOURS (surfaced, not decided)

- **D1 — `wip.yaml` location.** Repo-root (simple, always committed) vs `.wip/wip.yaml`
  with a `.gitignore` whitelist (`!.wip/wip.yaml`) keeping the namespace tidy but
  finicky. *Recommend repo-root.*
- **D2 — `roadmap.md`: one growing file** (Rounds as `##` sections; one read for status)
  **vs `roadmap-N.md` per Round** (stub precedent; scales past ~10 rounds). *Recommend
  one file for v1.*
- **D3 — Dependabot vs Renovate.** Make it `wip setup deps --tool dependabot|renovate`?
  *Recommend yes, default dependabot.*
- **D4 — python-uv flake strategy.** Impure shellHook (venv activate/install on entry,
  bizapps shape) vs explicit `make install` (pure devShell, xcind shape). *Recommend
  `make install` default + documented impure variant.*
- **D5 — Tool name.** Keep `wip` (anchored by the `.wip/` dir) vs `tide`/`flow`/`loom`.
  *Recommend keep `wip`.*

## Recommended v1 (smallest thing that delivers "deterministic state of things")

6 CLI verbs — `wip detect`, `wip status`, `wip next`, `wip init`, `wip intake`,
`wip doctor` — plus one slash command `/wip:next`. Then sequence: setup family →
extract/graduate (LDS) → orchestrate/spawn (Solo).

## Immediate, low-risk fixes the research surfaced (independent of the big design)

- **prtend**: replace the duplicated `CLAUDE.md` with the xcind 3-line pointer to `AGENTS.md`.
- **workflow-portable-stub**: fix the broken `notes/playbook/…` paths (→ `playbook/…`)
  in `orchestrator.md:5`, `coordinator.md:5`, `researcher.md:5`, `builder.md:5`,
  `shared-static.md:59`, `README.md:51`; create the missing `playbook/README.md`.

## Source findings
`findings/w1-planning-model.md` · `w2-bootstrap-tooling.md` · `w3-lds-discoverability.md`
· `w4-orchestration.md` · `w5-cli-grammar.md`
