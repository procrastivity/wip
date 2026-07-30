# `write-surface` — decisions this Matter made

The design of record is `workplans/write-surface.md` (external, `wip-reboot`).
`write-surface` earns no Brief of its own — it reads `chassis`, `vocabulary`
and `schema`'s Briefs. This file records what building the three Stages
forced: the calls those Briefs left open, and where this Matter had to invent
a convention none of the three upstream Briefs already carried.

Status: **all three Stages — `birth-and-amendment`, `content-prose`,
`gates-and-dependencies` — locally complete.** The Matter's seal condition —
every write path is a verb emitting exactly one event (§10 invariant 1); no
separate Brief — is discharged; see the e2e tests in `internal/cli/writesurface_*_test.go`
for the evidence. The `reviewed-local` gate is the user's to close.

## What this Matter builds on

`schema` had already built essentially the whole projection layer this Matter
needed before write-surface opened: every P1 event type's fold rule (node
birth, lifecycle transitions with the From-guard, reorder, replace, tombstone,
edges, gate state, backlog, reference-bind) already existed in
`internal/store/project.go`, and the one write path (`Store.Commit`,
`Draft`/`Cause`/`ErrNoEvent`) already made "no verb writes directly"
structural. `write-surface`'s job turned out to be almost entirely the verb
layer — Cobra commands plus the business-logic package
(`internal/writesurface`) that resolves locators and assembles Drafts — not
new storage or projection work. Two small additive read methods were added to
`store.View`, in the same spirit as `tiers`'s own additions:
`MatterByLocator` (a Matter resolves by its own locator within a Repo — no
existing method did this, since `NodeByLocator` needs the Matter's ID
already) and `EdgeBetween` (`wip depend remove`'s lookup, resolving the live
edge for a blocked/blocker pair by identity rather than scanning `BlockedBy`).

## The addressing convention (invented here, no Brief owned it)

None of the three Briefs this Matter reads fix how a CLI locator string
resolves to a node. `internal/writesurface/resolve.go` invents the smallest
consistent rule: a ULID resolves by identity; anything else is a
`/`-separated path whose first segment names a Matter and whose *last*
segment (when there is more than one) names the live node within it. A
middle segment — a Stage's own locator, in `<matter>/<stage>/<step>` — is
presentational only and is never checked, because a grouping is not a
namespace (D16): `step-04` is already globally unique within its Matter
regardless of which Stage groups it, so `<matter>/<stage>` and
`<matter>/step-04` both resolve by last segment, identically. This reading
is what `vocabulary`'s own drafted `wip next` output implies
(`tiers/tier-verbs · step-04`) without spelling out the resolution rule
itself.

**Matter locators are `slugify(title)`, checked for cross-Matter collision at
write time.** Neither Brief says how a newly birthed Matter gets its own
locator; the workplan's Step-02 text ("a positional parent locator where one
applies... plus a name/title flag") reads as: the *only* things a birth verb
takes are the parent locator (where one applies) and a title flag — so the
new node's own locator is a function of the title, not a second positional.
Stage locators are the same function, unique within their Matter (the DB's
own `(matter, locator)` index already enforces that, since a Stage's `matter`
column is its owning Matter's id). **Matter locators need an
application-level uniqueness check that the DB does not provide**: a Matter's
own `matter` column is its own id, so two different Matters can carry the
same locator string as far as the `(matter, locator)` index is concerned —
`CreateMatter` checks `MatterByLocator` first and refuses a collision
(`validation.locator-collision`) rather than leaving two Matters
indistinguishable by locator. Step locators are never a function of title —
`step-NN`, globally sequential per Matter (`NextStepLocator`, already built
by `schema`), exactly as D16 requires.

## Amendment: the one-event-common-case, two-event-rare-fallback shape

`wip step insert` computes a single sort key via `SortKeyBetween`/`NextSortKey`
in the common case — one event, `step.inserted`. The `schema` Brief itself
anticipates the case where the gap between two siblings is exhausted ("a
whole-sibling-set `step.reordered` when a gap runs out"), so when that
happens `InsertStep` commits **two** drafts in one command: a `step.reordered`
renumbering the existing live Steps to dense keys (the chain's origin), then
`step.inserted` placing the new Step in the resulting gap (`Cause: 0`). This
is the same "one command, several verbs, each emitting exactly one event"
shape D57's start-cascade uses, not a new pattern — and it is genuinely rare
(the sort-key gap of 1000 survives roughly ten successive same-position
insertions before it exhausts).

**`wip step replace`'s replacement gets the next sequential `step-NN`, never
the old locator.** Neither Brief says this explicitly; it follows from D44's
"locators are never renamed or reused" read literally — reusing the replaced
Step's old locator would be exactly a renumbering, which D16 already rules
out for removal and there is no reason replace should differ.

## Lifecycle: the diagram taken literally

MODEL §2.2's diagram draws `cancel` and `pause` as arrows out of **In
Progress** only — not out of Planned. `Cancel` and `Pause` in
`internal/writesurface/lifecycle.go` therefore require `From: InProgress`,
matching the diagram exactly rather than the perhaps-more-intuitive "you can
cancel anything not yet Done." A Planned Matter cannot be canceled directly
in this Matter's verb set; nothing in the workplan asked for that path, and
adding it would be inventing a sixth transition the model doesn't draw.

**`start`'s cascade is a linear chain, not always caused by the origin.**
Each ancestor that actually needs starting is caused by the *previous* draft
in the chain (`Cause: len(drafts)-1`), not always index 0 — matching the
schema Brief's own worked example (`a:a:a, b:a:a, c:b:a`) rather than the
alternative "every event points at the origin" shape, which the Brief
reserves for same-command events with *no* causal relation to each other.
An ancestor already under way (not Planned) is simply skipped — it is not
part of the chain, and the chain's origin is whichever ancestor is the first
one that actually needed an event, which may not be the Matter.

## A documented gap: `batch.joined` on first start inside an open dispatch

The workplan's step-04 calls for this (D58): "a node's first start inside an
open dispatch emits `batch.joined` on the dispatch's batch." **This is not
implemented.** Two things are missing to do it honestly: there is no verb yet
that opens a dispatch (`render-scratch`'s `wip refresh`, not built), and the
`dispatches` table `schema` shipped carries no `batch` column at all — there
is no schema path from "the open dispatch on this worktree" to "its batch."
Implementing this now would mean guessing at a schema shape `schema`/`render-scratch`
haven't settled. Flagged here rather than silently left out:
`render-scratch` or `orchestration`, whichever wires dispatch-opening first,
is the Matter that should add this once a dispatch actually has a batch to
join.

## A documented gap: gate-order monotonicity (D12) is not called

The workplan says `wip gate declare` should call `guards`' doctor check for
gate-order monotonicity as a precondition, never re-implementing it — the
same pattern `schema` set for the static cycle check. **`guards` does not
exist yet** (`guards ← tiers, render-scratch`), so there is nothing to call;
`DeclareGate` writes the declaration unconditionally. This mirrors how
`tiers` step-04 left four of `doctor`'s five checks for `guards` to add to
the same command later — `guards`, when built, is the Matter that wires this
precondition into `write-surface`'s existing `DeclareGate` call site, not a
new verb.

**Resolved by `guards`:** `DeclareGate` (`internal/writesurface/gate.go`) now
calls `guards.WouldViolate` before writing, exactly as anticipated above —
see `docs/guards/decisions.md` for the implementation.

## `wip gate declare`/`wip gate close` stay fully general — no hardcoded gate-name block

HANDOFF §1.2 and the workplan both say declaring `verified`/`reviewed`/
`ci-green` is "barred" in this dogfood. Read against the workplan's own
fuller sentence — "not because `wip gate declare` can't express them, but
because they cannot close without their owning role and a Matter that
declares them can never seal" — this is operational discipline (the human
running this dogfood chooses not to) and a structural consequence (no role
exists to close them, Phase 2), not a code-level refusal. Neither verb
hardcodes a blocklist; `TestGateDeclare_WritesConfigEmitsNoEvent` confirms a
forge gate is technically declarable, on purpose, so a future decision to
harden this reads as a real design change rather than the removal of dead
code.

## Content verb argument shapes: `agent-path`'s resolution, implemented here

`write-surface`'s own `content-prose` Stage explicitly defers argument shape
(argument/stdin/scratch-file, D45) to the `agent-path` workplan. `agent-path`
had already resolved this (its own step-01, written up front along with every
other workplan in this dogfood's register): stdin by default or `--file
<path>` for `wip brief`/`wip workplan`/`wip body`; a positional argument by
default, falling back to stdin/`--file`, for `wip finding add`. Since the
resolution already existed as text, this Matter implements it verbatim
(`internal/verbs/content`) rather than inventing a placeholder shape that
`agent-path` would later have to redo. What is genuinely `agent-path`'s own
remaining work — not built here — is the D45 scratch-file *consumption* half
tied to a real dispatch (`render-scratch`'s `wip refresh` and its
`.wip/work/<dispatch-id>/` directory don't exist yet): `--file <path>` here
reads whatever path it is given, once, with no dispatch-scoped machinery
behind it.

**"No stdin and no `--file`" is a plain (non-`wiperr`) Go error**, which
`chassis`'s exit-code mapping routes to code 2 (usage) — matching
`agent-path` step-01's literal spec that a bare invocation with neither input
is a usage error. This could not be exercised end-to-end through
`exec.Command` in this Matter's own test suite: the distinction turns on
`os.Stdin` being a real terminal (`os.ModeCharDevice`), and a subprocess's
stdin, piped or not, is never a TTY. Noted in
`internal/cli/writesurface_content_test.go` rather than asserted against a
case the harness cannot produce.

## Everything else

Every other Step's behavior (one event per write for lifecycle/content/
dependency/gate/backlog/bind; `payload.kind` discrimination; tombstones vs.
Canceled; the cycle check called, not reimplemented; the bind verb's P1
inertness) is `schema`'s projection layer exercised as documented, with no
divergence from either Brief.
