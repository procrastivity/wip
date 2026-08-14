# Tracker trace 5 — Gate failure

Status: **refused for eventual automatic state delivery by the first
provider**. The gate substrate supports the no-red rule.

A hermetic Matter declared a Matter-scale gate and ran at `narrated`. Stage
closure queued its comment, but finishing the Matter while the gate remained
open queued no completion. Closing the final gate queued the Stage comment
and completed state exactly once. The tracker therefore has no red or
premature Done state to observe.

GitHub can deliver the comment. It refuses the eventual completed state
because its Issues API does not provide the required atomic lease guard.

Executable evidence:

- `TestFinalGateCloseQueuesMatterCompletionAndLateStageCommentOnce`
- `TestCommentDeduplicatesHalfFailedReplay`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed all tests.

Findings (D65, exhaustive):

- GitHub cannot deliver the eventual green state. See
  `tracker-model-11-amendment-draft.md`.
