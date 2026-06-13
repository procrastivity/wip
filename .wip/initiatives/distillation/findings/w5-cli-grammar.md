# w5 — The `wip` CLI + `/wip:*` command grammar

## TL;DR

- **One verb taxonomy, two surfaces.** `wip <verb> <noun>` is the deterministic CLI (no LLM, JSON stdout / human stderr, prtend-style contract). `/wip:<verb>` is the judgment surface (a Claude Code skill that *calls* the CLI for facts and uses its own reasoning for everything else). The same verb means the same thing on both sides; the slash command is always a superset of the CLI form.
- **Rule for the split (recommended):** *if the answer is computable from files + git + Solo state, it's CLI; if it requires reading prose, choosing between options, or composing new prose, it's `/wip:*`.* Mirrors prtend's "CLI is deterministic; skill is the LLM surface" (prtend/docs/overview.md:18).
- **Ship as a Claude Code plugin** (`.claude-plugin/plugin.json` + bundled `bin/wip` bash dispatcher + `skills/wip/` + `commands/wip/*.md`), installed through the procrastivity marketplace exactly like prtend. Bash dispatcher with one file per subcommand (the `prtend/bin/prtend` shape) — single binary is over-engineered for v1, plugin-only is under-powered (you want `wip status` in any terminal, not only inside Claude).
- **Composition is feature-flagged in `wip.yaml`.** Every subcommand first calls `wip detect` (echoes the prtend pattern); subcommands that need an absent feature exit 3 ("feature not enabled") with a one-line hint pointing at `wip setup <feature>`. No hidden assumptions, no silent no-ops.
- **v1 minimal surface = 6 verbs:** `wip detect`, `wip status`, `wip next`, `wip init`, `wip intake`, `wip doctor`. Everything else (`setup`, `extract`, `graduate`, `orchestrate`, `spawn`) is v1.1+. The v1 promise is *"deterministic answer to `where am I` and `what's next`"* — nothing else.

## Recommendations

### 1. The grammar

Top-level: `wip <verb> [noun] [flags]`. Verbs are the canonical activity primitives; nouns disambiguate when one verb spans multiple targets.

| Verb | CLI form (deterministic) | `/wip:*` form (judgment) | Source workstream |
|---|---|---|---|
| `detect` | Emit JSON: which features are enabled, where LDS lives, `.wip/` policy, Solo presence, current initiative slug. *The mandatory first call.* | — (CLI only) | w3 |
| `status` | JSON: current initiative, active step, open chunks, Solo processes alive, last graduation, dirty `.wip/` files. | `/wip:status` — narrate it. | w1, w4 |
| `next` | JSON: ranked candidates for "what to do next" from roadmap order, backlog priority, blocked-but-unblocking signals. **No prose.** | `/wip:next` — pick one, justify, ask for confirmation, hand to orchestrator. | w4 |
| `init` | Scaffold `.wip/wip.yaml` + `.wip/<slug>/brief.md` from a template. Refuses if `<slug>` exists. | `/wip:init` — run `spec-generator`, then call `wip init` with the produced brief. | w1 |
| `intake` | Validate inbound planning artifacts (PRD, handoff, proposal) — schema check only. | `/wip:intake` — the proposal-intake skill; calls `wip intake --validate` then composes the roadmap. | w1 |
| `setup` | `wip setup <direnv\|release\|hygiene\|agents\|dependabot\|solo>` — runs a deterministic installer per feature; idempotent; records itself in `wip.yaml`. | `/wip:setup` — picks *which* feature, then shells out. | w2 |
| `doctor` | JSON diagnosis: each feature's expected vs actual install location, manifest health, drift between `wip.yaml` and reality. Exit 4 on any drift. | `/wip:doctor` — same + "do you want me to fix?" prompt. | w3 |
| `extract` | Run LDS `extract.md` driver against `.wip/<slug>/` → emit candidate Spec/Decision files to a staging dir. Does not write to LDS. | `/wip:extract` — actually drives the extraction with judgment about what graduates. | w3 |
| `graduate` | Move staged files from `wip extract` into the LDS tree at the location `wip detect` reports. Atomic; refuses on conflict. | `/wip:graduate` — confirms intent, calls CLI, then updates manifests. | w3 |
| `orchestrate` | Print Solo playbook-routing prompt for the current state (re-emits the workflow-portable-stub `Route this request to the project's orchestrator…` boilerplate, parameterized). | `/wip:orchestrate` — *is* the orchestrator role. | w4 |
| `spawn` | `wip spawn <coordinator\|researcher\|builder> --step N` — emits the Solo `spawn_agent` call payload (does not execute). | `/wip:spawn` — actually spawns. | w4 |

**Naming discipline.** Verbs are imperatives; nouns are singular. No `wip start-next-round` — that's `wip next --start` or, more honestly, `/wip:next` (it requires judgment about whether to start). The current stub `orchestrator start-next-round` (workflow-portable-stub/playbook/README.md:7) collapses into `/wip:next`.

### 2. The CLI-vs-slash-command rule

> **A command is CLI-only when its answer is a pure function of (files + git state + Solo state). A command is `/wip:*` when it requires reading prose, choosing among options, or producing prose.**

This is prtend's contract restated for `wip`. The CLI exits 0/1/2/3/4 per prtend's table; structured output on stdout; human prose on stderr (prtend/docs/cli-contract.md:11–38). Adopt verbatim — no reinvention.

Concretely:
- `wip status`, `wip detect`, `wip doctor`, `wip next` (the *ranking*, not the *choice*) are CLI. They are the "deterministic state of things" promise.
- `wip init`, `wip intake`, `wip extract`, `wip orchestrate` always have a `/wip:*` companion because their *real* work is judgment; the CLI side is a validator / scaffolder / payload-emitter for the skill.
- `wip setup <feature>` is CLI (installation is deterministic) but `/wip:setup` exists because *picking which feature to install given the repo* is judgment.

### 3. Distribution & composability

**Distribution.** Single Claude Code plugin (`procrastivity/wip`) shipping:

```
wip/
  .claude-plugin/
    plugin.json
    skills/wip/SKILL.md       # the /wip:* dispatcher skill
  commands/wip/               # one file per /wip:<verb>
    status.md  next.md  init.md  intake.md  doctor.md  ...
  bin/wip                     # bash dispatcher (prtend/bin/prtend shape)
  lib/wip/
    wip-lib.bash
    wip-subcommands/
      detect.bash status.bash next.bash init.bash ...
  templates/
    wip.yaml.tmpl  brief.md.tmpl  roadmap.md.tmpl  workplan.md.tmpl
```

Mirror `prtend/bin/prtend` precisely (prtend/bin/prtend:1–24): one dispatcher, one file per subcommand, sourced on demand, `WIP_LIB` env override for dev installs. **Reject single-static-binary (Rust/Go) for v1** — bash is what the example projects use, what `xcind/bin/` uses, and the audience is people who already have `bash + jq + git` for prtend. Rust is a v3 optimization, not a v1 prerequisite.

**Outside Claude Code:** `bin/wip` is invokable directly from `$PATH` (Homebrew tap or `nix run`). The plugin is the *convenient* distribution; the CLI works standalone.

**Composability.** `.wip/wip.yaml` is the manifest (per BRIEF.md:22, 76–87). Schema (minimal):

```yaml
version: 1
gitignore_policy: opt-out          # or "opt-in" — default opt-in to commit nothing
features:
  lds: { enabled: true, location: engineering/ }   # detected by wip detect
  diataxis: { enabled: false }
  changelog: { enabled: true }
  direnv:  { enabled: true }
  prtend:  { enabled: true }
  solo:    { enabled: true }
current_initiative: distillation
```

Every subcommand calls `wip detect` first. If a verb requires a feature that's off, exit 3 with `wip setup <feature>` hint — the prtend exit-code-3 model (prtend/docs/cli-contract.md:23).

### 4. Minimal v1 surface

Smallest core that delivers *"deterministic answer to where am I and what's next"*:

1. `wip detect` — features + locations as JSON. Foundation.
2. `wip status` — current initiative, step, dirty files, processes. The "state of things."
3. `wip next` — ranked candidates with reasons. The "what's next."
4. `wip init` — scaffold `.wip/<slug>/brief.md` + `wip.yaml`. Required to *have* state to report.
5. `wip intake` — validate inbound artifacts so `wip status` has something concrete.
6. `wip doctor` — diagnose drift between `wip.yaml` and reality (catches the LDS-location ambiguity in BRIEF.md finding #6).

Plus *one* slash command: `/wip:next` (it's the moment-of-truth surface; everything else can wait for users to type `wip status` themselves).

**Sequence after v1:**
- v1.1: `wip setup <direnv|hygiene>` + `/wip:status` — pull more of w2's bootstrap surface in.
- v1.2: `wip extract` + `wip graduate` + `/wip:extract` — the LDS pipeline (w3).
- v1.3: `wip orchestrate` + `wip spawn` + `/wip:orchestrate` — Solo wiring (w4).
- v1.4: `wip setup <release|agents|dependabot>` — full bootstrap family.

The rationale: v1 promises one thing (deterministic state) and ships it for any repo, even one with zero features enabled. That's the user-facing value that nothing else in the example projects currently delivers (see BRIEF.md finding #4 — three rival vocabularies, no single answer).

## Evidence

- **prtend's split is the model.** `prtend/docs/overview.md:18–24` makes the CLI/skill division explicit: *"The CLI never calls an LLM. All summarization, evaluation, and composition happens in the skill."* Adopt that line as `wip`'s.
- **prtend's CLI contract is reusable.** `prtend/docs/cli-contract.md:9–38` defines stdout=JSON / stderr=prose / exit-codes 0/1/2/3/4. Reusing this verbatim avoids inventing a parallel contract.
- **Bash dispatcher works at this scale.** `prtend/bin/prtend:1–24` is 24 lines and dispatches 10 subcommands by sourcing one file each. Same pattern in `xcind/bin/xcind-config` etc.
- **Plugin packaging is light.** `prtend/.claude-plugin/plugin.json` (562 bytes) + `direnv-session-loader/.claude-plugin/plugin.json` (287 bytes) show plugin.json is a metadata sliver — no friction to ship `wip` the same way.
- **Existing playbook prose is already the `/wip:*` payload.** `workflow-portable-stub/playbook/README.md:7–13` lists 4 boilerplate prompts (`orchestrator status / intake-proposal / start-next-round / continue`). These map 1:1 to `/wip:status`, `/wip:intake`, `/wip:next`, `/wip:orchestrate` — the work of turning them into slash commands is mostly re-homing, not authoring.
- **The current verb collision exists.** `workflow-portable-stub/playbook/README.md:7` uses `orchestrator start-next-round`; bizapps uses `phase` for the same unit; LDS uses `extract`/`graduate`. `wip`'s grammar must pick one canonical verb per activity (BRIEF.md:74 explicitly flags "Step ≡ Phase").

## Open questions / escalations for the human

1. **Naming: `wip` vs longer name.** `wip` is short but collides with WIP-the-concept ("work-in-progress branch" is a common informal term). Acceptable? Alternatives considered: `tide`, `flow`, `loom`. Recommendation: keep `wip` — the `.wip/` directory anchor makes it self-explanatory in context.
2. **`/wip:*` namespacing in Claude Code.** Slash commands typically live at `/<plugin>:<command>` once the plugin is installed. Confirm the colon syntax is what you want vs `/wip-status` (flat). Recommendation: colon form (matches `/plugin install prtend@procrastivity` style in prtend/README.md:21–28).
3. **`wip next` semantics when multiple initiatives are active.** A repo can hold `.wip/foo/` and `.wip/bar/` concurrently. Should `wip next` require `--initiative` or pick from `wip.yaml`'s `current_initiative`? Recommendation: latter, with `--initiative` override.
4. **Should `wip setup` ever modify files outside `.wip/`?** `wip setup direnv` writes `.envrc`; `wip setup hygiene` writes `.pre-commit-config.yaml`. This is exactly w2's territory. Recommendation: yes, but every external write is logged into `wip.yaml`'s `features.<name>.installed_files: [...]` so `wip doctor` and an eventual `wip uninstall <feature>` can reverse it cleanly — mirrors xcind's `add-installed-file` skill (BRIEF.md finding #1).

## Dependencies on other workstreams

- **w1 (lifecycle & vocabulary).** This grammar assumes w1 finalizes `initiative`/`brief`/`proposal`/`roadmap`/`workplan`/`chunk` as the canonical nouns and picks one of `step`/`phase`. If w1 chooses `phase`, every occurrence of `step-NN.md` and `--step N` above is renamed; the verbs stand.
- **w2 (bootstrap family).** `wip setup <feature>` is a thin dispatcher over whatever w2 designs. The feature *names* used in `wip.yaml` (`direnv`, `release`, `hygiene`, `agents`, `dependabot`, `solo`) are my naming — w2 may refine. The contract: each feature installer is idempotent, records what it installed, and can be re-run.
- **w3 (feature detection & LDS pipeline).** `wip detect` *is* w3's contract surfaced as a CLI verb. If w3 specifies a `.lds-manifest.yaml` discovery algorithm and a feature-presence schema, this CLI just serializes it. `wip extract`/`graduate` assume w3 defines what graduates (Spec vs Decision vs deferred) — the CLI handles the staging dir and atomic move, nothing else.
- **w4 (orchestration & "what's next").** `wip next` ranks candidates; the *ranking algorithm* is w4's. I assume w4 emits a JSON spec like `{candidates: [{source: "roadmap"|"backlog"|"proposal", id, score, reason}]}` that `wip next` serializes. `wip orchestrate`/`spawn` are thin wrappers around w4's actor playbooks — if w4 changes Orchestrator/Coordinator/Researcher/Builder roles, the noun arguments to `wip spawn` change with them.
- **No conflict with confirmed decisions in BRIEF.md.** `wip.yaml` is the manifest, `.wip/` default-gitignored with opt-in toggle, composability per-feature, Opus-only Solo runtime — all preserved.
