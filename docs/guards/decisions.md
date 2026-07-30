# `guards` — decisions this Matter made

The design of record is `workplans/guards.md` (external, `wip-reboot`).
`guards` earns no Brief of its own — Steps only (register). This file
records the implementation-forced calls building its eight Steps required
that the workplan left to the Builder.

Status: **all eight Steps — 01 through 08 — locally complete**, including
step-08 (`manifest-install` had already sealed before this Matter opened, so
the single-Step exception carried no wait). The seal condition — all five
doctor checks implemented with tests; render refuses on tracked `.wip/` — is
discharged by `internal/guards`'s and `internal/guards/trackedwip`'s own unit
tests plus `internal/cli/guards_e2e_test.go`'s end-to-end coverage. The
`reviewed-local` gate is the user's to close.

## Doctor's two shapes: a hard-failing check kept apart from a flat findings
## registry

The workplan resolves doctor's own exit posture as "no severity levels; a
flat findings list; exit 0 clean / exit 1 with findings" and separately
observes that `tiers` step-04's unknown-clone check is "one check of the
eventual five-check doctor set." Read together and literally, those two
statements are in tension: unknown-clone's own hard-failure branch (no known
clone *and* no remote match) is — by MODEL §11's own words — a refusal, not a
finding, and the existing regression coverage this Matter must not disturb
(`TestTiers_UnknownClone_Doctor_RefusalJSONMode`,
`internal/cli/tiers_e2e_test.go`) asserts exit code **3** for that case, not
1. Folding it into the flat findings list would either lose that hard-fail
behavior or invent a severity tier ("this one finding is worse than the
others") the workplan's own resolved call explicitly rejects.

The call made here: `tiers.CheckUnknownClone` keeps its own call site in
`internal/verbs/doctor`, run *ahead of* the new registry, and is allowed to
return an error that aborts the command immediately (propagating to exit 3)
exactly as it already did before this Matter existed. The four checks this
Matter actually owns — cycles, gate-order, tracked-`.wip/`, stale-harness —
run afterward through `internal/guards.Run`, a small ordered-list-of-checks
registry (`guards.Check = func(ctx, s, dir) ([]Finding, error)`), and only
*that* list feeds doctor's flat exit-0/1 posture. `guards.Run`'s own error
return is reserved for a check genuinely failing to run (I/O, a store
failure) — never for "the check found a problem," which is always a
`Finding`, never an `error`.

Doctor's own "there are findings" signal is a second, distinct use of
`*wiperr.Error`: after printing the findings payload to stdout (JSON or
human), a non-empty findings list returns `wiperr.New("doctor.findings-
present", …)`. Its code carries neither the `refusal.` nor `internal.`
prefix, so `internal/exitcode.FromError` falls through to `UserFail` (1) —
the same bucket every plain `validation.*` code already lands in, chosen
deliberately over inventing a third prefix convention for one verb.
`internal/cli.Execute` renders this to stderr the same way it renders any
other exit-1 error, which means a dirty `doctor --json` run prints its
findings on stdout *and* a terse `{"error":{"code":"doctor.findings-
present",...}}` summary on stderr. This was checked against chassis's own
stdout discipline ("stdout is always either valid JSON or empty, never
polluted by an error") before accepting it: the rule is about stdout, and
doctor's stdout stays exactly the `{"findings":[...]}` shape step-05 asks
for regardless of exit code — the stderr line is additional, not a
substitute, and chassis's own exit-code table already documents every 1/3/4
exit as carrying that envelope.

## The trackedwip subpackage: an import-cycle constraint, not a design
## preference

The natural single-package shape — `internal/guards` housing all four
checks plus the render precondition — does not compile. `internal/render`
imports `internal/writesurface` (for node resolution in `render.Render`),
and this Matter's own step-03 has `internal/writesurface`'s `DeclareGate`
call into `internal/guards` for the gate-order precondition (guards.md:
"two callers, one function… `write-surface`'s `wip gate declare` calls it").
If `internal/guards` itself also imported `internal/render` for the tracked-
`.wip/` check, the cycle would be `writesurface -> guards -> render ->
writesurface`.

The tracked-`.wip/` check and `RenderPrecondition` are the only pieces that
need `render.Current`/`render.WorktreeRoot`, so they live in
`internal/guards/trackedwip`, a one-directional dependent of `internal/
guards` (for the `Finding` type only). `internal/writesurface` never imports
`trackedwip`, so the cycle never forms. `internal/render/git.go`'s
`worktreeRoot` was exported (`WorktreeRoot`) as part of this split — `guards/
trackedwip`'s doctor-side check needs to resolve a worktree root from a bare
working directory the way the render precondition's already-resolved
`Current` does not.

## Gate-order monotonicity: the two-caller function, and what "exhaustive"
## means for a pairwise check

`guards.Violations(declared []store.GateDeclaration) []Violation` is the one
function both callers read, mirroring `schema`'s cycle-check pattern with
ownership reversed (this Matter owns the implementation; `write-surface`
only calls it). It walks every pair of declared bindings where one gate
precedes another in the fixed order (`verified < reviewed-local < reviewed <
ci-green`; `push` is the workplan's own ordering landmark and is never
declared) and reports every pair whose scale went strictly finer going
forward — not just the first, per D65, which this Matter treats as applying
to gate-order the same way guards.md states it explicitly for cycles.
`guards.WouldViolate` (the add-time precondition `wip gate declare` calls)
is built on top of `Violations` rather than a separate check, so the two
callers really do share one definition of a violation; it also excludes any
existing binding for the gate being redeclared before checking, so
redeclaring a gate at a new scale is never compared against its own prior
binding.

## The stale-harness-artifact check needed `doctor` to grow a constructor
## signature

`CheckStaleHarnessArtifact` calls `manifest.Build(root, build)` to compute
what the current binary would generate — the same call `wip manifest`/`wip
install` already make — which means it needs the fully-assembled root
`*cobra.Command` and the build-info triple. `internal/verbs/doctor.Command`
therefore grew from `Command(streams)` to `Command(streams, build, root)`,
the exact signature `wip manifest`/`wip install` already use, and
`internal/cli/root.go`'s registration call was updated to match. This is the
one place this Matter's step-08 touched code outside `internal/guards` and
`internal/verbs/doctor` itself, beyond the `write-surface`/`render-scratch`
call-site wiring both already documented as this Matter's obligation to
fill.

## Everything else

Every other property the workplan's Steps ask for is exercised as written:
`doctor`'s check registry (step-01) is confirmed not to have disturbed the
pre-existing unknown-clone check
(`TestGuards_Doctor_UnknownCloneCheckStillWorksThroughTheRegistry`); the
cycle and gate-order checks are each exercised at both call sites
independently (`internal/cli/guards_e2e_test.go`'s
`TestGuards_Doctor_FindsABlockedByCycleLandedSomeOtherWay`/
`TestGuards_GateDeclare_RefusesGateOrderViolation` and their `doctor`-side
counterparts); the tracked-`.wip/` check and the render precondition are
checked independently so a regression in one cannot hide behind the other
(`TestGuards_TrackedWipDir_DoctorFindingAndRenderRefusal`, which also
confirms the render refusal lands before any file is written); and every
message reused from `vocabulary`'s ratified drafts is compared verbatim
(`internal/guards/gateorder_test.go`'s
`TestViolationMessage_MatchesVocabularyStep12`, and the exact string
`internal/selftest`'s pre-existing fixture command already manufactured
ahead of this Matter existing). `advisory.stale-harness-artifact` remains
flagged as provisional in `internal/guards/staleharness.go`'s own doc
comment, unchanged from the workplan's own framing, pending
`manifest-install`'s Brief reconciling it — that Brief has since sealed and
ratified the code as-is (`workplans/manifest-install.md`, "What `doctor`
drift output looks like"), so no further action is owed here.
