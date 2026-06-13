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

- **step-05 — CLI contract spec** — `engineering/specs/wip-plumbing-cli.md`: the 6 verbs (`detect`, `status`, `next`, `init`, `intake`, `doctor`), I/O schemas, exit codes. *(next up)*
- **step-06 — `detect` + `doctor`** — read `.wip.yaml`, verify sentinels, report drift. JSON out.
- **step-07 — `init` + `intake`** — scaffold an initiative from `templates/`; validate inbound artifacts.
- **step-08 — `status` + `next`** — read roadmap + (if Solo) todos; rank candidates. The headline value.
- **step-09 — repo baseline** — flake.nix / .envrc / Makefile / pre-commit for `wip` itself (bootstrap by hand; the dogfood test for the eventual `wip setup` family).

## Round 3 — Porcelain, plugin & features

- **step-10 — `wip` porcelain** — provider wiring (OpenAI-compatible endpoint) over `wip-plumbing`.
- **step-11 — `/wip:*` plugin** — `.claude-plugin/` + skills; `/wip:next` first.
- **step-12 — Roles set** — distill + path-fix `workflow-portable-stub/playbook/` into `roles/` (was scratchpad item 2).
- **step-13 — `wip glossary` assembler** — concatenate `core` + enabled-feature partials → generated `.wip/GLOSSARY.md`.
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
