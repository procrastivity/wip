# `read-surface` — decisions this Matter made

The design of record is `workplans/read-surface.md` (external, `wip-reboot`).
`read-surface` earns no Brief of its own (Steps only) — it reads `schema`'s
Brief for the envelope/entity/gate storage and `tiers`'s Brief plus its
`tier-verbs` Stage for tier scoping and addressing. This file records what
building the six Steps forced: the calls those Briefs left open, and where
this Matter had to invent a convention neither upstream Brief already
carried.

Status: **all six coding Steps (01–06) plus tests (07) locally complete.**
The seal condition — founding questions answerable from the store; cursor
moves appear in the event log; Session derived, never persisted (D17) — is
discharged; see `internal/readsurface`'s package tests and the e2e test in
`internal/cli/readsurface_e2e_test.go` for the evidence. The `reviewed-local`
gate is the user's to close.

## What this Matter builds on

`schema` had already anticipated this Matter's needs almost completely: the
`cursors` table, `TypeCursorMoved`, the `CursorMoved` payload (subject = the
Worktree, per its own doc comment), and `moveCursor`'s projection fold all
existed before this Matter opened — read-surface's step-01/04 confirm and use
that contract rather than inventing it, the same posture `tiers`'s own
step-01 took toward its tier tables. Two additive read methods were added to
`store.View`, in the same spirit as `tiers`'s and `write-surface`'s own
additions: `Planned` and `Done`, mirroring `InProgress`'s exact shape (same
`nodes_lifecycle` index, just a different lifecycle value in the `WHERE`) —
the store-wide, tier-free raw material MODEL D66 says the founding predicates
should be, with the asking surface (`status`/`next`) narrowing to its own
read scope afterward.

## The locally-complete/sealed predicate lives here, not in `schema`

D13/D55/D62 define *locally complete* and *sealed* at the model level, but no
earlier Matter had to compute either as a predicate over a live node — gate
*storage* was `schema`'s job (step-07), gate *closing* was `write-surface`'s
(its `gates-and-dependencies` Stage), but nothing before this Matter asked
"is this specific node locally complete right now." `LocallyComplete` and
`Sealed` (`internal/readsurface/frontier.go`) implement D62 literally: own
scale for locally complete, walking the `Parent` chain adding each ancestor's
own-scale gates for sealed. Two things worth stating because they are not
guessable from the model text alone:

- **An empty declaration set at a scale completes at Done** (D62, taken
  literally) — which is why every Step and Stage in this dogfood is locally
  complete the instant it's Done: nothing is ever declared at Step or Stage
  scale here, only `reviewed-local: matter`.
- **`Sealed`'s ancestor walk checks gate closures against each ancestor node,
  not that ancestor's own lifecycle.** D55 says sealed "adds every
  enclosing-scale gate closed against its enclosing node" — it does not say
  the enclosing node must itself be Done. So a Done Step's Sealed-ness can
  flip from false to true the moment its Matter's gate is closed, even if
  that Matter is still In Progress. This reads strangely on first encounter
  and is exercised deliberately in
  `TestSealed_BelowMatterScaleCanDifferFromLocallyComplete` so it is a known
  property rather than a surprise a future Matter trips over.

## The unblocked frontier is literal, not depth-restricted

`vocabulary`'s drafted `next` outputs use Matters and one Stage as their
worked examples (output 4's "3 Matters unblocked"), which could be misread as
"the frontier only ever contains top-level nodes." It doesn't: `Frontier`
(step-02) is exactly what the workplan states — every Planned node, any
scale, whose in-force `blocked-by` edges are all satisfied at locally
complete. A Step with no explicit edges is exactly as "unblocked" as its
sibling Steps; nothing in P1 auto-serializes siblings (D51: sibling order is
presentation-only, and MODEL never claims otherwise for Phase 1). The smoke
test under `wip status`/`wip next` against a real seeded tree confirms this
literal reading produces sensible output — individual ready Steps appear
alongside ready Matters, not just the coarsest grain.

## Addressing convention (invented here, no Brief owned it)

Neither Brief fixes how a node renders as a display locator for `status`'s
content sections or `next`'s cursor position. `internal/readsurface/locator.go`'s
`Address` invents the smallest rule consistent with `vocabulary`'s own
drafted output 2 (`tiers/tier-verbs · step-04`): a Matter is its own locator;
a Stage is `<matter>/<stage>`; a Step is `<matter> · <step>` when it hangs
directly off its Matter, or `<matter>/<stage> · <step>` when a Stage groups
it. The `/` marks structural containment (as `write-surface`'s own addressing
convention already reads `<matter>/<stage>/<step>` locators), and the ` · `
marks "and specifically this node" — vocabulary's own choice of separator in
its worked example, reused rather than invented from nothing.

**This Matter reproduces `write-surface`'s locator-resolution dispatch
(`internal/readsurface/locator.go`'s unexported `resolveLocator`) rather than
importing `internal/writesurface.ResolveNode`.** The workplan is explicit —
"the write verbs are not a dependency (no edge to write-surface)" — and while
that sentence is about test fixtures, the spirit (this Matter's read-only
posture should not gain a compile-time dependency on the write surface) reads
the same way for ordinary code. The duplication is small (~25 lines, ULID
shape dispatch or `/`-segment resolution against `MatterByLocator`/
`NodeByLocator`) and both copies read the same two `store.View` methods, so a
future divergence would be visible rather than silent.

## `wip next --set` needs a resolved Worktree, not just a Clone

`tiers`'s Brief carves out `status` alone from the unknown-clone hard failure
(MODEL §11); every other verb, including this Matter's `next`, inherits the
ordinary refusal. But the cursor keys at Clone **and** Worktree (D38), and
`tiers`'s own Move-detection section only guarantees a Worktree row for
locations that have themselves been `wip init`'d — a linked worktree of an
already-known Clone that has never been `init`'d there has no Worktree row
yet. `readsurface.ResolveCurrent` (step-01) therefore raises the same
`refusal.unknown-clone` message for *both* gaps: an unknown Clone and a known
Clone whose current Worktree has never been recorded. The remedy is
identical either way (`wip init` here), so reusing the one drafted message
rather than inventing a second refusal reads correctly from the user's side
even though the two internal causes differ.

## The dangling cursor is a sixth `next` result, not a variant of the five

D67 calls a dangling cursor (target tombstoned, Canceled, or sealed) "a
first-class result, not an error" — but it is also not one of vocabulary's
five drafted outputs, none of which anticipate it. `readsurface.Kind` adds a
sixth value, `Dangling`, carrying the specific reason (`"sealed"`,
`"canceled"`, or `"removed"` — the last covering both "genuinely tombstoned"
and "no longer resolvable by identity", since a tombstoned node's own read
already fails the same way an unknown identity would) and the same unblocked
candidate list output 4 shows. Its rendering deliberately echoes output 4's
shape (`next --set <locator>` as the way out) without literally reusing its
wording, since the situation is different: a candidate list here is offered
because the *current* cursor stopped being useful, not because none was ever
set. `TestNext_DanglingCursor_Sealed` also pins D67's other half — the read
never moves the cursor itself — by asserting the `cursor.moved` count is
unchanged after a `Next` call that discovers a dangling target.

## Session's two-pass derivation for D59's discount rule

"Neither extends a working period nor bridges an idle gap" (D59, for
`dispatch.closed` events with reason `superseded`/`reaped`) is stricter than
"ignore these events": an administratively-closed dispatch sitting *between*
two real working stretches must not be allowed to look like the bridge that
joins them into one session, but if it happens to fall *inside* a session
evidence already spans, it should still show up in that session's narration
— it did happen, D59 only discounts its role as a boundary marker.
`Derive` (step-05) therefore runs two passes: the first computes session
`Start`/`End` boundaries from the evidence stream alone (every event except
discounted closes); the second walks the *whole* log once more and attaches
every event — discounted closes included — whose `occurred_at` falls inside
an already-computed window. `TestSession_DiscountedDispatchCloseNeitherExtendsNorBridges`
and `TestSession_DiscountedCloseInsideARealWindowIsStillShown` pin both
halves against the same construction so neither can regress without the
other's test catching it.

## A store-package addition this Matter needed for its own tests: `OpenWithClock`

Session's derivation logic turns on inter-event gaps measured in hours, and
the store's `events` table is genuinely append-only — `events_no_update` and
`events_monotonic` (schema's own triggers) mean there is no way to place an
event at a chosen `occurred_at` after the fact, and no existing seam let a
test control the clock `id` minting reads at write time. `store.OpenWithClock`
(alongside the existing `Open`) injects that clock into the id source; `Open`
itself is unchanged (`time.Now`, as always) and every other caller of the
now-internal `openAt` was updated to pass it explicitly rather than relying on
a default. This is the same category of addition `tiers`'s "additive read
methods and a test escape hatch" commit made, applied to the write path
instead of the read surface, because nothing about *reading* the log needed
to change — only how a test could drive it.

## Two earlier Matters' tests updated, both anticipated by their own comments

- **`internal/config/config_test.go`**'s `TestLoad_NoOverride_ReturnsShippedDefault`
  asserted the shipped `config.default.yaml` carried zero keys, with the
  comment "chassis owns the mechanism, not any config key" — exactly the
  state `chassis`'s own Brief describes as provisional ("ships empty... until
  a later Matter earns a tool-config key"). `read-surface` step-06 is that
  Matter; the test now asserts the one key it earns (`idle_gap: 6h`) instead
  of an empty map.
- **`internal/cli/worked_examples_test.go`**'s `TestWorkedExample7_CrossRepoBatch`
  asserted `wip status`'s output byte-for-byte, with an inline comment
  reading "that is `read-surface`'s extension, reading this Step's output as
  settled input" — `tiers`'s own worked-examples Stage wrote that sentence
  anticipating this exact change. The assertion now includes the "next to
  start" section read-surface adds (the two seeded Matters, both Planned with
  no blockers), while confirming the Batch's cross-repo membership and
  repo-b's content still never leak into repo-a's tier-scoped view.

## `status`'s "finished" bucket can show a third state, described not named

D13 asks `status` to distinguish sealed from locally complete; MODEL §2.3
separately notes that a Done node whose own gate is still open ("all
children Done, own gate open") is *described* in output, never given a name
in the vocabulary. `finishedLines` (`internal/verbs/status/status.go`)
therefore renders three states, not two: `"sealed"`, `"locally complete"`
(the case that only exists below Matter scale, D55), and — when neither
holds — `"awaiting gate"`, a plain description rather than a coined term.
This only fires for a Done Matter whose `reviewed-local` gate has not yet
been closed, since nothing below Matter scale has a gate declared against it
in this dogfood.

## Everything else

Every other Step's behavior — `status`'s repo-wide/host-wide scoping
untouched (`tiers.Status`/`tiers.StatusView` are read, never modified);
`next`'s five vocabulary outputs rendered close to their drafted text, with
generic phrasing where a candidate list can hold a mix of Matters/Stages/Steps
rather than the all-Matters case vocabulary's own example happened to use;
`cursor.moved` carrying `repo`+`clone`+`worktree` (an execution event,
`schema`'s own taxonomy already required this) and `payload.previous`
recording the prior target — is exercised as documented, with no divergence
from either Brief.
