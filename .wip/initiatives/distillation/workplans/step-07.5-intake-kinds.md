# Workplan — step-07.5 · intake kinds + classify/validate (+ apply skeleton)

Generalizes the v0 intake validator into the full plumbing surface from
[ADR-0009](../../../../engineering/decisions/0009-intake-as-pipeline.md) +
[`engineering/specs/intake-kinds.md`](../../../../engineering/specs/intake-kinds.md):
`classify` (heuristic kind detection), per-kind `validate`, and `apply` with
dispatch routing. The destination verbs (`roadmap amend`, `workplan init`)
land in step-08.5; this step ships `apply`'s routing skeleton with stub-exit
behaviour for those destinations.

## Decisions (made here, feed later steps)

- **Layout:** new `lib/wip/wip-plumbing-intake-lib.bash` (functions prefixed
  `wip_intake_*`) sourced by the dispatcher. The
  `lib/wip/wip-plumbing-subcommands/intake.bash` from step-07 is rewritten:
  the v0 single-shape validator becomes one of the per-kind rule bodies.
- **Front-matter parsing:** awk-based scanner extracts the `---`-delimited
  YAML head from the top of the file, then `yq -o=json '.'` parses it. No
  fallback for trailing front-matter. Files without front-matter parse as
  empty `{}`.
- **`classify` "existing slug" lookups:** classify reads `.wip.yaml`
  (`wip_find_root`) to resolve `target:` keys. If no manifest is reachable,
  the heuristic that depends on it downgrades to `low` confidence with a
  signal `no-manifest`, never errors. This keeps `classify` usable on inbound
  artifacts before a repo is fully scaffolded.
- **`validate` per-kind rules:** kinds enforced per intake-kinds.md §2/§3:
  - `brief` — H1 + `## Goal`|`## Summary`. `target:` referencing an existing
    initiative slug is a validation failure (use `amendment` instead).
  - `amendment` — `target: <slug>` in front-matter; exactly one of
    `insert-after`/`replace`/`append-round`. Body has the
    directive-appropriate heading per §3 (`### step-XX — <title>` for
    insert-after; replacement body for replace; `## Round N — <title>` plus
    at least one `### step-NN —` for append-round). Slug existence is checked
    when a manifest is reachable.
  - `workplan-seed` — `target: <slug>/<step-id>` in front-matter; narrative
    body (no required sections). Step-existence check is **best-effort** in
    step-07.5: when a manifest + roadmap.md are reachable, parse the roadmap
    for `### step-NN — ` headings; if the target step is absent, validation
    fails (`missing: ["step-not-in-roadmap"]`). When unreachable, validation
    passes with a signal.
  - `spec` — fallback heading check (`## Summary` + one of `## User stories` /
    `## Requirements`). LDS delegation is deferred (no LDS verb surface yet
    per ADR-0006).
  - `handoff` — parseable + H1 only. Always valid as long as parseable.
- **`apply` routing:** dispatches per intake-kinds.md §2:
  - `brief` → calls `wip_plumbing_cmd_init <derived-slug>` (slug derived from
    front-matter `slug:` if present, else humanized H1).
  - `amendment` → exit **3** `not-implemented` ("step-08.5: `roadmap amend`
    not yet shipped").
  - `workplan-seed` → exit **3** `not-implemented` ("step-08.5: `workplan
    init` not yet shipped").
  - `spec` → exit **3** `not-implemented` ("LDS seam not yet wired").
  - `handoff` → exit **4** `not-terminal` per spec.
- **Output envelope:** keep the step-07 `{ok, file, kind, valid, missing[]}`
  shape for `validate`; extend `classify` to `{ok, file, kind, confidence,
  signals[]}` per spec §3. `apply` returns the dispatched verb's ledger
  wrapped in `{ok, kind, dispatched, target, result}` per spec.
- **No `init` re-entry from `apply` in dry-run mode regressions:** `apply
  --dry-run` collects the dispatched verb's ledger by calling it with
  `WIP_DRY_RUN=1` already set; the dispatched verb's existing dry-run path
  carries.
- **Slug derivation for brief→init:** `derived-slug` is `slug:` from
  front-matter, else `slugify(<H1 title>)` = lower-case + non-alphanumerics
  collapsed to `-` + trim leading/trailing `-`. Reject if result is empty or
  starts with a digit-only segment that the existing slug regex rejects
  (let `init`'s validator do the final check).

## Chunks

1. **intake lib** — `lib/wip/wip-plumbing-intake-lib.bash`:
   - `wip_intake_read_front_matter <file>` — emit JSON of the front-matter
     map (empty object if none). awk extracts the `---…---` head; `yq
     -o=json` parses.
   - `wip_intake_read_h1 <file>` — emit the first H1's title text, empty if
     none.
   - `wip_intake_classify <file>` — apply intake-kinds.md §4 rules in order;
     emit `{kind, confidence, signals[]}`.
   - `wip_intake_validate_brief|_amendment|_workplan_seed|_spec|_handoff
     <file> <fm_json>` — per-kind shape body; each returns a JSON
     `{valid:bool, missing:[...], signals:[...]}`.
   - `wip_intake_existing_slugs` — list initiative slugs from a reachable
     `.wip.yaml`. Empty array if unreachable.
   - `wip_intake_roadmap_steps <slug>` — emit step ids from
     `.wip/initiatives/<slug>/roadmap.md` (`### step-NN — `). Empty array if
     unreachable.
2. **intake subcommand** —
   `lib/wip/wip-plumbing-subcommands/intake.bash` rewritten:
   - `validate <file> [--kind <k>]` — if `--kind` omitted, call classify
     internally and use its `kind`. Reject classify's `low` confidence
     downgrade for `apply` (see chunk 3) but keep `validate` permissive
     (pick whatever classify guessed; validate against that). Exit 0 valid,
     **4** invalid, **2** bad args or unparseable file.
   - `classify <file>` — emit classify JSON. Exit 0 always when parseable
     and titled, **4** otherwise.
   - `apply <file> --kind <k> [--target <slug|slug/step>]` — required
     `--kind` (no implicit classify here; the porcelain is supposed to
     reshape ambiguous artifacts before terminal apply). Validate; on
     failure exit 4. Then dispatch per the routing table.
3. **brief→init slug derivation** —
   `wip_intake_derive_slug <file>` reads front-matter `slug:` first, else
   slugifies the H1 title (awk + tr). Returns empty on failure; apply then
   exits 4 `bad-slug` with a precise message.
4. **dispatcher wiring** — the existing `case` block already routes
   `intake` to the subcommand; no top-level dispatcher change required.
   Source `wip-plumbing-intake-lib.bash` from `bin/wip-plumbing` alongside
   the existing libs.
5. **tests** — `test/test-intake-classify.sh`,
   `test/test-intake-validate-kinds.sh`, `test/test-intake-apply.sh`.
   Fixtures live inline (each test mints its own `mktemp` repo with
   `.wip.yaml` + a couple of initiatives). Use `WIP_NO_REGISTRY=1`.
6. **doc updates** — drop the "v0 single-kind" caveats from spec §3
   `intake validate`; cross-link the kind table to `intake-kinds.md`
   instead of describing rules inline. Add a one-line note that
   `apply`'s `amendment` / `workplan-seed` destinations are stubbed
   until step-08.5. Update spec §4 "open questions" to mark Q3
   (`intake` validators) resolved.

## Test strategy

`WIP_NO_REGISTRY=1` throughout. Each test sets `WIP_ROOT=<tmp>` (or runs
without — for kind-only checks). Cover at minimum:

**classify:**
- `wip-kind: brief` front-matter → `brief` high.
- `target: <existing-slug>` + `insert-after: step-NN` → `amendment` high.
- `target: <existing-slug>/<step-NN>` matching real roadmap step →
  `workplan-seed` high.
- `target: <existing-slug>` alone → `amendment` medium.
- `## User stories` heading, no target → `spec` medium.
- Title + Goal only → `brief` medium.
- Title only, nothing else → `handoff` low.
- No title → exit 4.
- `target: <unknown-slug>` (with manifest present) → degrade to `handoff`
  low with signal `unknown-target`.
- No manifest reachable → `target:` lookups emit signal `no-manifest`;
  kind still inferred from other rules.

**validate per kind:**
- `brief` valid; `brief` with `target: <existing-slug>` rejected
  (`use-amendment`).
- `amendment` valid with each of three directives; missing-directive →
  `missing: ["directive"]`; extra-directive → `missing:
  ["multiple-directives"]`.
- `amendment insert-after step-XX` with body missing
  `### step-XX — <title>` → `missing: ["new-step-heading"]`.
- `amendment append-round` with body missing `## Round N — <title>` or any
  `### step-NN —` → `missing: [...]`.
- `workplan-seed` with `target: <slug>/<step-NN>` referencing a real step
  → valid; referencing a missing step → `missing:
  ["step-not-in-roadmap"]`; no manifest reachable → valid + signal.
- `spec` valid (Summary + Requirements); missing one section →
  `missing: [...]`.
- `handoff` valid as long as parseable + titled.

**apply:**
- `--kind brief` with front-matter `slug: foo` → dispatches to `init foo`;
  ledger surfaces under `result.wrote[]`; outer `dispatched: "init"`.
- `--kind brief` without `slug:` → slug derived from H1; "Auth Rework" →
  `auth-rework`.
- `--kind amendment` → exit 3, kind `not-implemented`, message references
  step-08.5.
- `--kind workplan-seed` → exit 3.
- `--kind spec` → exit 3, kind `not-implemented`, message references LDS
  seam.
- `--kind handoff` → exit 4, kind `not-terminal`.
- Missing `--kind` → exit 2.
- Shape failure pre-dispatch → exit 4 with the validate envelope.
- `--dry-run` with `--kind brief` → no writes; ledger surfaces dry-run
  wrote[] (same paths the real run would write).

## Definition of done

- `make check` green; new `test/test-intake-{classify,validate-kinds,apply}.sh`
  pass.
- `bin/wip-plumbing intake classify .wip/initiatives/distillation/BRIEF.md` →
  `{kind: "brief", confidence: "medium"}` against this repo. (Dogfood.)
- `bin/wip-plumbing intake validate
  .wip/initiatives/distillation/BRIEF.md --kind brief` exits 0.
- `bin/wip-plumbing intake apply` on a tmp `brief.md` round-trips to a new
  initiative dir + manifest entry (the dispatched-to `init` ledger is in
  the envelope).
- `bin/wip-plumbing doctor` on this repo still passes.
- Spec `wip-plumbing-cli.md` updated per "doc updates" above; the v0
  single-kind caveat is gone.
- Roadmap entry for step-07.5 marked ✅ shipped.

## Open questions to resolve during execution

- **`amendment` body validation depth.** §3 implies parsing the body of
  insert-after / append-round amendments. Validate the directive heading
  presence (`### step-XX — …` or `## Round N — …`) and at least one body
  line, but defer richer well-formedness checks (heading order, multiple
  rounds in one artifact) to step-08.5's `roadmap amend`. Lean: presence
  check only here; `roadmap amend` is the final gate.
- **`existing slug` lookup scope.** When `--project <id>` is on the
  command line, classify/validate should consult that project's manifest,
  not the cwd's. The dispatcher arg-prelude already exports `WIP_ROOT`,
  so `wip_find_root` will pick it up — confirm during impl.
- **`apply --kind brief` and the manifest gate.** If `.wip.yaml` does not
  exist at the resolved root, `apply` calls `init <slug>` which itself
  scaffolds the repo manifest. That's intentional: an inbound brief
  from a fresh repo bootstraps the manifest and the initiative in one
  call. Confirm the dispatched ledger surfaces both files under
  `result.wrote[]`.
