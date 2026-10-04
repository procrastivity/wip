# M6 Step 8: cross-Matter dependencies and tracker-reference aggregates

Status: implementation complete and independently accepted on 2026-10-04.
This record seals the Step 8 authority/profile scope; it does not claim default
CLI cutover or completion of the remaining M6 steps.

## Accepted scope

The explicit `m6-step8` profile supports `dependency.add@v1`,
`dependency.remove@v1`, `reference.bind@v1`, `reference.unbind@v1`, and
`reference.rebind@v1`. All five operations are `DeliveryAuthority` with
`ClaimNone`: their Repo/domain/live-target guards and effects are committed in
the authority transaction, and none is admitted to a claim journal. They are
available only through the explicit authenticated M6 Step 8 profile; they
remain outside the global operation catalogue and ordinary CLI routing.

Dependency edges have authority-assigned identities. Endpoint liveness,
duplicate-pair checks, and cycle detection use the domain-wide Matter/Step
graph, including endpoints in different Repos. Reference membership and shared
aggregates are event-derived domain projections. Bind, unbind, and atomic
rebind snapshot the effective Repo tracker-push policy; lifecycle events update
aggregates and deterministic tracker candidates from that event-time policy.
Provider calls and credentials remain outside these transactions.

Authority schema v18 migrates the verified v17 store through an explicit,
backed-up upgrade path. Reopen and transfer validate dependency/reference
history, receipts, configuration history, aggregates, and candidates; ordinary
open fails closed on invalid or divergent projections. The negotiated Step 8
Environment path installs verified authority results and recovers exact
receipts across restart and lost replies.

## Boundaries

This acceptance does not add ordinary CLI adapters, global Catalogue entries,
claim-journal semantics, named Batch operations (Step 9), Run/Dispatch/role
scheduling (Step 10), public tracker configuration or outbox APIs (Step 17),
or provider credentials/effects (Step 18). Candidate-enabled lifecycle
refresh is verified through canonical config history and Environment
install/reopen; authenticated M5 process fixtures remain policy-off because
the Step 17 configuration command is not yet registered.

## Verification

The accepted postimage passed:

- `go test ./... -count=1 -timeout=600s`
- `go vet ./...`
- `go test -race ./internal/wipdjournal -run '^TestStep8(BoundaryCandidateLifecycleRefreshInstallAndReopen|ReferenceBindLifecycleRefreshReopenAndSharedRepo)$' -count=1 -timeout=5m`
- `go test -race ./internal/wipdauthority -run '^TestStep8ThroughAuthenticatedWipdProcess$' -count=1 -timeout=5m`

The Step 1 operation census records the final `m6-step8-profile-only`
classification and remains executable against the operation metadata.
