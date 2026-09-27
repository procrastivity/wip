# M4 Step 1: implementation seam and test fixture contract

Status: frozen design seam; no daemon, transport, profile activation, or
fixture store is implemented by this document.

## Opt-in profile and composition

The sole planned client invocation is the explicitly experimental command
`wip --experimental-wipd-profile <profile-file> ...`. It is opt-in; omission
continues to select the existing CLI behavior. The profile names one dedicated
private root and is not a database path or a request to activate arbitrary
store discovery. Later implementation must validate canonical path identity,
symlinks/aliases, default-store and `authority.db` collisions and fail closed
before creating, opening, or activating anything. It must never consult
`store.DBPath`, `WIP_DB_PATH`, XDG/default/current CLI store resolution, or
fall back to a client-side database opener.

The intended composition is:

```text
experimental CLI adapter -> local transport client -> one per-user wipd
                                                   -> operation.Registry
                                                   -> operation.Handler
                                                   -> daemon-owned isolated fixture store
```

Only the daemon opens the dedicated fresh fixture Environment. The client
contains no database opener or storage fallback. `internal/operation` owns the
transport-neutral `Registry`, `Handler func(context.Context, Request) Result`,
and typed `Result`; the client/daemon adapters own parsing, framing, transport,
and presentation, while handler composition supplies execution dependencies.
The handler receives semantic data, not transport or a store handle in
`Request`. Local and future authority adapters share that semantic boundary.
The existing `matter.create` command remains on its current legacy-only store
path and is not registered through this experimental client/handler path.

## Test-only asymmetric contract value

Use `matter.create@v1` as the narrow dispatcher contract fixture, with the
intentionally asymmetric input `{title: "M4 Fixture", locator: "fixture-17"}`
and output `{id: "01M4F1XT4R3E00000000000001", locator: "fixture-17",
title: "M4 Fixture"}`. The handler returns `result.succeeded` and the typed
`MatterCreateOutput`; validation must preserve the distinct title, locator,
and ID rather than relying on a symmetric echo. Negative dispatch continues to
use typed `result.rejected` plus stable `operation.*` problems. This is a
contract test only: it does not invoke `writesurface`, a database, or a
transport.

## Step 7 local fixture result mapping

For the explicitly opted-in local fixture Handler route only, Step 7 places the
bare M1 semantic `Result` in the payload of the existing M2 `response.end`
frame, using the already-defined three-field `terminal-result` map:
`code`, `output`, and `problem_code`. A successful result's `output` is the
existing deterministic-CBOR `matter-create-v1-output` mapping. For a
non-success disposition, `output` is null and `problem_code` carries the
permitted stable M1 problem code. No problem message is carried.

This is a scoped M4 payload-placement/meaning clarification. It adds no wire
version, field, feature, or CDDL production and does not change the M2
frame-security or version-compatibility contracts. This bare local result is
not a `wipd.terminal-receipt/1`, authority terminal outcome,
submission/commit acknowledgment, event-range claim, canonical-state proof, or
permission to replay or release dependents. `result.succeeded` means only that
the injected local Handler returned that M1 disposition; it does not mean
authority success. If a response is lost or the stream resets after a fixture
side effect, the client preserves uncertainty and must not synthesize
`result.failed`, `result.refused`, or a no-effect result. Production
`NewServer` remains empty and does not register the legacy `matter.create`
handler.

The fixture's local-only status vocabulary is deliberately limited to
`process_ready`, `profile_verified`, and `fixture_ready` booleans plus stable
local `status_code`. It conveys verified local process/profile/fixture
readiness only—not authority reachability, domain truth, canonical state, or
successful command admission. Later transport outcomes must separately retain
proven pre-submission no-effect versus post-commit response-loss/unknown.

## Existing M2 payload/schema mapping

The exchanged logical payloads, when implemented later, map only to already
specified M2 contracts: protocol capability negotiation in
`version-compatibility-contract.md` (`wipd.protocol` family and its existing
operation/schema/store/feature capability lists); exact operation identity
`matter.create@v1`; identity schema `wipd.command/1`; store schema
`wipd.store/1`; and the existing required `wipd.frame/1` feature from
`frame-security-execution-contract.md`. Negotiation remains owned by the
version-compatibility contract. JSON examples/vectors remain diagnostic
notation, never wire JSON. M4 adds no wire fields, feature IDs, schemas, or
protocol version.

The test handler and its future disposable fixture store are not canonical
authority state, authority events, receipts, claims, or journals. They must
not be described as or promoted to authority evidence. Normal production
`matter.create` continues to use its current legacy-only store path. M4 Step 1
does not implement a profile resolver, fixture DB, daemon/socket, peer
authentication, production framing, client activation, or concurrency lanes;
Steps 2–9 remain untouched.
