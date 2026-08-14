# Tracker trace 8 — Tracker-side hand edit

Status: **supported** through explicit refusal and durable visibility.

The flush fixture first delivered completed state with provider lease
`rev-10`, then proposed active state for the same reference. The local
never-backward guard marked the regression `withheld` with zero attempts and
did not call the seam. For an independent reference, the seam returned
`LeaseMismatch` to model a tracker-side hand edit. Flush marked that entry
`withheld`, retained reason `lease changed`, incremented its attempt, and did
not alter the push record. No tracker result changed wip state.

The concrete GitHub adapter has no conditional state API. It therefore
returns `PermanentRefusal` before HTTP instead of claiming protection it
cannot provide. This is the required safe behavior for that provider.

Executable evidence:

- `TestFlushComposesStatesKeepsCommentsAndGuardsEachReference`
- `TestStateDeliveryRefusesWithoutCallingGitHub`

The focused run on 2026-08-14 passed both tests.

Findings (D65, exhaustive): empty. The refusal is the scenario's expected
safety result, not an unsupported claim.
