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
- **step-06.5 — global project registry + `--project` selector** ✅ shipped 2026-06-13 — JSONL upsert at `$XDG_STATE_HOME/wip/projects.jsonl` on every successful `.wip.yaml` walk-up; new `project list/register/resolve/forget` subcommands; `--project <id>` (abs-path / dash-segment / opt-in slug) on existing verbs; opt-out via `WIP_NO_REGISTRY` or `.wip.yaml`'s `plumbing.register: false`. Landed [ADR-0008](../../../engineering/decisions/0008-global-project-registry.md) + [`engineering/specs/wip-plumbing-registry.md`](../../../engineering/specs/wip-plumbing-registry.md).
- **step-07 — `init` + `intake validate` v0** ✅ shipped 2026-06-13 — `bin/wip-plumbing init [<slug>]` scaffolds the repo manifest and per-initiative `BRIEF.md` + `roadmap.md` from `templates/`; `intake validate <file>` ships the v0 single-kind shape check (parseable + H1 + `## Goal` / `## Summary`). `classify` / `apply` and per-`--kind` rules deferred to step-07.5. Templates `wip.yaml.tmpl` / `brief.md.tmpl` / `roadmap.md.tmpl` and `lib/wip/wip-plumbing-scaffold-lib.bash` shipped here. Dogfood: this BRIEF now validates.
- **step-07.5 — intake kinds + classify/validate** ✅ shipped 2026-06-13 — `wip-plumbing intake` is now `classify` / `validate` / `apply`. Closed kind vocabulary (`brief` / `amendment` / `workplan-seed` / `spec` / `handoff`) with per-kind shape rules from [`intake-kinds.md`](../../../engineering/specs/intake-kinds.md). `apply --kind brief` dispatches end-to-end through `init`; `amendment` / `workplan-seed` exit 3 until step-08.5 ships the destination verbs; `spec` exits 3 (LDS seam); `handoff` exits 4 (not terminal). New `lib/wip/wip-plumbing-intake-lib.bash` carries front-matter parsing + heuristic classification + per-kind validators. Dogfood: distillation BRIEF now round-trips classify → validate.
- **step-08 — `status` + `next`** ✅ shipped 2026-06-13 — `bin/wip-plumbing status` answers "where am I" (initiative / round / active step / dirty `.wip/` / `solo_available`) and `bin/wip-plumbing next` ranks the next moves: manifest `active_step` → first unshipped in active round → sequential → upcoming rounds → roadmap backlog → repo backlog. New `lib/wip/wip-plumbing-roadmap-lib.bash` is the canonical roadmap parser (rounds, steps with shipped state and dates, backlog entries) — bash regex + jq, BSD-awk portable. Spec §4 Q1 (dirty `.wip/`) + Q2 (roadmap grammar) resolved.
- **step-08.5 — `roadmap amend` + `workplan init`** ✅ shipped 2026-06-13 — `wip-plumbing roadmap amend <slug> --from <file>` does idempotent inserts/replaces/append-round edits with SHA-256 marker comments. `wip-plumbing workplan init <slug> <step-id>` scaffolds from `templates/workplan.md.tmpl` (lands here) with `--slug` override, `--from <seed>` (validates as workplan-seed + appends under `## Seed (from intake)`), and `--force`. New `lib/wip/wip-plumbing-amend-lib.bash` carries the render + hash + in-place edit primitives. `intake apply` now dispatches `amendment` → `roadmap amend` and `workplan-seed` → `workplan init` end-to-end. Spec §4 Q4 resolved.
- **step-09 — repo baseline** ✅ shipped 2026-06-13 — `flake.nix` (devShell pinning bash/jq/yq-go/shellcheck/shfmt/git/gnumake/pre-commit on `nixos-25.05`), `flake.lock`, `.envrc` (nix-direnv shim), `.pre-commit-config.yaml` (local hooks mirroring `make check` + stock hygiene hooks), Makefile `hooks` target, `features.direnv.enabled: true`. Dogfood passed: `nix develop --command make check`, `… doctor`, and `… pre-commit run --all-files` all exit 0. Surfaced two cross-environment portability bugs in the existing test/CLI code (BSD-vs-GNU `stat` flags; `grep -c` on empty-file fallback) and fixed both — exactly what the flake exists to catch.

## Round 3 — Porcelain, plugin & features

- **step-10 — `wip` porcelain** ✅ shipped 2026-06-13 — `bin/wip` ships with `ask` (single-turn chat completion) + `provider show` (config diagnostic, never prints the api_key); every other verb is `exec`'d through to `bin/wip-plumbing` byte-identically. Provider config reads `.wip.yaml`'s `provider:` block; v1 supports `kind: openai-compatible` only. Test seam: `WIP_PROVIDER_CMD` replaces curl with a shell snippet fed the request JSON on stdin, so `make check` stays network-free. Error envelope kinds (`no-provider` / `bad-provider` / `unsupported-provider` / `provider-env-unset`) include payload fields (`error.env`, `error.provider_kind`) so step-10.5's shaper can branch on them. Adds `curl` to `flake.nix` + Makefile `deps-check`. Spec: [`engineering/specs/wip-porcelain.md`](../../../engineering/specs/wip-porcelain.md).
- **step-10.5 — `wip intake` porcelain** ✅ shipped 2026-06-13 — `wip intake <file>` drives the full intake pipeline end-to-end: classify (shellout) → shape (LLM via the step-10 provider seam) → validate (shellout) → route → apply. The shaper emits clarifying questions via a `---ASK---` fenced block the porcelain parses; the user answers on stdin/`/dev/tty`. Flags: `--kind` / `--target` / `--yes` / `--dry-run` / `--output` / `--max-rounds`. Stdout envelope counts every LLM call under `rounds` (initial + ASK turns + validate retries) and records each clarifying question under `asked[]`. New error kinds: `classify-failed`, `kind-ambiguous`, `shape-failed`, `ask-without-tty`, `bad-shape-response`, `apply-failed`. New `lib/wip/wip-intake-shaper-lib.bash` inlines the per-kind shape rules from `intake-kinds.md` §2/§3. Tests reuse step-10's `WIP_PROVIDER_CMD` seam, stateful counter-files cover retry / ASK / round 2 conversation shape. Dogfood: round-tripped `~/.claude/plans/explore-how-clast-whimsical-floyd.md` into a fresh `intake-pipeline` roadmap as `step-03 — porcelain shaper` (mocked provider per `WIP_PROVIDER_CMD` since no live key in the dev shell; transcripts under `.wip/scratch/dogfood-step-10.5-*`). Porcelain version bumped to `0.2.0-dev`.
- **step-11 — `/wip:*` plugin** ✅ shipped 2026-06-13 — `.claude-plugin/` with `plugin.json` (name=`wip`) + `commands/{next,status,intake}.md` user-invoked slash commands resolving as `/wip:next`, `/wip:status`, `/wip:intake`. Each shells out to `bin/wip-plumbing`; Claude Code is the LLM (no provider call). `/wip:intake` drives the full intake pipeline from inside a Claude Code session with **Claude as the shaper** — clarifying questions happen inline in chat (no `---ASK---` fence). Load-bearing seam: shaper prompts lifted from heredocs in `lib/wip/wip-intake-shaper-lib.bash` to `templates/prompts/intake/{preamble,brief,amendment,workplan-seed,spec,handoff}.md`; the lib reads them from disk; the plugin reads them via the new `wip-plumbing template show <id>` verb so plugin command bodies say *what they want* not *where the bytes live*. Equivalence pinned by `test/test-shaper-templates.sh` (verb-show byte-equiv against source files + lib's `wip_shaper_system_prompt` contains both halves verbatim). Adds the `template show|list` plumbing verb (doc'd in `wip-plumbing-cli.md` §3), the `WIP_TEMPLATES_DIR` env override (test seam + install seam), and a new spec [`engineering/specs/wip-plugin.md`](../../../engineering/specs/wip-plugin.md). Three new test suites (`test-shaper-templates.sh`, `test-template-verb.sh`, `test-plugin-manifest.sh`) — 55 new assertions, all 22 prior suites still green, doctor clean. `agents/` is a 3-line README stub pointing at `roles/` (files land step-12). Dogfood: `/wip:next` + `/wip:status` + `/wip:intake` executed against this repo from inside Claude Code; full transcripts (including the inbound, shaped artifact, apply ledger, roadmap diff + rollback) at `.wip/scratch/dogfood-step-11-*`.
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
