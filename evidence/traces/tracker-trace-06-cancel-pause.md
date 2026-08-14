# Tracker trace 6 — Cancel and pause mapping

Status: **refused for automatic state delivery by the first provider**.
The tracker substrate supports the provider-neutral mapping.

A hermetic fixture showed that pause and resume queue no state candidate.
Cancel is a Matter boundary. With two Matters on one reference, canceling one
left the aggregate active while its peer remained active. Canceling both
produced canceled. A sealed plus canceled terminal pair produced completed.
Provider-specific state names did not enter any payload.

The concrete GitHub adapter refuses every state candidate before HTTP because
it cannot guard the transition atomically.

Executable evidence:

- `TestDescendantCascadeAndNonqualifyingTransitionsQueueNoChildCandidate`
- `TestSharedReferenceAggregationAndFinalUnbind`
- `TestSharedReferenceTerminalAggregation`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed all tests and terminal aggregation
subtests.

Findings (D65, exhaustive):

- GitHub cannot deliver cancel or completion mappings. See
  `tracker-model-11-amendment-draft.md`.
