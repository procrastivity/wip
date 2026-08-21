# Tracker gate read verification

Date: 2026-08-21

Matter: `tracker-gate-read`

Verdict: **Positive**

## Incident replay

`TestRrulerReplayAlreadyDoneIssueProducesNoCloseOffer` reconstructs the
2026-08-20 rruler case with the original Matter locator
`v001-release-framing` and tracker reference `BDS-132`.

The fixture uses a read-capable Linear-shaped provider seam. The provider
reports BDS-132 as `Done` with lease `2026-08-20T13:48:49Z`, the time of the
recorded In Review to Done transition. The repo push level is `off`. The
Matter finishes, then its final `reviewed-local` gate closes.

The final gate close reads BDS-132 exactly once. The completed provider state
aligns with wip's completed aggregate, so the command prints only its normal
gate-close line. It does not offer to close BDS-132, create outbox work, or
write to the provider.

## Behavior matrix

The focused tests cover these rows:

| Case | Evidence | Result |
| -- | -- | -- |
| Matter finish seals | `TestPostSealAlignmentThroughFinishAndFinalGate` | Reads and reports a behind tracker. |
| Final Matter gate seals | same test | Reads once; aligned output is silent. |
| Gate before finish | `TestSealTransitionReportsFinishAfterGateClosedFirst` | Gate reports no seal; finish reports the seal. |
| Nonfinal and child gates | `TestSealTransitionReportsOnlyTheFinalMatterGate`, `TestSealTransitionReportsFalseForChildGate` | Do not report a Matter seal. |
| Wrong-scale gate | `TestCloseGateRefusesNodeAtWrongDeclaredScale` | Refuses before an event. |
| Push level `off` | `TestPostSealAlignmentReadsAtPushOffAndBackendNoneIsNonfatal` | Reads normally and creates no outbox entry. |
| Backend `none` | same test | Reports unavailable and keeps exit success. |
| Shared reference | `TestTrackerAggregateReadsSharedReferenceLifecycle` | Another live Matter keeps the aggregate active. |
| Provider mappings and failures | GitHub `TestReadState*` tests | Maps all terminal rows and returns read errors without a write. |
| Scheduler finish | `TestLoop_MatterFinishCallsAlignmentCoordinatorAfterCommit` | Uses the same coordinator after commit and creates no outbox work. |

## Commands

```text
env GOCACHE=/tmp/wip-root-step05-cache GOTMPDIR=/tmp \
  WIP_RUNTIME_DIR=/tmp/wip-root-step05-runtime \
  go test -count=1 ./internal/cli \
  -run TestRrulerReplayAlreadyDoneIssueProducesNoCloseOffer -v
```

Result: pass.

```text
env GOCACHE=/tmp/wip-root-full-cache GOTMPDIR=/tmp \
  WIP_RUNTIME_DIR=/tmp/wip-root-full-runtime go test ./...
```

Result: pass for all packages.

`gofmt -d` produced no output for the changed Go files. `git diff --check`
also passed.
