# Tracker trace 3 — Matter as the smallest node

Status: **refused for automatic state delivery by the first provider**.
The flush substrate supports the composition scenario.

A no-child Matter queued active and completed state between flushes. The
substrate kept both durable candidates until the flush boundary. Flush sent
only the newest completed state, marked the older state `withheld` with zero
attempts, and preserved independent comments. This is T5 composition at the
boundary rather than an early destructive collapse.

The concrete GitHub adapter refuses the composed state before HTTP because it
cannot enforce the atomic lease guard.

Executable evidence:

- `TestMatterBoundariesQueueWithoutPrematureComposition`
- `TestFlushComposesStatesKeepsCommentsAndGuardsEachReference`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed all three tests.

Findings (D65, exhaustive):

- GitHub cannot deliver the composed state. See
  `tracker-model-11-amendment-draft.md`.
