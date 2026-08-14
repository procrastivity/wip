# `tracker-traces` step-02 — tracker family fidelity audit

Scope: the complete tracker-family taxonomy in schema versions 5–7 and every
tracker-family event in the focused hermetic fixture streams. The audit also
covers each emitter and projection branch when a stream has no instance of a
type. The audit covers the concrete provider only at the provider-neutral
return boundary. Provider names and provider states must not enter the event family.

**Findings list (D65, present even when empty): empty.**

## Complete type census

The taxonomy contains eight tracker-family types. The audit found all eight
in the frozen migration slices and the matching strict payload and projection
paths.

| Type | Subject | Payload fact | Projection result |
|---|---|---|---|
| `tracker.item-created` | outbox ULID | external reference | flushes creation and retires its delegated stub |
| `tracker.state-pushed` | outbox ULID | reference, provider-neutral disposition, opaque lease | flushes state and advances the push record |
| `outbox.approved` | outbox ULID | empty | queued to approved |
| `outbox.declined` | outbox ULID | reason | terminal decline, no attempt |
| `outbox.withheld` | outbox ULID | reason and attempted flag | visible withholding and correct attempt count |
| `outbox.delivery-failed` | outbox ULID | reason | returns to queued and increments attempts |
| `outbox.retried` | outbox ULID | empty | reapproves the same identity and key |
| `outbox.flushed` | outbox ULID | empty | records successful comment delivery |

`reference.added`, `reference.removed`, and `reference.rebound` remain in the
reference family. `backlog.delegated` remains in the backlog family. The audit
included those boundary events when it checked reconstruction, but did not
misreport them as tracker-family events.

## Exhaustive fixture-instance census

The focused streams exercised every tracker-family type:

- Creation confirmation emitted `outbox.approved` then
  `tracker.item-created`. A duplicate confirmation emitted nothing.
- The retry stream emitted `outbox.approved`, `outbox.delivery-failed`,
  `outbox.retried`, `outbox.withheld`, `outbox.retried`, and
  `tracker.item-created`, in that order.
- The terminal stream emitted one approved/declined pair and one
  approved/withheld pair. Neither terminal event incremented attempts.
- The state stream emitted `tracker.state-pushed`. A later regression emitted
  nothing and left its approved entry unchanged.
- The composition stream emitted local `outbox.withheld` events for superseded
  and regressing states. It recorded provider-attempted withholding for lease
  mismatch and `tracker.state-pushed` for guarded success. It emitted
  `outbox.flushed` for each of two independent comments.
- Retryable transport, restart, malformed-success, and no-seam streams covered
  queued recovery, durable approval, permanent visibility, and a refusal with
  no event.

The named tests assert the event sequences or each resulting projection. The
focused run on 2026-08-14 passed with no skipped test.

## MODEL §10 fidelity list

### 1. Append-only

Every tracker change enters through `Store.Commit`. Projection failures and
refused lifecycle transitions roll back the event and projection together.
`TestRebuildLeavesTheLogAlone` confirms that rebuild does not alter the log.
The retry stream preserves one outbox identity and idempotency key through all
six events.

### 2. Identity-not-locator references

Every event subject is a 26-character outbox ULID. Delegation payloads carry
the outbox ULID. Tracker references are external natural keys and can be
nullable before provider confirmation, so they correctly remain strings.
No tracker payload uses a Matter, Stage, or Step locator as an entity reference.

### 3. Tier dimensions

All eight types use the durable dimension rule in `V5Taxonomy`, `V6Taxonomy`,
and `V7Taxonomy`: each type requires Repo, while Clone and Worktree are null. The
store derives the rule from the frozen taxonomy and validates it at stamp
time. No emitter selects dimensions itself.

### 4. System-driven events are first-class

Flush commits provider results with the caller's verified actor. Every event
uses the same envelope path as a human lifecycle action. `Store.Commit`
refuses an empty or malformed actor and verifies a `role:*` claim against an
open role instance.

### 5. Enough to reconstruct in-flight work

The outbox projection records state, reason, attempts, payload, reference,
and idempotency key. Rebuild and reopen tests reproduce queued, approved,
withheld, and flushed work. The push record reconstructs the last successful
provider-neutral disposition and opaque lease. A failed delivery remains
eligible for an explicit retry after restart.

### 6. Enough to narrate a sealed Matter after the fact

Reference events identify the Matter's external-reference set. Lifecycle and
gate events explain the aggregate candidate. Tracker and outbox events then
show approval, provider result, refusal reason, retry, and success without an
inbound state mutation. The event families therefore explain the push
proposal and the result at the seam.

## Mechanical commands

```text
go test -count=1 -v ./internal/store -run 'Test(Outbox|Tracker|Reference|Backlog|Rebuild).*'
go test -count=1 -v ./internal/tracker
go test -count=1 -v ./internal/writesurface -run 'Test(Outbox|Reference|MatterBoundaries|OffSuppresses|FinalGate|SharedReference|Candidate|Descendant|Rebind).*'
go test -count=1 -v ./internal/cli -run 'TestOutbox.*'
```

All commands passed on 2026-08-14. Static inspection covered
`internal/store/{taxonomy,payloads,project,schema_v5}.go`,
`internal/writesurface/{outbox,backlog,bind}.go`, and
`internal/tracker/flush.go`.
