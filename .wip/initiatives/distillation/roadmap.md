# Roadmap — distillation

The plan of record for building `wip` by distilling the collected workflows. Promoted
from the remaining-items scratchpad (now this file is the source of truth; the scratchpad
is just a live mirror). Brief: [`BRIEF.md`](./BRIEF.md) · Decisions:
[`../../../engineering/decisions/`](../../../engineering/decisions/).

Lifecycle reminder: locked **decisions** graduate to `engineering/decisions/` (ADRs) as
soon as they lock; this **roadmap** holds the plan; each Step gets a
`workplans/step-NN-<slug>.md` when it starts.

---

## Round 1 — Shape & bootstrap  ✅ shipped 2026-06-12

Establish what the repo *is* before writing the CLI.

- **step-01 — Repo skeleton** ✅ — `engineering/` (LDS), `docs/` (Diátaxis), `roles/`, `templates/`, top-level README.
- **step-02 — Glossary layering** ✅ — `templates/glossary/{core,solo}.md` + README; `.wip/GLOSSARY.md` regenerated as a generated instance.
- **step-03 — Graduate locked decisions** ✅ — ADRs 0001–0006 in `engineering/decisions/`.
- **step-04 — Promote scratchpad → roadmap** ✅ — this file.

## Round 2 — `wip-plumbing` v1

The deterministic core. Shipping criterion for the Round: `wip-plumbing` answers
"where am I / what's next" on this very repo.

- **step-05 — CLI contract spec** ✅ shipped 2026-06-12 — [`engineering/specs/wip-plumbing-cli.md`](../../../engineering/specs/wip-plumbing-cli.md): the 6 verbs, JSON I/O, exit codes (prtend conventions). 3 open questions to resolve while building step-06–08.
- **step-06 — `detect` + `doctor`** ✅ shipped 2026-06-12 — `bin/wip-plumbing` + `lib/wip/` + tests + Makefile; `make check` green under nix (yq-go/shellcheck/shfmt). Established the bash deps + layout for Round 2. `doctor --fix` is advisory in v1. (Dogfood: `doctor` caught a real drift on our own repo — undeclared `diataxis` — now fixed in `.wip.yaml`.)
- **step-06.5 — global project registry + `--project` selector** — JSONL upsert at `$XDG_STATE_HOME/wip/projects.jsonl` on every successful `.wip.yaml` walk-up; new `project list/register/resolve/forget` subcommands; `--project <id>` (abs-path / dash-segment / opt-in slug) on existing verbs; opt-out via `WIP_NO_REGISTRY` or `.wip.yaml`'s `plumbing.register: false`. Lands [ADR-0008](../../../engineering/decisions/0008-global-project-registry.md) + [`engineering/specs/wip-plumbing-registry.md`](../../../engineering/specs/wip-plumbing-registry.md). Slotted before step-07 so all later verbs gain `--project` for free.
- **step-07 — `init` + `intake validate` v0** — scaffold an initiative from `templates/`; ship the single-kind `intake validate <file>` shape check (parseable + title + goal/summary). The generalized intake pipeline slots in at step-07.5. *(next up)*
- **step-07.5 — intake kinds + classify/validate** — promote `intake` to subcommands (`classify` / `validate` / `apply`); extend kind vocabulary (`brief` / `amendment` / `workplan-seed` / `spec` / `handoff`); land [ADR-0009](../../../engineering/decisions/0009-intake-as-pipeline.md) and the new [`intake-kinds.md`](../../../engineering/specs/intake-kinds.md) spec. `intake apply`'s routing is gated on step-08.5.
- **step-08 — `status` + `next`** — read roadmap + (if Solo) todos; rank candidates. The headline value.
- **step-08.5 — `roadmap amend` + `workplan init`** — the two new plumbing verbs that `intake apply` dispatches to. Idempotent roadmap edits (insert / replace / append-round); workplan scaffolding from `templates/workplan.md.tmpl` (created here if step-07 didn't ship it). Unlocks the full `intake apply` surface.
- **step-09 — repo baseline** — flake.nix / .envrc / Makefile / pre-commit for `wip` itself (bootstrap by hand; the dogfood test for the eventual `wip setup` family).

## Round 3 — Porcelain, plugin & features

- **step-10 — `wip` porcelain** — provider wiring (OpenAI-compatible endpoint) over `wip-plumbing`.
- **step-10.5 — `wip intake` porcelain** — the LLM-driven shaper/router (ADR-0009 phases 2 & 4) on top of the step-10 shell. Drives `wip-plumbing intake classify` → shape → `intake validate` → route → `intake apply`. May ask the user clarifying questions; the plumbing beneath it does not. End-to-end pipeline becomes usable from this step. Dogfood target: round-trip a real Claude Code plan file into a `roadmap.md` amendment.
- **step-11 — `/wip:*` plugin** — `.claude-plugin/` + skills; `/wip:next` first.
- **step-12 — Roles set** — distill `workflow-portable-stub/playbook/` into the backend-agnostic role structure per [`roles/README.md`](../../../roles/README.md): behavior files + `tier-policy.md` + `backends/solo.md` binding (the only doc naming Solo MCP tools). Roles are gated on `features.orchestration.enabled`, bound by `features.orchestration.backend` (ADR-0007). Prereqs already landed: the glossary split (`orchestration.md` / `solo.md`) and the `.wip.yaml` capability/backend split. The `wip spawn` / `wip orchestrate` verbs (w4/w5) resolve a requested Tier through the *active backend* binding, not Solo directly. (was scratchpad item 2.)
- **step-13 — `wip glossary` assembler** — concatenate `core` + enabled-feature partials (here: `core` + `orchestration` + `solo`) → generated `.wip/GLOSSARY.md`.
- **step-14 — `wip setup` family** — direnv / release / hygiene / agents / deps (was the bulk of w2).
- **step-15 — `graduate` / `extract`** — the LDS seam (wraps existing LDS verbs).

---

## Deferred (decided-not-now)

- Plural LDS installs / monorepo support (v1 = scalar single root).
- python-uv flake impure-shellHook variant (default is `make install`).
- Diátaxis sentinel: README-4-section heuristic for v1 vs `.diataxis-manifest.yaml` later.
- `/wip:*` colon namespacing vs flat `/wip-status`.

## Backlog (cross-cutting; see also `.wip/backlog.md`)

- **In-place study-slice fixes** (scratchpad item 3): fix `prtend/CLAUDE.md` (→ xcind
  pointer) and `workflow-portable-stub` broken paths *in the gitignored slices*. Needs a
  human call — these are reference copies and prtend is a useful counter-example.
