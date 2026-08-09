# Trace 7 — gate failure and recovery

PLAN 1.7's seventh trace (renumbered at Step 0): a Matter that declares
`ci-green`, reaches Done, and cannot seal — CI goes red, the Warden
reacts, CI comes back green, and the gate closes (`orch-traces` step-02).

This trace is deliberately two-paned in a way traces 1–6 were not. The
live forge seam is Phase 4, and the system encodes that honestly: Warden
activation *derives* today (D14) but the role is **inert** — "a
hand-closed CI gate would be a seal that lies" (`writesurface/role.go`).
So the first pane drives the real binary to the exact boundary of what
exists — the declaration, the activation derivation, Done-but-unsealed,
and the full refusal lattice that keeps `ci-green` truthful — and the
second pane narrates the Phase 4 event shapes past that boundary, exactly
as the P1 workplans narrated event sequences for verbs that did not yet
exist. Fixture: throwaway store, runtime dir, and clone, as in trace 6.

## Live pane — driven to the inert boundary

Commands driven, in order:

```
$ wip init
$ wip gate declare ci-green --scale matter        # project config — no domain event
$ wip gate declare reviewed-local --scale matter  # the human-owned contrast gate
$ wip matter create --title "harden the release pipeline"
$ wip step create harden-the-release-pipeline --title "pin the runner image"
$ wip step create harden-the-release-pipeline --title "add the smoke stage"
$ wip refresh
$ wip role active
$ wip start harden-the-release-pipeline/step-01 ; wip finish harden-the-release-pipeline/step-01
$ wip start harden-the-release-pipeline/step-02 ; wip finish harden-the-release-pipeline/step-02
$ wip finish harden-the-release-pipeline
$ wip status                                      # Done, both gates open
$ wip role spawn warden                           # refused: inert
$ wip gate close ci-green harden-the-release-pipeline              # refused: owner
$ wip --as-role warden gate close ci-green harden-the-release-pipeline  # refused: no spawn
$ wip gate close reviewed-local harden-the-release-pipeline        # closes — human-owned
$ wip status                                      # still awaiting gate
```

`wip role active` — the gate list read from the role side (D14). Declaring
`ci-green` activates the Warden *by derivation*, and the same row states
why it still cannot act:

```
verifier   owns verified             declared:              dormant
warden     owns reviewed, ci-green   declared: ci-green     inert until the forge seam (Phase 4)
```

The refusal lattice, verbatim — three distinct refusals, one per
escalation, each naming its rule:

```
$ wip role spawn warden
wip: role spawn: warden activates by declaration (D14) but stays inert until the forge seam exists (Phase 4)

$ wip gate close ci-green harden-the-release-pipeline
wip: gate close: ci-green is closed by its owning role warden (D14); spawn it and run under --as-role warden

$ wip --as-role warden gate close ci-green harden-the-release-pipeline
wip: store: actor role:warden claims a role with no open spawn behind it; `wip role spawn warden` first (MODEL §6, D59)
```

Every path to a lying seal is closed *structurally*: the gate needs its
owner's actor, the actor needs an open spawn, and the spawn refuses while
inert. MODEL §2.3's "cannot close until their owning role exists" is
enforced, not disciplinary.

`wip status` after `reviewed-local` closed — one gate down, the Matter
still honestly short of sealed (D55: sealed needs *every* declared gate):

```
local repo 01KZJ8AP5ATVB04TDY2WJRNTZ7
  clone              Clone · current

finished:
  harden-the-release-pipeline matter · awaiting gate
  harden-the-release-pipeline · step-01 step · locally complete
  harden-the-release-pipeline · step-02 step · locally complete
```

### Live event table

Alias legend: `repo-B` = `01KZJ8AP5ATVB04TDY2WJRNTZ7` · `clone-B` =
`…NTZ8` · `worktree-B` = `…NTZ9` · `matter-R` =
`01KZJ8AP8KH02ZEF7YXDDTG7RE` (`harden-the-release-pipeline`) ·
`step-R1`/`step-R2` = its Steps · `disp-plain` = the `wip refresh`
bracket. The three tier-attachment events are omitted as in every trace.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-08-09T03:16:12.179Z | human | ev-01 | ev-01 | repo-B |  |  | matter-R | `{"title":"harden the release pipeline","locator":"harden-the-release-pipeline","sort_key":0}` |
| ev-02 | step.created | 2026-08-09T03:16:12.221Z | human | ev-02 | ev-02 | repo-B |  |  | step-R1 | `{"title":"pin the runner image","locator":"step-01","parent":"matter-R","sort_key":1000}` |
| ev-03 | step.created | 2026-08-09T03:16:12.276Z | human | ev-03 | ev-03 | repo-B |  |  | step-R2 | `{"title":"add the smoke stage","locator":"step-02","parent":"matter-R","sort_key":2000}` |
| ev-04 | dispatch.opened | 2026-08-09T03:16:12.337Z | human | ev-04 | ev-04 | repo-B | clone-B | worktree-B | disp-plain | `{}` |
| ev-05 | render.performed | 2026-08-09T03:16:12.361Z | human | ev-05 | ev-05 | repo-B | clone-B | worktree-B | disp-plain | `{}` |
| ev-06 | matter.started | 2026-08-09T03:16:12.435Z | human | ev-06 | ev-06 | repo-B |  |  | matter-R | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-07 | step.started | 2026-08-09T03:16:12.436Z | human | ev-06 | ev-06 | repo-B |  |  | step-R1 | `{"from":"planned","to":"in-progress"}` |
| ev-08 | step.finished | 2026-08-09T03:16:12.503Z | human | ev-08 | ev-08 | repo-B |  |  | step-R1 | `{"from":"in-progress","to":"done"}` |
| ev-09 | step.started | 2026-08-09T03:16:12.558Z | human | ev-09 | ev-09 | repo-B |  |  | step-R2 | `{"from":"planned","to":"in-progress"}` |
| ev-10 | step.finished | 2026-08-09T03:16:12.625Z | human | ev-10 | ev-10 | repo-B |  |  | step-R2 | `{"from":"in-progress","to":"done"}` |
| ev-11 | matter.finished | 2026-08-09T03:16:12.687Z | human | ev-11 | ev-11 | repo-B |  |  | matter-R | `{"from":"in-progress","to":"done"}` |
| ev-12 | gate.closed | 2026-08-09T03:16:21.143Z | human | ev-12 | ev-12 | repo-B |  |  | matter-R | `{"gate":"reviewed-local","scale":"matter"}` |

Two things the live table proves by *absence*: the gate declarations
emitted no events (they are project config, not domain history — `wip
gate declare`'s own help says so), and the three refusals emitted no
events (a refused write leaves no trace; the log is what happened, never
what was attempted). The `ci-green` failure state is likewise not an
event today — it is the open gate itself, readable as `awaiting gate`.

## Narrated pane — the Phase 4 shapes (paper evidence)

What the same story looks like once the forge seam exists. Event `id`s
are prefixed `nv-` to mark narration; envelope discipline (actor,
causation/correlation, tier dimensions) follows MODEL §10 exactly; the
`forge.*` type names are illustrative — the P4 forge family is reserved
in MODEL §10 but its taxonomy is Phase 4's to fix. The scene resumes at
this trace's live ending: matter-R Done, `reviewed-local` closed,
`ci-green` open, the branch pushed.

| id | type | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|
| nv-01 | role.spawned | human | nv-01 | nv-01 | repo-B | clone-B | worktree-B | warden-1 | `{"dispatch":"disp-plain","name":"warden"}` |
| nv-02 | forge.ci-observed | role:warden | nv-02 | nv-02 | repo-B | clone-B | worktree-B | matter-R | `{"pipeline":"release","status":"red"}` |
| nv-03 | forge.ci-observed | role:warden | nv-03 | nv-03 | repo-B | clone-B | worktree-B | matter-R | `{"pipeline":"release","status":"green"}` |
| nv-04 | gate.closed | role:warden | nv-03 | nv-03 | repo-B |  |  | matter-R | `{"gate":"ci-green","scale":"matter"}` |
| nv-05 | role.closed | role:warden | nv-05 | nv-05 | repo-B | clone-B | worktree-B | warden-1 | `{"reason":"completed"}` |

The narration's load-bearing choices, each anchored to a settled rule:

- **The Warden is an outer-loop watcher, not a turn** (MODEL §6): one
  spawn (nv-01) brackets the whole red-to-green wait — "may wait an
  hour, may come back negative" — where a Builder would have closed at
  the end of its bounded piece of work.
- **CI red is an observation event, not a gate event.** The gate table
  has no reopen and needs none: `gate.closed` is the *only* gate event
  family, and failure is the gate staying open with the failure
  observation on the record. Between nv-02 and nv-03 the Matter still
  reads `awaiting gate` — the same durable answer the live pane showed,
  now with the *reason* first-class in the log. System-driven events as
  first-class citizens is exactly what MODEL §10 says the fidelity rules
  exist to protect.
- **Recovery is a chain, not a coincidence.** nv-04 carries
  `causation`/`correlation` = nv-03: the green observation *entailed*
  the close, one command's chain (D57), recoverable from either event.
  Only after nv-04 does matter-R's derived state flip from
  `awaiting gate` to `sealed` — sealed remains a predicate over gate
  state, never an event of its own (D55).
- **The close survives the owner check as structure.** nv-04's actor is
  `role:warden` with the open spawn nv-01 behind it — the same two
  checks the live pane showed refusing everything else are what admit
  this one write.
- **The role census stays honest.** After nv-05, a `wip session` over
  this story would count `role:warden 1 spawned · 1 closed` with the
  gate close attributed to it — the outer loop visible in Session, which
  is the reason system-driven events had to be first-class from event
  one (MODEL §10).
