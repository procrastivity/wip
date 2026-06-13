# Workplan — step-08 · `status` + `next`

Implements the two "where am I / what's next" verbs from
[`engineering/specs/wip-plumbing-cli.md`](../../../../engineering/specs/wip-plumbing-cli.md)
§3. The headline value of Round 2: when these land, `wip-plumbing` answers the
question this initiative exists to make deterministic.

## Decisions (made here, feed later steps)

- **Layout:** new `lib/wip/wip-plumbing-roadmap-lib.bash` (functions prefixed
  `wip_roadmap_*`) sourced by the dispatcher — the canonical roadmap parser
  for the rest of the project. New `lib/wip/wip-plumbing-subcommands/{status,
  next}.bash`.
- **Roadmap grammar (parsed in v1):** the bullet form already in use on this
  repo, per spec §4 Q2:
  - **Round heading:** `## Round <N> — <title>` (optional trailing
    `✅ shipped <YYYY-MM-DD>` marks the whole round as shipped).
  - **Step bullet:** `- **step-<NN[.5]> — <title>**` (optional `✅`, optional
    trailing ` shipped <YYYY-MM-DD>`) followed by `—` and free-form body.
  - **Backlog section heading:** `## Backlog` (everything after is parsed as
    backlog entries until end-of-file or another `##`).
  - **Backlog entry:** `- **<title>** — <body>` (title humanized; id is
    `slugify(title)`).
  The `### step-NN — <title>` heading form referenced by intake-kinds.md is
  recognized by the parser too (amendments emit headings, not bullets), but
  is normalized to the same step record.
- **`active_step` source of truth.** Read from
  `.wip.yaml`'s `initiatives.[].active_step`. The roadmap's "first unshipped
  step" is the *inferred* active step; if it disagrees with the manifest,
  `status` trusts the manifest and `next` ranks the manifest's `active_step`
  ahead of the inferred one (signal: `manifest-step-ahead`). No write — just
  surface the divergence.
- **Round + active_step matching.** Walk the parsed rounds in order; the
  active step's round is the one whose step list contains it. `status` emits
  `round: {n, title, shipped}` for that round.
- **`status`'s dirty `.wip/`.** When `.wip/` is gitignored (default), `git
  status --porcelain -- .wip/` returns nothing → `dirty_wip_files: []`. Per
  spec §4 Q1, accept that; the porcelain layer can mtime-augment later.
- **`solo_available`.** Read from the same feature-detection path `detect`
  uses (`features.solo.active`). Reuse `wip_features_json` instead of a
  bespoke check.
- **`--initiative <slug>`.** Resolves against `.wip.yaml`'s `initiatives[]`.
  Exit 3 if unknown. Defaults to `current_initiative`; exit 3 if neither set
  nor passed.
- **`next` ranking (v1):**
  1. Manifest `active_step` if unshipped (reason: `manifest active step`).
  2. First unshipped step in the inferred-active round (reason: `first
     unshipped step in active round`) — skipped if same as #1.
  3. Subsequent unshipped steps in the active round in declared order
     (reason: `next sequential step`).
  4. Unshipped steps in later rounds, in round + step order (reason:
     `upcoming round <N>`).
  5. Roadmap's `## Backlog` entries (reason: `roadmap backlog`).
  6. `.wip/backlog.md` entries if the file exists (reason: `repo backlog`).
  When all roadmap steps are shipped, the first candidate is `roadmap
  complete` (reason: `start next round / close initiative`) with `id: null`,
  followed by the backlog.
- **Output stays flat.** Match the spec exactly — no nested
  `candidates_by_source` etc.

## Chunks

1. **roadmap lib** — `lib/wip/wip-plumbing-roadmap-lib.bash`:
   - `wip_roadmap_parse <path>` — emit a JSON document `{rounds: [{n, title,
     shipped, shipped_date, steps: [{id, title, shipped, shipped_date}]}],
     backlog: [{id, title, body}]}`. Pure awk + jq; rounds keyed by `n` so
     downstream consumers don't re-walk.
   - `wip_roadmap_active_round <doc> <step_id>` — emit `{n, title, shipped}`
     for the round containing `step_id`, or `null`.
   - `wip_roadmap_unshipped_after <doc> <step_id>` — emit a JSON array of
     `{round_n, id, title}` for every unshipped step at or after `step_id`
     in declared order. Used by `next`.
   - `wip_roadmap_first_unshipped <doc>` — `{round_n, id, title}` or `null`.
2. **status subcommand** — `wip_plumbing_cmd_status`:
   - Resolve initiative (manifest `current_initiative` or `--initiative`).
   - Read manifest fields (`status`, `active_step`) for that initiative.
   - Parse roadmap; locate the active step's round (or, if the manifest has
     no `active_step`, infer from `first_unshipped`).
   - Compute `dirty_wip_files` via `git status --porcelain -- .wip/` (empty
     when gitignored).
   - Compute `solo_available` from `wip_features_json`.
   - Emit the JSON shape from spec §3.
3. **next subcommand** — `wip_plumbing_cmd_next`:
   - Resolve initiative.
   - Parse roadmap; build the candidate list per the ranking rules above.
   - Read `.wip/backlog.md` if present; parse entries via the same backlog
     grammar (`- **<title>** — <body>`).
   - Emit `{ok, initiative, candidates: [{rank, source, id, title, reason}]}`.
4. **dispatcher wiring** — source `wip-plumbing-roadmap-lib.bash` from
   `bin/wip-plumbing` (sibling of the other libs); register `status` + `next`
   in the dispatcher's `case` block; update `wip_usage` so they no longer
   read *(later step)*.
5. **tests** — `test/test-status.sh`, `test/test-next.sh`, plus a focused
   `test/test-roadmap-parse.sh` that pins the parser's JSON shape against a
   curated fixture (multiple rounds, mixed shipped/unshipped, .5 step ids,
   backlog entries). Use `WIP_NO_REGISTRY=1` + `WIP_ROOT=<tmp>` throughout.
6. **doc updates** — flip step-08's roadmap entry status when this lands;
   mark spec §4 Q1 (dirty `.wip/`) and Q2 (roadmap parsing) resolved with
   one-line outcomes; cross-link the roadmap-lib from spec §3 entries for
   `status` and `next`.

## Test strategy

**roadmap-parse fixture** covers: a shipped round (✅ marker), an unshipped
round, `.5` step ids, a shipped step with body, an unshipped step, a
`## Deferred` section ignored, a `## Backlog` section with two entries.
Assert the JSON shape line-by-line via `jq`.

**status** covers:
- happy path against the fixture (manifest active_step + round metadata
  resolve correctly);
- `--initiative <unknown>` → exit 3;
- no `current_initiative` and no `--initiative` → exit 3;
- gitignored `.wip/` → `dirty_wip_files: []`;
- `solo` enabled-with-no-sentinel → `solo_available: true`;
- `solo` disabled → `solo_available: false`;
- manifest `active_step` divergent from roadmap (manifest names a shipped
  step) → status surfaces both, signal `manifest-step-ahead`.

**next** covers:
- active step is manifest-named and unshipped → rank 1 = manifest, rank 2 =
  next sequential;
- active step is shipped (or manifest empty) → rank 1 = inferred first
  unshipped;
- all roadmap steps shipped → rank 1 = "roadmap complete" with `id: null`;
- backlog entries appear after roadmap steps;
- `.wip/backlog.md` present → its entries appear after roadmap backlog;
- `--initiative` switches the target.

## Definition of done

- `make check` green; the three new test files pass.
- `bin/wip-plumbing status` on this repo emits `initiative: "distillation"`,
  `round.n: 2`, `active_step.id: "step-08"`, `active_step.shipped: false`.
- `bin/wip-plumbing next` on this repo emits at least one candidate; the
  first candidate is `step-08` (manifest active step). The list does not
  duplicate `step-08`.
- `bin/wip-plumbing status --initiative bogus` exits 3.
- `bin/wip-plumbing doctor` on this repo still passes.
- Spec `wip-plumbing-cli.md` updated per "doc updates" above.
- Roadmap step-08 marked ✅ shipped.

## Open questions to resolve during execution

- **Backlog id collision.** Slugified titles could collide across rounds vs
  `.wip/backlog.md`. Lean: emit them with their source (`source: "roadmap"`
  vs `"backlog"`) and accept that ids may repeat; ranking still works
  because `source` is part of the key.
- **Round-shipped detection.** A round is shipped iff its `## Round`
  heading carries `✅` *or* every declared step is shipped. Lean: trust the
  explicit `✅` marker on the round line when present; otherwise compute
  from steps. Surface the computed value as `round.shipped`.
- **`next`'s "all shipped" candidate id.** `null` reads cleanly in JSON but
  forces porcelain to special-case it. Alternative: a sentinel string like
  `"<roadmap-complete>"`. Lean: `null` + a stable `reason` is sufficient;
  porcelain branches on `reason`, not `id`.
