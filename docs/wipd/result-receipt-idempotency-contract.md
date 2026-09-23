# M2 Step 5: results, receipts, and idempotency

Status: normative protocol design over `wipd.command/1`, the M1 operation
contract, and M2 Steps 3–4. This contract closes Q06–Q08 and the Step 5 parts
of Q20, Q21, Q24, and Q25. It defines logical protocol messages and durable
invariants, not a wire encoding or store schema. The key words **MUST**,
**MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Outcome layers

One command attempt has exactly one of these protocol outcomes:

| Outcome | Stable evidence | Meaning |
|---|---|---|
| `terminal` | A `wipd.terminal-receipt/1` | The authority durably completed the command as succeeded, rejected, refused, or failed. |
| `admission-refused` | Stable boundary evidence; an authenticated problem when it is safe to return one | This attempt was not submitted. Malformed input, incompatible operations, failed authentication/authorization, and command-ID/hash conflict are examples. There is no receipt or model effect. |
| `unavailable` | Authenticated no-submission response or local proof that no command byte reached a handler | The command was definitely not submitted. An authority command is not queued for later execution. |
| `outcome-unknown` | Absence of conclusive no-submission or terminal evidence after bytes may have reached a handler | The caller MUST retain the same command ID/hash, block dependents, and query or retry. This is not failure, refusal, or rollback. |
| `pending-return` | A deferable command's durable local journal acceptance | The command has not yet received an authority result. Steps 6–7 own return and quarantine schemas. |

`terminal`, `admission-refused`, `unavailable`, `outcome-unknown`, and
`pending-return` are protocol states. They are not M1 `ResultCode` values. In
particular, a transport loss MUST NOT be represented as `result.failed`, and a
protocol/authentication refusal MUST NOT be represented as `result.refused`.

Within `terminal`, the exact M1 result maps as follows:

| M1 result | Receipt fields | Effects |
|---|---|---|
| `result.succeeded` | Typed deterministic output, no problem, and normally a nonempty accepted event range | Events, projections, and receipt commit atomically. Step 9 permits a null range only for an operation-version-declared deterministic no-op. |
| `result.rejected` | No output, one `operation.*`, `validation.*`, or `not-found.*` problem code, null event range | Receipt only; no model or external effect. |
| `result.refused` | No output, one `refusal.*` problem code, null event range | Receipt only; no model or external effect. |
| `result.failed` | No output, one `internal.*` problem code, null event range | Receipt only; no model or external effect. It is terminal only if the authority can durably commit this no-effect receipt. |

A closed-schema, noncanonical, hash-mismatched, unauthenticated, or unsupported
command is rejected before submission as `admission-refused`; it never enters
the M1 result mapping. A canonical, structurally valid request may be submitted
and then produce `result.rejected` through semantic evaluation. A storage or
process failure that prevents the terminal transaction from committing leaves
the durable submission pending and the caller at `outcome-unknown`; the server
MUST NOT invent a failed receipt.

## 2. Validation and the exact submission point

For every authority fold, including a command returned from a deferable
journal, processing is ordered as follows:

1. Authenticate the Step 4 local or remote peer and complete Step 3
   negotiation.
2. Enforce framing limits; decode the closed `wipd.command/1` value; validate
   deterministic CBOR, the exact advertised operation/schema pair, and static
   operation metadata.
3. Recompute `request_hash`; bind domain, active epoch, Environment, context,
   claim, and sequence to authenticated authority state; check pre-submission
   deadline/cancellation and required transfer prerequisites owned by later
   contracts.
4. Look up the domain-lifetime unique key `(domain_id, command_id)` before any
   semantic handler, guard, model write, or external effect runs.
5. If absent, atomically and durably create one immutable submission containing
   the key, request hash, canonical command bytes, identity schema, operation
   version, authenticated Environment, and state `submitted`. **The commit of
   this record is the authority submission point.**
6. Only the single execution owner established by that commit may evaluate
   semantic validation, guards, or the handler. It finishes with one terminal
   transaction as specified in §3.

No model or external effect may occur before step 5. The server emits
`submission.accepted` only after that commit. Cancellation, deadline expiry,
stream reset, caller exit, certificate expiry/revocation, and disconnect after
that point stop waiting only; they do not revoke the execution owner or remove
the submission.

The submitted record is a recovery obligation from an exchange that already
reached the authority, not a deferred authority queue. An unavailable authority
command never creates it. Recovery MUST resume or finish the one durable
submission under its original command ID/hash and MUST NOT create a second
semantic execution owner. A command with nontransactional external effects
MUST NOT be advertised until its operation-specific effect protocol can use the
same command ID as its idempotency boundary and satisfy this receipt ordering.

For `claim`, `provisional`, `capture`, and `environment` delivery, local
acceptance occurs only when the Environment has durably committed the complete
immutable command to its eligible ordered journal and made its provisional
overlay recoverable from that journal. That is the `pending-return` point; it
is not authority submission and creates no authority receipt. Step 7 owns the
exact journal transaction and ordering keys. On return, each command crosses
the authority submission point above separately. `authority` delivery has no
local pending-return acceptance: before bytes are sent the Environment may
persist attempt/outcome evidence, but that evidence never authorizes future
execution.

## 3. Terminal transaction and receipt

The terminal receipt identity is `(domain_id, command_id)`. There is no second
minted receipt ID. The authority MUST retain one receipt for the domain's
lifetime and through handoff/restore. A receipt is a closed logical map:

```text
TerminalReceipt {
  "schema": "wipd.terminal-receipt/1",
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "identity_schema": "wipd.command/1",
  "command_id": canonical command ULID,
  "request_hash": canonical sha256 text,
  "operation": {"name": semantic name, "version": positive uint16},
  "environment": {"id": canonical Environment ULID,
                  "sequence": positive uint64},
  "result": {
    "code": result.succeeded | result.rejected |
            result.refused | result.failed,
    "output": deterministic-CBOR byte string or null,
    "problem_code": stable problem code or null
  },
  "accepted_events": EventRange or null
}

EventRange {
  "first_event_id": canonical event ULID,
  "last_event_id": canonical event ULID,
  "event_count": positive uint64
}
```

`authority_epoch` is the epoch that admitted and completed the command. Every
identity and operation field MUST equal the submitted canonical command. The
result output uses that exact operation version's deterministic output schema;
it is not presentation JSON. Problem messages, stack details, endpoint data,
timestamps, transport request IDs, deadlines, and retry counts are excluded so
replay is stable. The `matter.create@v1` output map is exactly `{id, locator,
title}`; `locator` is the assigned locator. The immutable command retains the
requested locator, while later event/read schemas own repair-condition detail.

For `result.succeeded`, one authority transaction MUST:

1. append one or more contiguous authority events, each carrying the same
   domain ID, command ID, request hash, Environment sequence, and acted time;
2. update projections from exactly those events;
3. persist the typed output and inclusive event range in the receipt; and
4. mark the durable submission terminal.

No other command's event may interleave in that range. `event_count` MUST equal
the number of events from `first_event_id` through `last_event_id` in authority
fold order, and every event in that interval MUST name this command ID/hash.

**Step 9 amendment:** an exact operation version MAY statically declare one or
more deterministic no-op outcomes. Only such an outcome may commit
`result.succeeded` with its declared typed output and `accepted_events: null`.
The same terminal transaction MUST prove that it appended no event, changed no
projection, promoted no blob, and performed no external effect. This exception
cannot be inferred dynamically from an empty handler result, and it does not
alter the nonempty range required for every effectful success. It preserves
idempotent user-visible success without inventing a domain event.

For `result.rejected`, `result.refused`, and `result.failed`, one transaction
persists the receipt and marks the submission terminal while appending no event,
changing no projection, promoting no staged blob, and performing no external
effect. `accepted_events` and `output` are null. A refused or failed transaction
that cannot prove this no-effect rule cannot commit that disposition.

The logical receipt above and its command identity remain unchanged. Step 9
requires the authority to atomically produce a portable
`wipd.signed-artifact/1` wrapper around it. Live exchanges still require the
Step 4 OS-peer or TLS/mTLS channel; the wrapper provides independent durable
verification after transfer or handoff. A signature cannot replace hash
recomputation or event-range/no-effect validation.

## 4. Retry and command-ID uniqueness

Command IDs are unique for the lifetime of a domain, not merely one authority
epoch or connection. At the §2 step 4 lookup:

- **same ID, same request hash, terminal:** return the exact stored receipt and
  its event range. Do not run semantic validation, guards, the handler, model
  writes, or external effects again;
- **same ID, same request hash, submitted:** attach/coalesce the retry onto the
  existing execution owner, return `submission.accepted`, and wait or permit a
  receipt query. Never create a second execution owner;
- **same ID, different request hash, submitted or terminal:** return
  `command.id-conflict` as `admission-refused`. Preserve the original
  submission/receipt unchanged and disclose neither its payload nor its hash;
  create no receipt for the conflicting attempt; and
- **ID absent:** only the exact received command may cross the submission point.

The authority always recomputes the presented hash before this comparison. A
caller-provided hash and command ID cannot probe another payload. Changing an
operation/protocol version, authority epoch, Environment sequence, actor,
context, claim, argument, causation/correlation ID, acted time, or blob
reference changes the canonical hash and therefore cannot be retried under the
old ID.

## 5. Submission acknowledgment and receipt query

Step 4's `submission.accepted` payload is the following closed map:

```text
SubmissionAccepted {
  "schema": "wipd.submission-accepted/1",
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "command_id": canonical command ULID,
  "request_hash": canonical sha256 text
}
```

It proves only that the §2 authority submission point was crossed. It is not a
terminal outcome, receipt, event acknowledgment, or permission to run a
dependent command.

Step 4's reserved `receipt.query` request payload is:

```text
ReceiptQuery {
  "schema": "wipd.receipt-query/1",
  "domain_id": canonical domain ULID,
  "command_id": canonical command ULID,
  "request_hash": canonical sha256 text
}
```

The query runs only after Step 3 negotiation on a Step 4 authenticated exchange.
The authenticated domain MUST equal `domain_id`. It does not change, submit, or
execute a command. The response is exactly one of:

| Response kind | Payload/effect |
|---|---|
| `command.terminal` | The exact `TerminalReceipt`; terminal outcome resolved. |
| `receipt.pending` | `SubmissionAccepted`; the same ID/hash is durably submitted but not terminal. Dependents remain blocked. |
| `receipt.not-found` | `{schema: "wipd.receipt-not-found/1", domain_id, command_id, request_hash}`; no current submission or receipt exists for that ID in the authority's retained lineage. No command is created. |
| `problem` with `command.id-conflict` | The ID exists with another hash. The existing hash/receipt is not returned and no state changes. |
| `problem` with an auth/protocol/store-history code | No receipt claim is made beyond that code. |

`receipt.not-found` permits a current-epoch caller to retry the exact same
command ID/hash; it is not a terminal result and does not by itself release
dependents. After epoch promotion or `history-regressed`, absence is not proof
that an old command never executed. D129 requires unresolved old-epoch evidence
to be abandoned explicitly or replaced by a new current-epoch command; it is
never executed unchanged by the new authority.

After any command bytes may have been sent, a lost stream starts as
`outcome-unknown` even if `submission.accepted` was not observed. A found
receipt resolves it. A pending response retains it. A not-found response under
the same active epoch permits a same-ID/hash retry, which remains unresolved
until a terminal receipt arrives. Neither query nor retry may manufacture
`unavailable` retroactively.

## 6. Dependent commands and receipt barriers

A command whose causation, explicit prerequisite, journal order, claim close,
or operation guard depends on another command MUST NOT cross its applicable
local or authority submission point while the predecessor is
`outcome-unknown`, `pending-return`, or `receipt.pending`.

A predecessor's `result.succeeded` receipt permits the dependent to be freshly
revalidated against the receipt's installed event range and current authority
state; it does not guarantee the dependent will be admitted. A
`result.rejected`, `result.refused`, or `result.failed` receipt resolves
uncertainty but does not make the original dependent valid. The dependent
remains unsubmitted and enters the Step 7-owned quarantine/replace/abandon path
where applicable. A suffix is never auto-submitted merely because uncertainty
ended.

D126's normal claim release barrier is satisfied only when every command in
the active claim journal has a validated terminal receipt installed locally.
Any missing, pending, unknown, conflicting, or nonterminal entry blocks release.
Whether a non-success receipt requires quarantine repair before release is
owned by Step 7; Step 5 supplies the exact terminal evidence, not the claim
journal or close message.

D128's per-domain lane installs returned receipts and their exact event ranges
atomically before rebuilding the overlay or admitting later work. Steps 6–7
own pull/return/fold messages, contiguous-prefix acknowledgment, and snapshots;
they MUST carry these receipts without weakening the same-ID/hash rules.

## 7. Recovery, migration, and observability integration

For D129 recovery, an accepted receipt may be reconstructed only from a
complete contiguous recovered event group whose every event agrees on domain,
old authority epoch, command ID, request hash, Environment sequence, and acted
time. The operation version MUST define deterministic reconstruction of its
operation identity and output from those event types/payloads and projections.
The reconstructed receipt has the same logical fields and exact range as the
lost original. A refusal, rejection, or failure receipt has no events and
cannot be inferred from absence; it must be retained in the backup or remain
unresolved evidence.

D130's `wipd.migration-command/1` is not an M1 operation and therefore does not
pretend to be `wipd.command/1` or use this ordinary terminal-receipt schema.
Migration evidence MUST nevertheless bind each deterministic migration command
ID/hash to its exact nonempty migrated event range and obey the same no-duplicate
and reconstruction rules. Steps 6/9 own that migration-specific envelope,
transfer/bundle form, and proof; the Step 2 identity and ordering are unchanged.

D131 routine logs MAY include verified domain/epoch, Environment, command ID,
operation, protocol outcome, semantic result code, stable problem code, and
event-range IDs/count. Full request hashes remain restricted diagnostic/audit
data under Step 4. Receipt output, problem messages, command bytes, arguments,
locators, event payloads, credentials, and secrets MUST NOT be logged. Metrics
never establish submission, terminality, idempotency, or a receipt barrier.
After terminal resolution, Step 9 permits canonical command-byte compaction
only when the receipt, hash, typed output, event/reconstruction evidence, and
all recovery obligations remain durable. Nonterminal and quarantined command
bytes are retained until resolved or explicitly repaired.

## 8. Deterministic vectors

`result-receipt-idempotency-vectors.json` uses diagnostic notation
`wipd.result-receipt-vector/1`. JSON is not a wire encoding and is never hashed.
It covers accepted and rejected terminal results, malformed admission refusal,
guard refusal, same-ID replay/conflict, definitely unsent unavailability,
lost-response query recovery, and dependent blocking. Receipt output bytes use
the operation's deterministic CBOR and accepted event ranges are inclusive.

`internal/protocol/step5_vectors_test.go` strictly decodes the vectors and
checks the result mapping, receipt identities, event-range/no-effect rules,
retry execution counts, recovery behavior, and required traceability tokens.
Step 8 still owns standalone schemas, exact frame bytes, independent
client/server doubles, fuzzing, and cross-implementation agreement (Q25).

## 9. Decision traceability and closure

| Source | Step 5 closure | Later-owned integration |
|---|---|---|
| **D120 / Q06–Q07** | Immutable `wipd.command/1` crosses one durable submission point; M1 results map to one terminal receipt vocabulary. | Additional operation schemas must define deterministic output and effect idempotency before advertisement. |
| **D121** | Claim acquisition results use the same receipt and no-reexecution rules. | Step 7 owns acquire/grant and claim exclusion schemas. |
| **D122** | Each returned command gets its own submission/terminal transaction; a non-success head cannot admit its dependent suffix. | Steps 6–7 own contiguous return and quarantine repair. |
| **D123 / Q07–Q08** | Domain-lifetime command uniqueness, same-hash replay/coalescing, different-hash refusal, atomic event/receipt commit, exact range, query, and dependent blocking are fixed. | Step 6 owns blob upload and promotion messages. |
| **D126** | A validated terminal receipt for every active journal command is the normal-release evidence floor. | Step 7 defines journal enumeration, close, and stand-down. |
| **D128** | Receipt/event installation precedes overlay rebuild and later admission in the per-domain lane; reseed preserves receipts and submissions. | Step 6 defines transfer, stable snapshots, and reseed execution. |
| **D129** | Accepted receipts reconstruct only from complete agreeing event groups; old absent outcomes are never guessed or replayed. | Steps 6/9 define bundle verification, promotion, owner attestation, and history-regressed handling. |
| **D130** | Migration evidence uses the same unique command-ID/hash and exact accepted-range invariant without masquerading as an M1 receipt. | Steps 6/9 define the migration-specific envelope, transfer, and proof. |
| **D131** | Receipt/outcome logging is allowlisted and metrics are nonauthoritative. | Steps 6/9 complete provenance and privacy review. |
| **Q20–Q21** | Pre-submission cancellation has no receipt/effect; post-submission cancellation stops waiting only and may yield `outcome-unknown`. | Step 7 applies the same rule to journal/claim lifecycle messages. |
| **Q24** | Authority recomputes hash/range or declared no-effect, binds authenticated identity, and assigns events/epoch. | Step 9's separate portable signed-artifact wrapper cannot weaken recomputation. |
| **Q25** | Step 5 publishes deterministic logical vectors and validators. | Step 8 publishes independent wire/schema conformance. |

The exact Step 6 seed/pull/return/blob/read schemas and Step 7
journal/claim/stand-down schemas remain later-owned. This step defines only
their receipt, barrier, and ordering integration points. It adds no daemon,
listener, authority store, migration, current CLI behavior, tracker/WIP/outbox
mutation, event schema, production receipt persistence, or M3/M4 code.

There are no unresolved Step 5 decisions. Changes to outcome vocabulary,
submission ordering, receipt identity/schema, result mapping, event-range
binding, retry behavior, query behavior, or dependent blocking require a new
negotiated protocol/receipt contract; they are not implementation choices.
