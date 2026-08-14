# Tracker trace 1 — No-Stage Matter

Status: **refused for automatic state delivery by the first provider**.
The provider-neutral substrate supports the scenario.

A hermetic store drove a Matter with no Stages through bind, start, and
finish at `boundary`. The three candidates were current active state at bind,
active at start, and completed at finish. No child transition was necessary.
The same substrate test also establishes that `narrated` adds only Stage
closure comments. Therefore, a Matter with no Stages degrades to boundary
behavior.

The concrete GitHub adapter then received a state entry in its HTTP sandbox.
It returned `PermanentRefusal` without making an HTTP request because GitHub
Issues does not provide the required atomic lease guard.

Executable evidence:

- `TestMatterBoundariesQueueWithoutPrematureComposition`
- `TestOffSuppressesCandidatesButBoundaryAndNarratedFanOut`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed all three tests.

Findings (D65, exhaustive):

- GitHub cannot deliver the automatic state part of this scenario. See
  `tracker-model-11-amendment-draft.md`.
