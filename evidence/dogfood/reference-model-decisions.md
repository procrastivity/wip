# Tracker reference-model decisions

2026-08-10. This record closes appendix-tracker items 6–7: the delegated
backlog stub's retirement boundary and the reference shape consumed by the
tracker seam. The operative lines are ready for the next free numbers in
MODEL §12 at phase close. The local labels R1–R2 (R for reference) preserve
their order without allocating D-numbers early. Each label is one operative
decision, so the append allocates one D-number per row.

Related record: `push-model-decisions.md` (T1–T10). Its one-way authority,
outbox, late-binding, shared-reference, and configuration rules are settled
inputs, not questions reopened here.

| Local | Operative decision |
|---|---|
| R1 | **A delegated backlog entry remains a link-only stub until the provider confirms creation of the external item.** Delegation always queues one idempotent creation entry through the outbox. With no backend, with tracker push `off`, or after a failed attempt, the stub remains visible and pending; confirmation retires it from the active backlog without removing its event history. |
| R2 | **A Matter owns a set of zero or more tracker references used equally by the seam.** Stages and Steps share their Matter's set and cannot bind independently. Every reference receives the same qualifying Matter state changes and narrated Stage comments. References and Matters form a many-to-many relation: adding a reference queues its current aggregate without replay; removing one recomputes it only while other Matters remain bound and otherwise leaves the external item unchanged. |

## Rationale and consequences

### R1 — confirmation, not intent, retires the stub

Delegation is the third backlog exit beside planning and declining. D8 makes
its local result a stub because ownership of the work moves outward, while
wip still needs enough identity to show what is awaiting delivery and to
deduplicate a retry. Retiring the entry when the command merely queues the
write would erase that visible distinction before the provider has accepted
anything.

The provider's successful create response is the retirement boundary. Until
then, one stub and one idempotency key represent all attempts. A failure or
unavailable transport changes delivery state, not backlog identity, and
cannot create a second external item on retry. After confirmation, the
active backlog no longer lists the stub; the delegation, attempts,
confirmation, and retirement remain reconstructible from the event log.

Creation is an outbox operation, but it is not a Matter lifecycle or Stage
narration entry. T10's `off` / `boundary` / `narrated` levels govern those
push entries, not whether an already-approved delegation can be recorded.
Delegating while push is `off`, or before a backend is configured, therefore
queues the creation entry and retains the stub. A later eligible flush uses
that same entry; it does not replay delegation, change its creation-time
meaning, or mint a new idempotency key. Boundary and narrated projects have
the same creation and retirement behavior.

### R2 — one owning scale, many effective addresses

T2 assigns external-item lifecycle to the Matter. The Matter is therefore the
only node scale that can own effective tracker references, but ownership scale
does not constrain cardinality. A Stage closure at narrated level comments on
each item in its Matter's reference set, and a Step never pushes. Giving a
child an independently effective reference would add a second granularity
model that T2 rejected; giving its Matter more than one address does not.

Every reference is lifecycle-active. There is no primary reference and no
link-only secondary class: each receives the same started, canceled, or sealed
proposal, and each receives future narrated Stage comments. Entries still
compose, deduplicate, acquire a provider lease, and pass never-backward guards
per reference under T5–T6. A failure or drift on one reference does not block
eligible entries for another.

The effective shape is many-to-many. One Matter can collapse several external
items into one local outcome, and many Matters can still share one external
item. T9 is evaluated independently for each reference from all Matters bound
to that reference. One Matter's lifecycle therefore contributes to every
reference in its set without making their provider state shared internally.

Binding operations change set membership. `wip bind <matter> <ref>` adds one
reference and emits `reference.added`; adding an existing member refuses
without an event. `wip unbind <matter> <ref>` emits `reference.removed`;
removing a missing member also refuses without an event. The command
`wip rebind <matter> <old-ref> <new-ref>` emits one `reference.rebound` event
and atomically replaces the named member, equivalent in result to remove plus
add but never exposing an intermediate set. Stage and Step locators receive a
stable not-a-Matter validation refusal before any of these writes.

Adding or replacing keeps T7's current-state-only rule. Each new destination
receives one proposal derived from all Matters bound to it after the change;
earlier lifecycle and narration are not replayed. Removing or replacing a
source queues one recomputed proposal when other Matters remain. When none
remain, there is no truthful local aggregate, so wip sends no cleanup
transition. Removal does not mean completion or cancellation.

The existing singleton `nodes.external_ref` projection and historical
`reference.bound` events cannot be reinterpreted as additive without changing
the meaning of old event streams. The substrate therefore introduces an
event-projected Matter/reference relation and the three new events above. On
migration and rebuild, legacy `reference.bound` events on a Matter project as
singleton replacements into the relation, so the final legacy value becomes
at most one live membership. Legacy events on Stages or Steps continue to
project only into the historical column and remain inert. New commands write
only relation events; they do not rewrite the legacy column or event history.

R2 refines reference cardinality without reopening T1–T10. T2 lifecycle and
narration rules fan out to the set; T5–T6 operate independently per reference;
T7 applies when a membership is added; and T9 derives each reference's state
from its own bound-Matter set.

## Scenario audit

| # | Scenario | Required result |
|---|---|---|
| 1 | Delegation succeeds | The entered backlog item becomes one pending stub and one creation entry. Provider confirmation retires the stub from the active backlog; event history remains. |
| 2 | No backend is configured | The creation entry and stub remain pending. Configuring a backend later makes the same entry flushable; delegation is not replayed. |
| 3 | Push level is `off` | Delegation still queues because creation is not a T10 lifecycle entry. The stub remains until a later eligible flush confirms creation. |
| 4 | Creation fails, then retries | The stub stays visible. Retry reuses the idempotency key, so at most one external item can be confirmed and only that confirmation retires the stub. |
| 5 | Boundary versus narrated | Both levels create and retire delegated items identically. The levels differ only for subsequent Matter state and Stage narration entries under T2–T4 and T10. |
| 6 | A child is bound | A new Stage or Step bind is refused before the write and emits no event. A historical child reference is readable but inert. |
| 7 | One Matter has several references | Every reference receives the same qualifying Matter state proposal. At narrated level, every reference receives each future Stage comment. Entries, leases, failures, and push records stay independent per reference. |
| 8 | Two Matters share one reference | The reference follows T9's aggregate across both Matters. Either Matter can also have other references without changing this aggregate. |
| 9 | A reference is added | The destination receives its post-add aggregate under T7. No earlier state or narration is replayed. Adding the same membership again refuses without an event. |
| 10 | A reference is removed | If other Matters remain, the source receives their recomputed T9 aggregate. If none remain, the external item receives no automatic transition. Removing a missing membership refuses without an event. |
| 11 | A reference is replaced | The named old membership is removed and the new membership added atomically. The destination gets current state; the source is recomputed only when other Matters remain. No intermediate binding set is observable. |
| 12 | A sealed Matter adds or replaces a reference | Each new destination receives one completion proposal under T7, subject to its own T6 guard; earlier lifecycle and narration are not replayed. |
| 13 | A legacy store migrates or rebuilds | Replaying a Matter's legacy `reference.bound` events preserves only its final singleton as one relation membership. Child bindings remain historical and inert. New relation events survive reopen and rebuild without changing the legacy column or log. |

## Implementation contract for downstream Matters

- `tracker-substrate` implements delegation as one event-backed backlog exit,
  one idempotent outbox creation entry, a visible pending stub, and retirement
  after a recorded provider confirmation. Failed and unavailable delivery
  leaves the same stub and entry retryable.
- `tracker-substrate` introduces an event-projected many-to-many relation
  between Matters and references. `wip bind`, `wip unbind`, and `wip rebind`
  emit `reference.added`, `reference.removed`, and `reference.rebound`;
  duplicate additions, missing removals, and child locators refuse without an
  event.
- Migration and rebuild project a Matter's legacy binding history as singleton
  replacement, yielding at most its final value as one membership. Legacy child
  bindings remain historical and inert; new relation events never change the
  legacy column or log.
- Every qualifying Matter state entry and narrated Stage comment fans out to
  every current reference. Composition, leases, push records, withholding, and
  failure stay independent per reference.
- Membership changes compute affected post-change aggregates atomically. New
  destinations receive current-state candidates; removed sources receive one
  only when Matters remain. T5 composition, T6 guards, T7 no-replay, and T9
  aggregation apply per reference.
- `tracker-provider` returns an unambiguous creation confirmation before the
  substrate retires a stub. Provider-specific state names and old-item cleanup
  do not enter the provider-agnostic model.
- `tracker-traces` exercises all thirteen scenarios above against live verbs and
  proves that refused child binds and failed deliveries append no unintended
  domain events or external items.

## For the phase-close append

1. Append R1–R2 as the next free D-numbers after the ratified T decisions.
2. MODEL §5 gains the Matter-owned reference-set rule, equal fan-out,
   many-to-many aggregation, membership operations, and the
   no-aggregate/no-cleanup case from R2.
3. MODEL §4's backlog exit set gains R1's delegated-stub lifecycle and
   confirmation boundary.
4. appendix-tracker items 6–7 are closed. Item 5 remains with
   `tracker-substrate` as assigned by the Phase 3 split.
