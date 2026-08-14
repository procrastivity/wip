# Tracker trace 7 — Two Matters, one issue

Status: **refused for automatic state delivery by the first provider**.
The reference substrate supports the shared-reference aggregate.

Two hermetic Matters bound the same reference. Any active Matter kept the
aggregate active. The terminal combinations produced these results:

- Two sealed Matters produced completed.
- One sealed and one canceled Matter produced completed.
- Two canceled Matters produced canceled.

Removing one binding recomputed while one owner remained. Removing the final
binding queued no external cleanup candidate.

The flush guard also withheld a later active regression without calling the
provider after completed state had entered the push record. The concrete
GitHub adapter independently refuses state because it has no atomic lease
guard.

Executable evidence:

- `TestSharedReferenceAggregationAndFinalUnbind`
- `TestSharedReferenceTerminalAggregation`
- `TestFlushComposesStatesKeepsCommentsAndGuardsEachReference`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed all tests.

Findings (D65, exhaustive):

- GitHub cannot deliver shared-reference state. See
  `tracker-model-11-amendment-draft.md`.
