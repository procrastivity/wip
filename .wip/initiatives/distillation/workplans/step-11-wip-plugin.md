# Workplan — step-11 · `/wip:*` Claude Code plugin

The third layer in [ADR-0001](../../../../engineering/decisions/0001-three-layer-plumbing-porcelain.md):
**Claude Code is the brain**. The plugin gives `wip` a presence inside a
Claude Code session as `/wip:*` slash commands. Each command shells out to
`bin/wip-plumbing` for deterministic facts, then Claude renders prose /
drives shape rewrites / asks the user follow-ups directly in chat.

Round 3's headline note calls out `/wip:next` first. This step ships
`/wip:next`, `/wip:status`, and `/wip:intake <file>` — enough to round-trip
a real Claude Code plan file into a roadmap amendment **from inside Claude
Code itself**, mirroring the dogfood criterion the CLI porcelain (step-10.5)
hit through the OpenAI-compatible provider seam.

The load-bearing constraint for this step is **prompt sharing**. step-10.5
inlined the shaper preamble + per-kind rules as 13 bash heredocs inside
`lib/wip/wip-intake-shaper-lib.bash`. The plugin cannot reuse heredocs
without either re-implementing the same text in `commands/intake.md` (drift
risk) or shelling out to the CLI porcelain (defeats the point — the plugin
should use Claude Code's own LLM, not the provider seam). The fix lands here:
**lift the prompts to `templates/prompts/intake/`** and refactor the lib to
`cat` them from disk. Both frontends — `bin/wip` and `.claude-plugin/` —
then read the same files. Equivalence is pinned by a golden test.

After this step:

- `/wip:next` answers "what's next" inside Claude Code with prose + a
  recommendation, backed by `wip-plumbing next`.
- `/wip:status` answers "where am I" with the same prose treatment.
- `/wip:intake <file>` round-trips an inbound plan file into the right
  destination (init / roadmap amend / workplan init) using Claude Code as
  the shaper; clarifications happen inline in chat (no `---ASK---` fence
  needed).
- `templates/prompts/intake/*.md` is the single source of truth for the
  shaper prompts; `lib/wip/wip-intake-shaper-lib.bash` reads them at
  runtime; the plugin reads them via a new `wip-plumbing template show <id>`
  verb that resolves the templates dir without leaking layout to the plugin.

## Decisions (made here, feed later steps)

- **Plugin location and shape.** `.claude-plugin/` at the repo root,
  following the convention in `~/.claude/plugins/marketplaces/claude-plugins-
  official/plugins/example-plugin/`:

  ```
  .claude-plugin/
    plugin.json
    commands/
      next.md       → /wip:next
      status.md     → /wip:status
      intake.md     → /wip:intake
    README.md       # human-facing install / what's in the plugin
  ```

  Plugin **name is `wip`** so the standard plugin-name colon-prefix yields
  `/wip:next` etc. without subdirectory tricks. The "`/wip:*` colon
  namespacing" deferred-decisions item is resolved by this naming.

- **`agents/` is deferred to step-12.** Roles ship with the plugin
  (`roles/README.md`), but the *files* land via step-12. This step lays
  the plugin floor; the Roles surface is one more layer up. Step-11 *does*
  add a stub `.claude-plugin/agents/README.md` pointing at `roles/` so
  the future binding is signposted.

- **Verb set for v1: `/wip:next`, `/wip:status`, `/wip:intake`.** Three
  user-invoked commands. **Not shipped in v1:**
  - `/wip:detect` / `/wip:doctor` — deterministic; meant for CI / scripts.
    Inside a Claude Code session the user can just ask "what's installed?"
    in chat and the plugin's `/wip:status` answers it via plumbing already.
    Adding these as commands buys nothing.
  - `/wip:ask` — the porcelain's chat verb has no analog inside Claude
    Code; Claude Code *is* the chat.
  - `/wip:project list/register/forget` — registry verbs are operational,
    not in-session; the CLI is the right surface for them.

  Lean: **defer all of the above until a real ask surfaces.** The plugin
  charter is "what's next / what's the state / shape this file"; that's
  three verbs, the right scope for step-11.

- **Commands, not skills, for the three verbs.** Slash commands
  (`commands/<name>.md`) are user-invoked; skills (`skills/<name>/SKILL.md`)
  are model-autoinvoked. The user types `/wip:next`; this is the command
  pattern. Skills are deferred — they make sense once we have an
  always-on behavioral surface to attach (e.g., "auto-classify any plan
  file that lands"), which is post-step-12.

- **Prompt sharing seam — the load-bearing decision.** Three options
  considered:

  1. **Plain files under `templates/prompts/intake/`; lib `cat`s them;
     plugin reads them via a new plumbing verb.** Lean. Lowest moving
     parts; the templates dir already exists and is the right home for
     "stuff `wip` ships into things"; an explicit verb (`wip-plumbing
     template show <id>`) means the plugin doesn't hardcode the templates
     dir layout.

  2. **Generate the bash heredocs from markdown at build time.** Rejected.
     Build step, dual artifacts, plugin still has to read the markdown
     (or duplicate it) — worse on every axis.

  3. **Skip the lib refactor; hardcode the prompts in the plugin command
     bodies.** Rejected. Drift risk between two frontends.

  Concretely:

  ```
  templates/prompts/intake/
    preamble.md           # the shaper preamble (from _wip_shaper_preamble)
    brief.md              # per-kind rules (from _wip_shaper_rules brief)
    amendment.md          # …rules amendment
    workplan-seed.md      # …rules workplan-seed
    spec.md               # …rules spec
    handoff.md            # …rules handoff
  ```

  Each file is plain markdown — no front-matter, no template substitutions
  (the existing heredocs use no substitutions; they're literals). Byte
  contents are exactly what `_wip_shaper_preamble` / `_wip_shaper_rules
  <kind>` emit today, so the migration is mechanical.

  `lib/wip/wip-intake-shaper-lib.bash` is refactored to a single helper
  that resolves the templates dir and reads the file:

  ```bash
  _wip_shaper_read_template() {
    local name="$1"
    local dir="${WIP_TEMPLATES_DIR:-}"
    if [[ -z "$dir" ]]; then
      # Walk up from WIP_LIB to find templates/
      dir="$(CDPATH= cd -- "$WIP_LIB/../../templates" && pwd)"
    fi
    cat -- "$dir/prompts/intake/$name.md"
  }
  _wip_shaper_preamble() { _wip_shaper_read_template preamble; }
  _wip_shaper_rules()    { _wip_shaper_read_template "$1"; }
  ```

  No environment variable contract changes; `WIP_TEMPLATES_DIR` is an
  *override* (test seam + plugin-install seam), not a requirement.

- **New plumbing verb: `wip-plumbing template show <id>`.** Resolves an
  id (`intake/preamble`, `intake/brief`, ...) to a `templates/` path and
  prints its body on stdout. Errors via the standard `{ok:false, error:
  {code:4, kind:"unknown-template", ...}}` envelope. The plugin commands
  use this verb instead of `cat $WIP_ROOT/templates/...` so they don't
  hardcode the layout. List form (`template list [--json]`) returns the
  catalog (lean: ship list too, it's eight lines and makes the seam
  introspectable).

  Why a verb instead of just letting the plugin `cat`: a verb means the
  plugin command body says *what it wants* (`intake/brief`), not *where
  the file lives*. If we later embed prompts elsewhere (e.g. compiled
  into the plumbing binary), the plugin doesn't change. The same verb
  is also handy for the porcelain's `--system-file` flag the step-10.5
  workplan deferred — "use the bundled prompt" becomes `wip-plumbing
  template show intake/brief`.

- **Equivalence test.** A new test (`test/test-shaper-templates.sh`)
  asserts that for each kind `K ∈ {brief, amendment, workplan-seed,
  spec, handoff}`:

  - `wip-plumbing template show intake/$K` byte-equals
    `templates/prompts/intake/$K.md`.
  - `wip_shaper_system_prompt $K` (the lib) contains the bytes of
    `templates/prompts/intake/preamble.md` AND
    `templates/prompts/intake/$K.md`.

  This pins the seam: any future refactor that drifts the lib from the
  files (or vice versa) fails CI.

- **`/wip:intake` does NOT use the `---ASK---` fence protocol.** The fence
  exists in the CLI porcelain because the LLM-bound state machine has
  exactly one request/response per turn; questions have to round-trip
  through the porcelain. Inside Claude Code there is no porcelain
  intermediary — Claude IS the agent. When Claude needs a clarification
  it just asks the user directly in the chat. The plugin's `intake.md`
  body tells Claude:

  > If a required field is missing and you cannot infer it confidently,
  > ask the user one clarifying question in chat and wait for the answer
  > before producing the shaped artifact. Do not emit `---ASK---` fences;
  > those are only for the CLI's non-interactive shape loop.

  This is a real simplification of the contract for the plugin path
  without losing anything the user experiences.

- **Plugin command bodies follow the official `commands/*.md` schema.**
  Front-matter: `description`, `argument-hint`, `allowed-tools`. Body is
  Claude-facing instructions, not script. Claude reads the body and
  executes the steps using its own tools (Bash for `wip-plumbing`, Read
  for files).

  - `commands/next.md` — `description: List the ranked next actions for
    this wip initiative.` `allowed-tools: Bash, Read`. Body: "Run
    `wip-plumbing next` (or `wip-plumbing next --initiative <slug>` if
    the user named one in `$ARGUMENTS`); render the candidates as a
    short markdown list ordered by `rank`; recommend rank 1; mention
    backlog candidates last with a one-line caveat."

  - `commands/status.md` — `description: Show where the user is in the
    current initiative.` `allowed-tools: Bash, Read`. Body: "Run
    `wip-plumbing status`; render initiative / round / active step /
    dirty `.wip/` files / `solo_available` as a short paragraph or
    bulleted summary."

  - `commands/intake.md` — `description: Shape an inbound plan file into
    the canonical wip kind and apply it.` `argument-hint: <file> [--kind
    <k>] [--target <slug>]`. `allowed-tools: Bash, Read, Write, Edit`.
    Body: a state-machine walkthrough Claude follows:

    1. Parse `$ARGUMENTS` for `<file>`, optional `--kind`, `--target`.
    2. Run `wip-plumbing intake classify <file>` and capture the JSON.
       Bail with the prose error if exit ≠ 0.
    3. Apply `--kind` override; else accept high-confidence guess; else
       ask the user inline ("classify says <k> with <conf>; signals=…;
       confirm or override?").
    4. Run `wip-plumbing template show intake/preamble` and
       `wip-plumbing template show intake/<kind>`. Concatenate as the
       shaper context. (Claude reads them via Bash, not Read — they're
       not project files in the consumer repo.)
    5. Read the original file (Read tool); shape it into a tempfile
       (`mktemp -t wip-intake.XXXXXX.md`) per the prompt rules. If a
       required field is missing and cannot be inferred, ASK THE USER
       INLINE in chat and wait for the answer.
    6. Run `wip-plumbing intake validate --kind <k> <tempfile>`. On
       `missing[]`, fix the tempfile and re-validate (up to 2 rounds —
       same default as the CLI porcelain; Claude controls the loop).
       After 2 rounds, report the validation failure prose and stop.
    7. Run `wip-plumbing intake apply --kind <k> [--target <t>]
       <tempfile>`. Echo the write ledger and a one-line success
       summary. On exit-4 from apply, report it.
    8. Cleanup the tempfile.

    This is materially the same state machine the CLI porcelain runs
    in `lib/wip/wip-subcommands/intake.bash`; the difference is that
    the LLM step is Claude itself (no provider call) and the ASK step
    is direct chat (no fence parse).

- **`wip-plumbing` resolution from inside the plugin.** Plugin commands
  run with the user's repo as `$PWD`. The plugin expects `wip-plumbing`
  on `$PATH` (or `$WIP_PLUMBING_BIN`). If absent, the command body
  starts with a one-line check and prints a clear install hint
  ("Install `wip-plumbing` first; see the project README"). No
  bin-resolution logic inside the markdown — Claude executes Bash, so
  PATH discovery is just `command -v wip-plumbing`.

- **README.md inside `.claude-plugin/`.** One-screen file: what the
  plugin is, install command, the three slash commands with one-line
  descriptions, the prompt-sharing seam (one paragraph), and a pointer
  to `engineering/specs/wip-plugin.md`. Not user-installation docs in
  full detail; the top-level repo README owns those.

- **New spec: `engineering/specs/wip-plugin.md`.** Authored here as the
  contract for the third layer (matches the pattern of `wip-plumbing-
  cli.md` and `wip-porcelain.md`). Sections: scope, plugin layout, verb
  contracts (per-command body summary + outputs), prompt-sharing seam
  (cross-link to `intake-kinds.md` + the new `template show` verb),
  open questions.

- **No `.wip.yaml` schema changes.** The plugin reads what plumbing
  reads. The existing comment in `.wip.yaml` ("Used ONLY by the
  standalone `wip` porcelain (not by /wip:* under Claude Code, which
  uses Claude Code's own model)") already documents the divide — that
  comment is load-bearing for this step and is preserved.

- **Versioning.** Plugin gets its own version field in `plugin.json`
  (`"version": "0.1.0-dev"`). Plumbing and porcelain versions are not
  affected.

## Chunks

1. **Lift shaper prompts to `templates/prompts/intake/`.**
   - Create `templates/prompts/intake/{preamble,brief,amendment,
     workplan-seed,spec,handoff}.md`. Each is the literal body of the
     corresponding heredoc in `lib/wip/wip-intake-shaper-lib.bash` —
     extract verbatim, no rewriting.
   - Cross-link from `templates/README.md`: a new row for
     `prompts/intake/` describing it as "shaper system prompts; consumed
     by both `bin/wip intake` and `/wip:intake`".

2. **Refactor `lib/wip/wip-intake-shaper-lib.bash`.**
   - Drop `_wip_shaper_preamble` / `_wip_shaper_rules` heredocs; replace
     with `_wip_shaper_read_template <name>` that resolves the
     templates dir and `cat`s.
   - Resolution order: `$WIP_TEMPLATES_DIR` → `$WIP_LIB/../../templates`
     (i.e. repo root) → exit non-zero with a clear error.
   - `wip_shaper_system_prompt <kind>` signature unchanged. Public API
     bytes unchanged (modulo trailing newline normalization if needed).
   - Run existing step-10.5 shaper tests; they should pass unchanged.

3. **Add `wip-plumbing template show <id>` + `template list`.**
   - New file `lib/wip/wip-plumbing-subcommands/template.bash`. Resolves
     `<id>` (`intake/preamble`, `intake/brief`, ...) against
     `$WIP_TEMPLATES_DIR` (or the repo's `templates/`); prints body on
     stdout; exits 4 with `unknown-template` envelope on miss.
   - `template list [--json]` enumerates `templates/prompts/**/*.md` with
     `{id, path}` per entry.
   - Dispatch from `bin/wip-plumbing` (new case in the verb router).

4. **Equivalence test (`test/test-shaper-templates.sh`).**
   - Asserts the byte equivalence described above for each kind.
   - Asserts `wip-plumbing template list --json` lists exactly the
     intake set we expect.
   - Asserts `wip-plumbing template show intake/<bogus>` exits 4 with
     `unknown-template` envelope.

5. **`.claude-plugin/plugin.json`.**
   ```json
   { "name": "wip",
     "description": "wip — distill workflows; ranked next actions, status, and intake shaping from inside Claude Code.",
     "version": "0.1.0-dev",
     "author": { "name": "Beau Simensen", "email": "beau@beausimensen.com" } }
   ```

6. **`.claude-plugin/commands/next.md`.** Per the body sketch above.

7. **`.claude-plugin/commands/status.md`.** Per the body sketch above.

8. **`.claude-plugin/commands/intake.md`.** The state-machine body
   detailed above. Includes the inline-ASK rule and the no-`---ASK---`
   instruction, and references `wip-plumbing template show intake/<k>`
   as the way to fetch shaper rules.

9. **`.claude-plugin/README.md`.** One-screen plugin overview as
   sketched above.

10. **`.claude-plugin/agents/README.md` stub.** Three lines pointing at
    `roles/README.md` and noting that role files land in step-12. Keeps
    the directory's purpose visible without committing to file layout
    yet.

11. **Top-level spec: `engineering/specs/wip-plugin.md`.** The contract
    for the plugin layer. Cross-link from `wip-plumbing-cli.md` §1 and
    `wip-porcelain.md` §1 (a one-line "see also" each).

12. **Plugin smoke test (`test/test-plugin-manifest.sh`).** Plain bash:
    - `.claude-plugin/plugin.json` exists and is valid JSON; `name ==
      "wip"`; `version` present.
    - `.claude-plugin/commands/{next,status,intake}.md` exist, each has
      a `description:` line in its front-matter.
    - `intake.md` body references `wip-plumbing template show
      intake/preamble` (catches accidental drift of the prompt-sharing
      seam in the command body).
    - All three command bodies reference at least one `wip-plumbing`
      shell-out (catches accidental detachment of the plugin from
      plumbing).

13. **Dogfood (mandatory before mark-shipped).** Install the plugin
    into a live Claude Code session against this repo:
    - Run `/wip:next` — confirm the ranked candidates render as prose
      and the recommended pick matches `wip-plumbing next` rank 1.
    - Run `/wip:status` — confirm initiative / active step / dirty
      `.wip/` files render correctly.
    - Run `/wip:intake <file>` on a *fresh* Claude Code plan file under
      `.wip/scratch/` (NOT a real `~/.claude/plans/*` file already used
      by step-10.5's dogfood — re-using would cause an idempotent no-op
      and not exercise the full pipeline). Capture: the classify JSON,
      the shaped artifact, the resulting `roadmap.md` (or BRIEF.md)
      diff. Roll back the test amendment after capture so the real
      roadmap stays clean.
    - Capture the three captures (next / status / intake) in the
      commit body as the dogfood evidence.

14. **Mark step-11 shipped on the roadmap; bump `active_step`.**
    - `.wip/initiatives/distillation/roadmap.md` step-11 bullet gets
      `✅ shipped <YYYY-MM-DD>` and a one-line outcome.
    - `.wip.yaml`'s `initiatives[0].active_step: step-11` → `step-12`.
    - `wip-plumbing doctor` still zero drift.

## Test strategy

Same harness as steps 06–10.5. Plain bash, `test/helpers.sh`, `mktemp`
for fixture repos. Three new test files:

- **`test/test-shaper-templates.sh`** — pins the prompt-sharing seam.
  For each kind: read `templates/prompts/intake/<kind>.md` and assert
  `bin/wip-plumbing template show intake/<kind>` is byte-identical;
  assert `wip_shaper_system_prompt <kind>` (sourced from the lib)
  contains the preamble + per-kind bytes. Negative: `template show
  intake/bogus` exits 4 with `unknown-template`.

- **`test/test-template-verb.sh`** — covers the new plumbing verb in
  isolation: `template list` enumerates the catalog; `--json` returns
  valid JSONL; `template show intake/preamble` works; `show` with no
  arg is exit 2 (`usage`).

- **`test/test-plugin-manifest.sh`** — plain-bash smoke for the plugin
  layout (per chunk 12).

All existing step-10.5 shaper tests must pass unchanged after the lib
refactor — that's the primary signal the equivalence holds at runtime,
not just on paper.

`make check` budget: three new tests, ~50 added assertions. No new
dependencies (the new verb is bash + `find`).

The plugin itself cannot be tested in `make check` (no Claude Code in
the dev shell). The dogfood capture in the commit body is the
acceptance signal for the plugin contracts; the unit tests cover only
the plumbing and lib surface area.

**Coverage targets:**

- **Prompt sharing equivalence.** All five kinds pinned byte-for-byte.
- **Template verb.** Happy paths (`show`/`list`/`list --json`) + the
  error envelope (`unknown-template`).
- **Lib refactor regression.** Existing `test-wip-intake-shape.sh` and
  friends pass unchanged. (No new assertions needed — the existing
  suite exercises the system-prompt path heavily.)
- **Plugin floor.** Manifest valid; commands present; commands
  reference plumbing; intake command references the template verb.

## Definition of done

- `templates/prompts/intake/{preamble,brief,amendment,workplan-seed,
  spec,handoff}.md` committed, with bytes equivalent to the lifted
  heredocs.
- `lib/wip/wip-intake-shaper-lib.bash` refactored to read from disk; all
  pre-existing shaper tests pass unchanged.
- `wip-plumbing template show <id>` + `template list` shipped; new
  `lib/wip/wip-plumbing-subcommands/template.bash`; dispatched from
  `bin/wip-plumbing`.
- `.claude-plugin/plugin.json`, `.claude-plugin/commands/{next,status,
  intake}.md`, `.claude-plugin/README.md`, `.claude-plugin/agents/
  README.md` committed.
- `engineering/specs/wip-plugin.md` shipped; cross-linked from
  `wip-plumbing-cli.md` §1 and `wip-porcelain.md` §1.
- Three new test files (`test-shaper-templates.sh`, `test-template-verb
  .sh`, `test-plugin-manifest.sh`) pass under `nix develop --command
  make check`. All previously-passing tests still pass (no regressions).
- `nix develop --command pre-commit run --all-files` exits 0 (shellcheck
  + shfmt + the established hooks).
- Dogfood in commit body: `/wip:next`, `/wip:status`, `/wip:intake
  <file>` all run successfully in a live Claude Code session against
  this repo. Capture includes the three outputs and (for `/wip:intake`)
  the resulting write ledger / file diff, with rollback of the test
  amendment.
- `.wip/initiatives/distillation/roadmap.md` step-11 bullet marked
  `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
- `.wip.yaml`'s `initiatives[0].active_step: step-11` → `step-12`.
- `nix develop --command bin/wip-plumbing doctor` still reports zero
  drift.
- Branch + commit + merge into `main` (no-ff merge commit, matches the
  pattern step-08.5 / step-09 / step-10 / step-10.5 used).

## Open questions to resolve during execution

- **Should the template verb live at `wip-plumbing template <show|list>`
  or `wip-plumbing prompts <show|list>`?** Lean: **`template`**. The
  `templates/` dir already has the name; "prompt" is a narrower concept
  ("shaper prompts" are one subdirectory under templates). If
  non-prompt templates (e.g. `brief.md.tmpl`) later land as `template
  show brief`, the verb stays useful.

- **Should `template show` substitute `{{key}}` placeholders, or print
  raw?** Lean: **raw in v1.** The intake prompts have no placeholders.
  Substitution is `wip_scaffold_render`'s job (`scaffold-lib.bash`); if
  we ever want a rendered-template verb, that's a separate
  `template render` subcommand. Keep `show` byte-honest.

- **Plugin commands access the templates dir via the verb, or via a
  direct `cat $WIP_ROOT/templates/...`?** Lean: **via the verb**.
  Decouples the plugin command bodies from the templates filesystem
  layout and gives us one chokepoint for future relocations. Cost is
  one extra `wip-plumbing` shell-out per `/wip:intake` invocation
  (cheap; the pipeline already has several).

- **Should the plugin ship `/wip:detect` / `/wip:doctor` for
  completeness?** Lean: **no**. Inside a Claude Code session the user
  asks "is X installed?" in chat and the plugin's `/wip:status` covers
  the answer through plumbing. If a real user request for these
  surfaces post-step-11 we can add them in a follow-up; preemptively
  shipping them dilutes the plugin charter.

- **`agents/` directory in the plugin — stub now or wait for step-12?**
  Lean: **stub README only**. Three lines pointing at `roles/`. Lets
  step-12 land role behavior files without litigating the directory
  contract; risks zero by being empty otherwise.

- **What's the exact prompt-precedence rule when the plugin command
  body is open in a Claude Code session and the user has *also* given
  Claude conflicting instructions in chat?** Lean: **the command body
  wins for the duration of the `/wip:intake` flow**. Claude Code's own
  command semantics already enforce this; we don't need to over-specify
  it in the command body. If real drift surfaces in testing we tighten
  the wording then.

- **Should `/wip:intake` write the shaped tempfile somewhere durable
  inside the user's repo (e.g. `.wip/scratch/intake-<ts>.md`) or use
  `mktemp`?** Lean: **`mktemp`**, matching the CLI porcelain. The user
  can ask Claude in chat to `--output` it explicitly; default behavior
  shouldn't clutter the repo. If we add an `--output <path>` arg-hint
  later it's a one-line update to `commands/intake.md`.

- **Plugin install path for end users (marketplace, `~/.claude/plugins
  /wip/`, repo-local `.claude-plugin/`).** Lean: **defer to a
  marketplace step**. step-11 ships the in-tree `.claude-plugin/` so
  it's usable via Claude Code's "local plugin" install today; a
  separate roadmap item (post-Round-3) can package it for the official
  marketplace if/when distribution matters.

- **Does the new `wip-plumbing template` verb count as a v1 verb that
  needs to land in `wip-plumbing-cli.md`?** Lean: **yes — add a §3 entry
  in the spec while we're here**. It's a small contract (two
  subcommands; one error kind) and skipping it would create a
  documented-vs-shipped gap.

- **Should the shaper templates carry their own front-matter (e.g.
  `kind:`, `version:`) for future-proofing?** Lean: **no, plain
  markdown only**. The lib + plugin both read them as opaque prompt
  bodies; front-matter would just be noise. If a new kind needs
  structured metadata, a sibling `templates/prompts/intake/<kind>.yaml`
  is cheap to add.
