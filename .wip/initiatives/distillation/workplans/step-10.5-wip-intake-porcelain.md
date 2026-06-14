# Workplan — step-10.5 · `wip intake` porcelain

Closes the intake pipeline end-to-end. Step-10 shipped the LLM-aware shell
(`wip ask`, provider seam, `WIP_PROVIDER_CMD` mock) over an already-complete
plumbing surface (step-07.5 + step-08.5: `intake classify`/`validate`/`apply`,
`roadmap amend`, `workplan init`). What's still missing is the **porcelain
shaper + router** from
[ADR-0009](../../../../engineering/decisions/0009-intake-as-pipeline.md)
phases 2 + 4 — the layer that takes an arbitrary file at wip's front door,
LLM-rewrites it into the canonical shape for its `kind` (with optional
clarifying questions to the user), then drives plumbing through
`classify → validate → apply` so the artifact lands.

After this step a real Claude Code plan file
(e.g. `~/.claude/plans/explore-how-clast-whimsical-floyd.md`) round-trips
through `wip intake` into a `roadmap.md` amendment without manual editing.

## Decisions (made here, feed later steps)

- **Verb surface — `wip intake <file> [flags]`.** One headline verb, no
  subcommands. It drives the whole pipeline. Flags:
  - `--kind <k>` — skip classify; force `k ∈ {brief, amendment, workplan-seed,
    spec, handoff}`. (User-provided override of plumbing's guess, per the
    intake-kinds.md §4 "porcelain is free to override any non-`high`
    classification" rule.)
  - `--target <slug|slug/step>` — skip the routing question; force the apply
    target. Lets the user pre-resolve "which initiative / which step" when
    they already know.
  - `--yes` / `-y` — non-interactive: skip every clarifying question. If a
    required answer can't be defaulted, exit 4 with a structured envelope
    naming the missing piece. The dogfood path needs `--yes` to be CI-safe.
  - `--dry-run` — run the pipeline through validate; render the shaped
    artifact + the routing decision; do **not** call `intake apply`. (Mirrors
    plumbing's `--dry-run`; useful for previewing the LLM's rewrite.)
  - `--output <path>` — write the shaped artifact to `<path>` *in addition*
    to apply (or *instead of*, with `--dry-run`). Default: shaped artifact
    lives only in a tempfile and is fed to `intake apply` then discarded.
  - `--max-rounds <n>` — cap shape→validate retries (default **2**). If
    validate still fails after N rounds, exit 4 with the last validation
    envelope echoed.
  - `--system-file <path>` — override the bundled shaper system prompt
    (escape hatch; lean: ship a default; users can override). Lean to defer
    if there's no concrete need by execution time.
  - `--system <text>` — same, inline. (Both forms documented but not both
    required; ship `--system-file` if any.)
- **Pipeline implemented as a state machine inside one bash function.**
  The function in `lib/wip/wip-subcommands/intake.bash` walks the phases in
  order. Each phase is a small helper:
  1. `_wip_intake_classify` — shells out `wip-plumbing intake classify
     <file>` and captures its JSON. Failure (exit 4 unparseable) bubbles up
     as the porcelain envelope kind `classify-failed`.
  2. `_wip_intake_pick_kind` — applies `--kind` override if given, else
     respects classify's `kind` when confidence is `high`, else **asks the
     user** ("classify says X with low confidence; signals=[…]; accept,
     override, or cancel?"). `--yes` accepts the guess; `--yes` + low
     confidence is exit 4 unless `--kind` is also supplied.
  3. `_wip_intake_shape` — the LLM call. Builds a request, sends via
     `wip_provider_chat`, parses the response to extract the shaped artifact
     body. The shaper is told the target kind, the per-kind shape rules
     (inlined from `intake-kinds.md` §2/§3), the original file, and any
     existing front-matter. The response contract is a single markdown
     document (the shaped file); the porcelain writes it to a tempfile.
     - **Asking via the LLM, not via the porcelain directly.** When the LLM
       needs a clarifying answer (e.g. "your brief has no goal — what is
       it?"), it emits a structured `ASK:` block in its response (`---ASK---`
       fenced section the porcelain parses). The porcelain prompts the user
       on stderr / reads stdin / re-issues the shape call with the answer
       appended to the conversation. This keeps shaping in one prompt domain
       — the porcelain doesn't try to be its own shaper, and doesn't have a
       second judgment-y layer above the LLM.
     - **`--yes` and clarifying questions.** When `--yes` is set, the shape
       prompt explicitly tells the LLM to skip questions and "do your best
       with what's there; mark anything you guessed in a TODO list at the
       end of the file." If the LLM still emits `ASK:`, the porcelain treats
       it as a shape failure and exits 4 with the LLM's `ASK:` payload in
       the envelope.
  4. `_wip_intake_validate` — shells out `wip-plumbing intake validate
     --kind <k> <shaped-tempfile>`. On failure, **loops back to shape** with
     the validation `missing[]` array appended to the conversation, up to
     `--max-rounds`. After exhaustion → exit 4 with the validation envelope.
  5. `_wip_intake_route` — pick `--target` for `apply`:
     - `brief` — derived slug from front-matter `slug:` or H1. Confirm
       with user when interactive; `--yes` accepts the derivation.
     - `amendment` — `target:` from front-matter is mandatory after shape
       (validate already enforces this). The directive
       (`insert-after`/`replace`/`append-round`) is also in front-matter.
       No further question needed; the LLM's shape output owns the answer.
     - `workplan-seed` — `target: <slug>/<step-id>` from front-matter,
       same shape rule.
     - `spec`/`handoff` — pass through; apply will exit 3/4 respectively.
  6. `_wip_intake_apply` — shells out `wip-plumbing intake apply --kind
     <k> <shaped-tempfile>` (forwarding `--dry-run` when set). Captures
     the write ledger; merges it into the porcelain's final envelope.
- **Per-kind shape rules are sourced from `intake-kinds.md` §2/§3, inlined
  into the shaper system prompt as plain text.** No new file; the spec is
  the source of truth. The shape prompt is *built at runtime* from a small
  per-kind template in `lib/wip/wip-intake-shaper-lib.bash` — one template
  per kind, plus a shared preamble.
  - Lean: **bundle the templates in the lib file, not in `templates/`.**
    These are LLM system prompts, not user-editable scaffolding; mixing them
    into `templates/` (which is brief.md / roadmap.md / etc. scaffolds)
    blurs two unrelated concepts. If users start asking to override them,
    the `--system-file` flag is the seam.
- **Conversation shape sent to the provider.** Single round per shape
  attempt; the porcelain re-builds the full messages array each round
  (system + user + prior assistant + user-follow-up). v1 conversation:
  ```
  [
    {"role":"system","content":"<shaper preamble + per-kind rules>"},
    {"role":"user","content":"# Original artifact\n<file body>\n\n# Classify\n<classify JSON>\n\n# Task\nShape this into a <kind> per the rules above."}
  ]
  ```
  On a validate-retry the porcelain appends:
  ```
  ,{"role":"assistant","content":"<prior shaped body>"}
  ,{"role":"user","content":"validate failed; missing=[\"goal-or-summary-section\"]. Fix and re-emit the full shaped artifact."}
  ```
  On a user clarifying answer the porcelain appends:
  ```
  ,{"role":"assistant","content":"<ASK block>"}
  ,{"role":"user","content":"<user's answer>"}
  ```
- **`ASK:` protocol the shaper emits.** The system prompt instructs the LLM
  to format any clarifying question as:
  ```
  ---ASK---
  question: <one sentence question>
  why: <one sentence justifying the question — what's missing>
  ---END---
  ```
  Optionally on a single line wrapped in those fences. Anything *outside*
  the fence on an `ASK:` reply is ignored (the porcelain does not treat a
  partial-shape + question as resumable; it forces a clean re-shape after
  the answer arrives). When the LLM produces a complete shape (no `ASK:`),
  the response body **is** the shaped markdown.
  - This is the v1 simplest contract. A richer "shape + ASK fields in
    parallel" protocol can come later; for now the boundary is binary.
- **Response parsing.** `_wip_intake_extract_response` walks the LLM
  response and routes to one of:
  - "ask" — `---ASK---` fence present → return `{mode:"ask", question, why}`
  - "shape" — no fence → return `{mode:"shape", body:<full text>}`
  - "invalid" — body is empty / unparseable markdown → treat as transport
    failure (exit 1 with kind `bad-shape-response`).
  The porcelain strips an outer ```markdown … ``` fence if present, because
  some providers wrap responses that way.
- **Layout.**
  - `bin/wip` — the `intake` case in its dispatcher peels off into the
    new porcelain subcommand (not the proxy fallthrough). All other proxied
    verbs stay as-is.
  - `lib/wip/wip-subcommands/intake.bash` — defines `wip_cmd_intake`,
    parses flags, runs the state machine. Mirrors `ask.bash` / `provider.bash`
    in shape.
  - `lib/wip/wip-intake-shaper-lib.bash` — `wip_shaper_system_prompt
    <kind>`, `wip_shaper_extract_response <raw>`, `wip_shaper_ask_user
    <question> <why>`. Pure bash + jq. Stays distinct from
    `wip-provider-lib.bash` so the provider helper remains kind-agnostic.
  - `lib/wip/wip-porcelain-lib.bash` — gains `wip_p_prompt <msg>` (read
    one line from `/dev/tty` or stdin) and `wip_p_confirm <msg>` (yes/no).
    Trivial wrappers; isolated so they're easy to mock.
- **Interaction transport.** Clarifying questions: write to **stderr**
  (prefixed `wip:`); read the answer from `/dev/tty` if it's open, else
  stdin. This keeps stdout reserved for the final envelope. `--yes`
  short-circuits both — no prompts written, no reads attempted.
- **Final envelope on stdout.** On success:
  ```json
  { "ok": true, "kind": "amendment", "target": "distillation",
    "rounds": 1, "asked": ["which step slot?"],
    "result": { /* the apply ledger from plumbing */ } }
  ```
  On failure: the porcelain error envelope (`{ok:false, error:{code,kind,
  message,...}}`) with new kinds documented below.
- **New error kinds.**
  - `classify-failed` (exit 4) — plumbing classify exited non-zero. Envelope
    echoes its message.
  - `kind-ambiguous` (exit 4) — confidence < high and `--yes` set without
    `--kind`. Envelope includes the classify guess + signals.
  - `shape-failed` (exit 4) — `--max-rounds` exhausted. Envelope includes
    the last validate `missing[]` and the last shaped body in `error.last_body`
    (truncated to 4 KiB to keep envelope size sane).
  - `ask-without-tty` (exit 4) — LLM emitted `ASK:` while `--yes` was set,
    or stdin is closed and no tty is available.
  - `bad-shape-response` (exit 1) — LLM response not parseable as shape or
    ASK. Caller can re-run with `-v` to see the raw response.
  - Existing provider errors (`no-provider`/`bad-provider`/`provider-env-unset`/
    `unsupported-provider`) flow through `wip_provider_load` unchanged.
- **Dry-run semantics.** `--dry-run` runs classify + shape + validate +
  route, prints the shape ledger (shaped artifact path, kind, derived
  target), but does **not** call `intake apply`. The plumbing `--dry-run`
  would *also* skip writes, but skipping the apply call entirely keeps the
  porcelain's dry-run audit one step shorter for the user.
  - If both `--dry-run` and `--output <path>` are given, the shaped
    artifact is written to `<path>` so the user can inspect it. Otherwise
    a tempfile path is printed.
- **No new `.wip.yaml` features flipped.** This step extends the
  porcelain only. The existing `provider:` block is consumed; no new
  fields invented (just like step-10 promised).
- **Versioning bump.** `WIP_PORCELAIN_VERSION` ticks from `0.1.0-dev` to
  `0.2.0-dev` (the porcelain meaningfully gained a verb). Plumbing version
  is unchanged.
- **No prose renderers for proxied verbs in this step.** Still deferred
  to a later step; step-10.5's identity is "the intake shaper," not "the
  prose-rendering shell." `wip intake` itself prints a short prose summary
  on stderr at the end ("amended distillation/roadmap.md: insert-after
  step-06; idempotent_noop=false"), but stdout stays JSON.

## Chunks

1. **`lib/wip/wip-intake-shaper-lib.bash`** (the LLM glue, no I/O of its own).
   - `wip_shaper_system_prompt <kind>` — emit the shaper system prompt for
     `<kind>`, assembled from a shared preamble + per-kind shape rules from
     `intake-kinds.md` §2/§3. Heredocs in the file; no `templates/`
     entanglement.
   - `wip_shaper_extract_response <raw>` — return JSON
     `{mode, question, why, body}` per the parsing rules above. Strips any
     outer ```markdown fence.
   - `wip_shaper_build_initial_request <kind> <classify-json> <file>` —
     emit the messages array for the first shape attempt.
   - `wip_shaper_build_retry_request <messages-so-far> <prior-body> <retry-payload>` —
     append assistant + user pair onto an existing messages array.
   - `wip_shaper_build_followup_request <messages-so-far> <prior-ask> <user-answer>` —
     same, for clarifying replies.
2. **`lib/wip/wip-porcelain-lib.bash`** — add `wip_p_prompt` (read one line
   from `/dev/tty` if open, else stdin) and `wip_p_confirm` (yes/no with
   default-no). Bump `WIP_PORCELAIN_VERSION` to `0.2.0-dev`. Extend usage
   block to list `intake`.
3. **`lib/wip/wip-subcommands/intake.bash`** — defines `wip_cmd_intake`.
   Flag parser; locate root via the same walk-up as `ask.bash`; call
   `wip_provider_load`; then the state machine:
   - classify (shell out)
   - pick-kind (interactive unless `--yes` / `--kind`)
   - shape loop (with `--max-rounds`)
     - inside the loop: build messages, `wip_provider_chat`, extract,
       handle ASK→user→re-issue or validate gate
   - validate (shell out)
   - route (derive `--target` from shaped front-matter / ask user for
     `brief` confirmation)
   - apply (shell out, optionally with `--dry-run`)
   - emit final envelope on stdout + prose summary on stderr.
4. **`bin/wip` dispatcher.** Add `intake)` case before the proxy fallthrough:
   ```bash
   intake)
     source "$WIP_LIB/wip-subcommands/intake.bash"
     wip_cmd_intake "$@" ;;
   ```
5. **Tests** (plain-bash, matches established harness).
   - `test/test-wip-intake-classify-pick.sh`
     - Fixture file with high-confidence classify → silently uses the
       guess. `--yes` path: same.
     - Low-confidence classify + `--yes` (no `--kind`) → exit 4
       `kind-ambiguous`. Envelope carries `signals`.
     - `--kind brief` override → classify result ignored. (Tested by
       stubbing the shape to short-circuit a fixture; see below.)
   - `test/test-wip-intake-shape.sh` — the heart of the suite. Uses
     `WIP_PROVIDER_CMD` to mock the LLM with a sequence of canned responses.
     - **brief happy path.** Input: a half-formed brief missing `## Goal`.
       Mock LLM returns a complete brief with `## Goal` filled in. Validate
       passes first try. Apply dispatches to `init`. Assert:
       - shaped artifact passes validate
       - apply ledger has `dispatched:"init"`
       - new `.wip/initiatives/<slug>/BRIEF.md` exists
     - **shape-retry path.** First mock response is missing a required
       section; second response is correct. Assert `rounds: 2` in the final
       envelope.
     - **shape-exhausted path.** Both mock responses are broken; assert
       exit 4 `shape-failed` with `last_body` and `missing[]` in the
       envelope.
     - **ASK path (interactive).** Mock LLM returns `---ASK---` block first;
       porcelain reads answer from stdin (we feed it via `printf … | wip
       intake …`); second mock response is a complete shape. Assert
       `asked` array contains the question. (Use a fixture script that
       returns different stdin-driven responses on each call —
       e.g. `WIP_PROVIDER_CMD` writes its invocation count to a counter
       file and `cat`s the matching response.)
     - **ASK + `--yes`.** First mock returns ASK; assert exit 4
       `ask-without-tty`.
   - `test/test-wip-intake-amend.sh` — the dogfood-shaped test.
     Fixture: a small repo with a roadmap that has `step-01` and `step-02`.
     Input artifact: a fragment that looks like a Claude Code plan with
     loose headings but no front-matter (so classify returns medium / low).
     Mock LLM shapes it into an amendment with `target: foo`,
     `insert-after: step-02`, and a `### step-03 — Three` heading. Assert
     `roadmap amend` ran and the roadmap now contains `step-03 — Three`.
   - `test/test-wip-intake-dry-run.sh` — `--dry-run` end-to-end on a brief:
     - assert classify + shape + validate ran (counter shows two LLM calls
       if a retry was needed, etc.)
     - assert `BRIEF.md` was **not** written
     - assert stdout envelope has `"dry_run": true` and a `shaped_path`
     - with `--output <path>`, assert the shaped file lands at `<path>`
   - `test/test-wip-intake-flags.sh` — flag parser + simple branches:
     - `--kind <bogus>` → exit 2 usage
     - `--max-rounds 0` with a broken first shape → exit 4 `shape-failed`
       (envelope carries `rounds: 0` would be misleading; lean: clamp to
       min 1 and document)
     - `--target slug` propagates into the apply call
6. **Dogfood + docs.**
   - Run `nix develop --command bin/wip intake
     ~/.claude/plans/explore-how-clast-whimsical-floyd.md --target distillation
     --kind amendment --max-rounds 3` against this repo, with a real
     provider — capture the LLM exchange, the shaped artifact, and the
     resulting `roadmap.md` diff in the commit body. Roll back the
     amendment from this dogfood after capture (we don't want a duplicate
     amendment marker in the real roadmap).
     - Alternative dogfood, if the plan file is genuinely already-shipped
       content: pick a *different* `~/.claude/plans/*.md` whose content
       matches an *unshipped* roadmap entry, or stage a fresh handoff-style
       file in `.wip/scratch/` and round-trip it.
   - Extend `engineering/specs/wip-porcelain.md` §3 with a `wip intake`
     subsection covering: the flag set, the ASK protocol, the envelope
     shape, the new error kinds, and the conversation contract sent to the
     provider. Cross-link `engineering/specs/intake-kinds.md` as the shape
     source of truth.
   - Extend `README.md`'s Porcelain section with a 5-line dogfood path:
     ```
     wip intake path/to/plan.md --target <slug> --kind amendment
     ```
   - No new ADR. step-10.5 implements ADR-0009 phases 2 + 4 without
     locking new architectural decisions.
7. **Mark step-10.5 shipped on the roadmap; bump `active_step`.** Update
   `.wip/initiatives/distillation/roadmap.md`'s step-10.5 bullet with
   `✅ shipped <YYYY-MM-DD>` and a one-line outcome (verb, ASK protocol,
   dogfood capture). Bump `.wip.yaml`'s `active_step: step-10.5` →
   `step-11`. Commit.

## Test strategy

Same harness as steps 06–10. Plain bash, `test/helpers.sh`, `mktemp` for
fixture repos. **Every** LLM call goes through `WIP_PROVIDER_CMD` — no
network in `make check`, ever. The mocked-LLM pattern is the same as
`test-wip-ask.sh`, extended in two ways:

- **Stateful mocks** for the retry / ASK tests use a counter file. The
  `WIP_PROVIDER_CMD` snippet increments the counter, then `cat`s the
  matching response fixture. This is a 5-line shell idiom; we don't reach
  for a fixture framework.
- **Request capture** for the conversation-shape tests uses the same
  `tee >/dev/null; cat …` pattern step-10's `test-wip-ask.sh` already
  uses. Assertions: messages length grows correctly across rounds; the
  shaper system prompt mentions the right kind; user-answer text is
  appended verbatim.

**Coverage targets:**

- **Pipeline state machine.** Every transition reachable from the spec
  (classify→shape, shape→ask→shape, shape→validate→shape, validate→apply,
  any-phase→error-envelope) has at least one test that exercises it.
- **`--yes` semantics.** Each pinned with an explicit assertion (low
  confidence → exit 4 unless `--kind` is set; ASK from LLM → exit 4
  `ask-without-tty`; high confidence happy path is silent).
- **Conversation shape.** At least one test asserts that the messages
  array on round 2 has length 4 (system, user, assistant, user) and the
  retry message names the validate `missing[]`.
- **Error envelope kinds.** Each new kind
  (`classify-failed`, `kind-ambiguous`, `shape-failed`, `ask-without-tty`,
  `bad-shape-response`) has a dedicated assertion against `error.kind`.
- **Idempotency dogfood (manual).** Re-running the same `wip intake`
  invocation on the same shaped output produces `idempotent_noop:true`
  in the apply ledger (this is plumbing's behavior, but pin it through
  the porcelain too).
- **Secret hygiene.** Re-uses step-10's `api_key not in stderr under -v`
  assertion against `wip intake -v`. New: the shaped artifact body
  shouldn't leak the api_key either (one assertion against a fixture
  where the key value appears in no field).

## Definition of done

- `bin/wip` dispatches `intake` to the new porcelain subcommand (no proxy
  fallthrough for it).
- `lib/wip/wip-subcommands/intake.bash` + `lib/wip/wip-intake-shaper-lib.bash`
  committed and executable; `wip_p_prompt`/`wip_p_confirm` added to
  `wip-porcelain-lib.bash`; `WIP_PORCELAIN_VERSION` → `0.2.0-dev`.
- Five new test files (`test-wip-intake-classify-pick.sh`,
  `-shape.sh`, `-amend.sh`, `-dry-run.sh`, `-flags.sh`) pass under
  `nix develop --command make check`. The 16 existing tests still pass.
- `nix develop --command pre-commit run --all-files` exits 0 (shellcheck +
  shfmt + the established hooks).
- `wip intake <file>` end-to-end: classify (shell-out) → shape (mocked
  LLM) → validate (shell-out) → apply (shell-out). All five new error
  kinds are reachable and pinned by tests.
- `--yes`, `--kind`, `--target`, `--dry-run`, `--output`, `--max-rounds`,
  `--system-file` (or its deferral note) all parsed and exercised by at
  least one test.
- Spec `engineering/specs/wip-porcelain.md` gains a `wip intake` §3
  subsection (flag set, ASK protocol, envelope, error kinds, conversation
  shape). README's Porcelain section gains the intake dogfood line.
- Dogfood capture in the commit body: a real Claude Code plan file
  round-tripped through `wip intake` into a `roadmap.md` amendment
  (or, if the canonical plan file's content overlaps shipped roadmap
  entries, an explicitly-staged equivalent). Capture: shape rounds used,
  any `ASK:` exchanges, the resulting roadmap diff.
- `.wip/initiatives/distillation/roadmap.md` step-10.5 bullet marked
  `✅ shipped <YYYY-MM-DD>` with the outcome summary.
- `.wip.yaml`'s `initiatives[0].active_step` bumped from `step-10.5` →
  `step-11`.
- `nix develop --command bin/wip-plumbing doctor` still reports zero
  drift (no manifest edits beyond `active_step`).
- Branch + commit + merge into `main` (no-ff merge commit, matches the
  pattern step-08.5 / step-09 / step-10 used).

## Open questions to resolve during execution

- **Should the porcelain re-validate the shaped artifact itself, or
  always shell out to `wip-plumbing intake validate`?** Lean: **always
  shell out.** Re-implementing the validator in the porcelain creates a
  second source of truth for shape rules. Yes, it's two extra fork-execs
  per round; for an LLM-bound pipeline that's noise.
- **Where does the shaped tempfile live?** Lean: **`$(mktemp -t
  wip-intake-shape.XXXXXX.md)`** with a trap to remove on exit. Keep in
  `$TMPDIR`. The `--output` flag is the only way to persist it. Don't
  drop it in `.wip/scratch/` automatically — that would clutter the repo
  and tempt users to treat tempfiles as durable state.
- **ASK protocol — fenced block vs JSON.** Lean: **fenced block** (one
  question, one why, one answer). JSON would be more parseable but adds
  template friction for the LLM. The fence is unambiguous enough and
  easier to instruct. If we hit real fragility we can switch to JSON in
  step-10.6+.
- **Multiple ASKs per shape attempt.** Lean: **one per attempt** in v1.
  The state machine treats each ASK as a turn; if the LLM has three
  questions it asks them serially. Multi-question ASKs are tempting but
  introduce ordering and partial-answer ambiguity; not worth the
  complexity yet.
- **What happens when `intake apply` exits 4 ("not-terminal" / "shape
  fail" on a kind the porcelain thought was already validated)?** Lean:
  **forward apply's envelope verbatim under a porcelain wrapper kind
  `apply-failed`**, with the inner `error` echoed. This should be rare
  (validate passed, so apply's shape gate should pass too); when it
  fires, the user has a path to debug both layers.
- **Should `wip intake` also accept stdin (no file arg)?** Lean: **no in
  v1.** The file path is load-bearing for classify (it reads the file
  twice) and for some validators that resolve adjacent paths. Stdin
  intake is easy to add later if real users ask for it; eliding the
  feature for v1 keeps the contract simple.
- **`--kind handoff` from the CLI.** A user could ask the porcelain to
  shape something *as* a handoff. Lean: **accept it, but immediately
  exit 4 `not-terminal` after validate** (the same way plumbing does).
  Don't try to coerce it to brief/amendment ourselves; that's the
  shaper's job when the user *doesn't* force the kind.
- **Provider `--max-tokens` / `temperature` for the shape call.** Lean:
  **defer — use provider defaults.** Adds knobs the spec must support;
  not needed for v1. If real-world shapes hit truncation, this is the
  first knob to add.
- **Should we let the user override the shaper system prompt per-kind?**
  Lean: **`--system-file <path>` overrides for all kinds in v1 (one
  prompt, the porcelain handles per-kind selection inside).** If users
  need per-kind overrides, we can split later. Lean further: defer the
  whole `--system-file` flag to step-10.6 if no concrete need surfaces
  during execution.
- **Dogfood capture mechanics.** Lean: **capture in the commit body**
  (the LLM exchange, abridged; the shaped artifact; the roadmap diff).
  Don't land any of it in tracked files outside the commit message.
  If the exchange is large, abbreviate with `[…]` and link the raw
  transcript path under `.wip/scratch/` (which is gitignored).
