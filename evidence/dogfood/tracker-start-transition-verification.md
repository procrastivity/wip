# Tracker start-transition verification

2026-08-21. Independent verification reviewed the uncommitted worktree on
branch `matter/tracker-start-transition`, based on commit `6cf3519`. The review
covered the A1-A8 decision record, the candidate projection change, focused
aggregate tests, and the provider-injected CLI tests.

## Verdict

The implementation and hermetic behavior pass verification. The rruler run
spans one derived work period. The `roles` Matter supplies the required second
real-run shape because its lifecycle crosses two derived work periods. The
timing supports `matter.started` as the activation trigger and supports
guard-read convergence after forge progress.

## Reviewed change

- `internal/store/view.go` distinguishes live reference membership from a
  deliverable disposition when every bound Matter has Planned lifecycle.
- `internal/store/project.go` skips candidate creation for the empty
  all-Planned disposition.
- `internal/store/tracker_substrate_test.go` covers membership without a
  disposition.
- `internal/writesurface/tracker_candidates_test.go` covers Planned bind,
  Planned rebind, shared activation, direct start, reopen, and rebuild.
- `internal/cli/tracker_start_transition_e2e_test.go` covers direct and cascade
  start, guarded delivery, forge-ahead convergence, and inert push levels.
- `evidence/dogfood/tracker-start-transition-decisions.md` records A1-A8 and
  the downstream Linear obligations.

The review also checked the Planned-plus-canceled aggregate. `cancel` requires
an In Progress Matter. A canceled Matter therefore has a prior
`matter.started` event, so that event activated the reference. Keeping that
shared reference active while another Matter stays Planned agrees with A4 and
T9.

## Executed checks

| Check | Result |
|---|---|
| `gofmt -d` on changed Go files | Pass. No output. |
| `gofumpt -d` on changed Go files | Pass. No output. |
| `git diff --check` | Pass. No output. |
| `go test ./internal/store ./internal/writesurface` | Pass. |
| `go test -count=1 ./internal/cli -run TestMatterStartTransition` | Pass: `ok github.com/procrastivity/wip/internal/cli`. |
| `go test ./...` outside the sandbox | Pass. Exit status 0. |
| `golangci-lint run` in the independent sandbox | Inconclusive. The tool exited 5 with `no go files to analyze`. The full Go suite is the authoritative result for this pass. |

The first full-suite attempt ran in the filesystem sandbox. Orchestrator and
scheduler tests could not read their run-lock path under `/run/user/1000`.
The unsandboxed full-suite rerun passed.

## Contract matrix

| Contract | Evidence | Result |
|---|---|---|
| A1: `matter.started` activates | Direct-start CLI test and descendant-cascade CLI test | Pass |
| A2: all-Planned bind is inert | Aggregate, bind, rebind, and CLI tests | Pass |
| A3: late binding after Planned keeps current-state behavior | Existing disposition table, adjusted only for Planned | Pass |
| A4: the first shared Matter start activates | Shared-reference candidate test | Pass |
| A5: forge progress needs no ownership switch | Converged CLI test, one read and zero writes | Pass |
| A6: state delivery uses a guard read | Injected guarded seam and existing GitHub guard matrix | Pass for the implemented GitHub provider; Linear inherits the obligation |
| A7: no backward path | Existing local-regression and GitHub terminal-state tests | Pass |
| A8: backend and level defaults | CLI tests for implicit `boundary`, explicit `off`, and no backend | Pass |

For delivered state, the end-to-end test records `tracker.state-pushed` and an
active push record. For forge-ahead state, the test records
`tracker.state-observed`, performs no write, and creates no push record.

## Real-run timeline

The read-only host WIP database contains the rruler Matter
`v001-release-framing` (`01M0FNS2WHXST4YFEMV0G0KZAW`) in repo
`01M070FS3F4F86G9GR2RSZB3HX`.

| Time UTC | Event |
|---|---|
| 2026-08-20 13:29:13.873 | Matter created |
| 2026-08-20 13:29:19.305-13:29:19.569 | Six Steps created |
| 2026-08-20 13:30:52.445 | Workplan content created |
| 2026-08-20 13:30:57.020 | Matter started, event `01M0FNW7KWJNR8SWQ3CAHMBDDZ` |
| 2026-08-20 13:31:04.746 | First Step started |
| 2026-08-20 13:37:28 | Branch push moved BDS-132 from Backlog to In Progress |
| 2026-08-20 13:40:31 | Pull request moved BDS-132 to In Review |
| 2026-08-20 13:48:49 | Merge moved BDS-132 to Done |
| 2026-08-20 13:53:28.339 | Matter finished |

The run supports A1 and A5. Planning and Step work began before the first
forge-visible transition. The guard-read rule would stand down after the forge
advanced the issue.

The event range is 24 minutes and does not cross the configured session gap.
The run is therefore one derived work period.

## Multi-session closure evidence

The read-only host WIP database and `wip session --json` identify the bound
`roles` Matter (`01KYW8ZXKNM7ZBBGEHPQ5CSS47`) in repo
`01KYTK9YMZVCHR3MJS9RKTGYRT`. The configured six-hour idle gap puts the
Matter's own lifecycle events in two derived work periods:

| Derived period | Matter events in the period |
|---|---|
| 2026-07-31 14:24:30.237-14:25:45.579 UTC | Matter created at 14:24:30.326. |
| 2026-08-07 23:06:44.525-2026-08-08 03:31:48.230 UTC | Matter started, all Step work occurred, the forge became visible, the reference was added, and the Matter finished. |

The Matter and Step timeline is:

| Time UTC | Event |
|---|---|
| 2026-07-31 14:24:30.326 | Matter created and entered Planned. |
| 2026-08-07 23:46:16.703 | Matter started. |
| 2026-08-07 23:49:09.408-23:49:09.550 | Steps 1-4 created. |
| 2026-08-07 23:49:14.289-2026-08-08 00:03:53.975 | Step 1 started and finished. |
| 2026-08-08 00:03:54.018-00:12:41.136 | Step 2 started and finished. |
| 2026-08-08 00:12:41.185-00:17:44.104 | Step 3 started and finished. |
| 2026-08-08 00:17:44.156-00:34:00.272 | Step 4 started and finished. |
| 2026-08-08 00:33:35.106 | A Researcher created Step 5. |
| 2026-08-08 02:59:06 | GitHub PR #56 was created. This is the first independently timestamped forge-visible transition in the available record. |
| 2026-08-08 02:59:14.534 | The Matter was bound to GitHub PR #56. |
| 2026-08-08 03:13:16 | GitHub PR #56 merged. |
| 2026-08-08 03:15:15.496-03:15:15.550 | Step 5 started and finished. |
| 2026-08-08 03:15:15.606 | Matter finished. |

This Matter stayed Planned across the gap between the two periods. The Matter
start marked pickup in the execution period. The first Step followed 2 minutes
and 57 seconds later. The first timestamped forge transition followed 3 hours,
12 minutes, and 49 seconds after the Matter start. Four Steps had finished by
then.

This timing supports A1: Matter start is a useful activation boundary,
while forge visibility is too late to be the local trigger.

The human actor added the reference eight seconds after GitHub created the pull
request. The run therefore did not exercise a direct push caused by
`matter.started`.
Instead, it supplies real late-binding and forge-ahead evidence for A3 and A5.
A guard read at bind would observe the forge progress and converge without a
write. The hermetic CLI test remains the proof of the direct-start delivery
path.

## Discrepancies and remaining work

- No code or behavior discrepancy remains from this verification pass.
- The `roles` timeline satisfies the multi-session evidence condition. Its
  explicit late binding does not replace the direct-start CLI proof.
- The later Linear provider must prove its workflow ordering and guard-read
  classifications. GitHub has no distinct Backlog state, so GitHub cannot
  prove the Linear Backlog-to-In-Progress row.
