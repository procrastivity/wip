# Tracker trace 4 — Late binding

Status: **refused for automatic state delivery by the first provider**.
The reference substrate supports the current-state-only scenario.

Hermetic fixtures moved Matters to each disposition before enabling tracker
push and binding a reference. Each bind queued exactly one current aggregate:
active for Planned and In Progress, canceled for Canceled, and completed for
sealed. The bind did not replay an earlier lifecycle boundary.

The concrete GitHub adapter refuses the resulting state before HTTP because
it cannot acquire or validate the required lease.

Executable evidence:

- `TestBindQueuesCurrentStateForEveryMatterDisposition`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed both tests and all four disposition
subtests.

Findings (D65, exhaustive):

- GitHub cannot deliver late-bound current state. See
  `tracker-model-11-amendment-draft.md`.
