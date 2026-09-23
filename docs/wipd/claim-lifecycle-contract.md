# M2 Step 7: claim lifecycle protocol

Status: normative protocol design over `wipd.command/1` and M2 Steps 3–6.
This contract closes Q15–Q17 and the Step 7 portions of Q12, Q20, Q21, Q23,
Q24, and Q25. It defines logical messages and state transitions, not a wire
encoding, daemon, listener, authority store, scheduler, or production claim
implementation. The key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY**
are normative.

## 1. Identity, compatibility, and execution boundary

Every exchange completes Step 3 negotiation and Step 4 authentication first.
Every domain, authority epoch, and Environment field MUST equal the active
authenticated values. Claim lifecycle operation versions are advertised and
selected exactly like every other operation; an unsupported version fails
before submission. Unknown fields fail closed. Step 4 framing, stream limits,
flow control, deadlines, redaction, and cancellation apply unchanged.

The following lifecycle actions are immutable `wipd.command/1` operations and
therefore use Step 5's domain-lifetime command identity, exact submission
point, terminal receipt, same-ID/hash replay, different-hash refusal, and
receipt query:

| Operation | Delivery | Canonical `claim` field | Purpose |
|---|---|---|---|
| `claim.acquire@v1` | `authority` | null | Acquire one existing Matter. |
| `claim.journal-repair@v1` | `authority` | exact current claim | Authorize abandon or one replacement generation. |
| `claim.release@v1` | `authority` | exact current claim | Normal owner release after the receipt barrier. |
| `claim.stand-down@v1` | `authority` | null | Cross-Environment loss close. |

These operation names specify the protocol boundary, not production operation
registration. Their typed inputs are carried as exact canonical command bytes
in the request schemas below. A successful lifecycle operation appends its
authority lifecycle events and receipt atomically under Step 5. A terminal
non-success receipt appends no event and changes no claim, journal, Dispatch,
Batch, or projection.

An ordinary operation whose static delivery is `claim` is accepted locally
only into the durable journal in §5. It reaches authority submission only in a
Step 6 `ReturnCommand`. A local Run lock, process, heartbeat, route, Clone,
Worktree, certificate, or cached grant is never a claim or authority evidence.

## 2. One-Matter claim and acquisition

A claim excludes direct claim-delivery writes for exactly one existing Matter.
It never claims a named Batch, Run, Repo, reference aggregate, another Matter,
or the domain. The authority maintains at most one active claim and one open
claim Dispatch for a Matter. Claim epoch is a positive, monotonically
increasing fence scoped to that Matter; it is distinct from authority epoch.
Every successful acquisition receives a fresh claim ID and an epoch greater
than every prior claim epoch for that Matter. IDs and epochs are never reused.

The first record of a claim acquisition exchange is:

```text
ClaimAcquire {
  "schema": "wipd.claim-acquire/1",
  "canonical_command": exact `claim.acquire@v1` `wipd.command/1` bytes,
  "request_hash": recomputed canonical sha256 text,
  "installed": exact Environment PrefixAnchor,
  "deadline": canonical UTC RFC3339Nano text or null
}
```

The typed acquire input contains:

```text
{
  "matter_id": canonical existing Matter ULID,
  "worktree_id": canonical Worktree ULID,
  "dispatch_mode": "anonymous-matter",
  "requested_dispatch_id": canonical fresh ULID
}
```

The authenticated Environment and command context identify the Repo, Clone,
and Worktree. `dispatch_mode` has no protocol-1 alternative. The authority
resolves the Matter's one anonymous Batch. If absent, it creates that Batch and
its single Matter membership in the acquisition transaction; if present, it
reuses it. It then opens `requested_dispatch_id` for that Matter, Worktree, and
new claim. A retry of the same command cannot create another Batch, Dispatch,
or claim. A caller cannot supply a Batch ID, add another Matter to the
anonymous Batch, or turn a named/multi-Matter Batch into one claim.

Scheduling a named Batch or any aggregate remains authority-owned. It acquires
separate one-Matter claims as each member becomes eligible. A command whose
actual guard or write footprint touches another Matter or a Batch/reference/
outbox aggregate is not claim-local even if one target lies in the claimed
Matter. It MUST be classified `authority`, or refuse as
`refusal.claim-aggregate-authority-required` or
`refusal.claim-cross-boundary`; acquisition never widens the claim.

The authority validates the request and installed anchor, then crosses the
Step 5 submission point. In one successful terminal transaction it allocates
the claim ID/epoch, resolves or creates the anonymous Batch, opens the
Dispatch, appends all acquisition events, and stores the receipt. Contention,
a nonexistent/terminal Matter, a Worktree mismatch, a second open Dispatch, or
an invalid aggregate shape produces a terminal non-success receipt with no
partial claim-side effect. Contention is `result.refused` with
`refusal.claim-contended`.

## 3. Grant, event tail, manifest, and replay

After the successful acquisition receipt, the authority pins one grant product
and returns records in this fixed order:

```text
ClaimGrantStart {
  "schema": "wipd.claim-grant-start/1",
  "grant_id": canonical ULID,
  "acquire_command_id": canonical ULID,
  "acquire_request_hash": canonical sha256 text,
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "owner_environment_id": authenticated Environment ULID,
  "claim": {"id": canonical ULID, "epoch": positive uint64},
  "matter_id": canonical Matter ULID,
  "batch_id": canonical anonymous Batch ULID,
  "dispatch_id": requested Dispatch ULID,
  "receipt": exact successful `wipd.terminal-receipt/1`,
  "prefix": {"start": ClaimAcquire.installed, "end": PrefixAnchor},
  "blob_manifest_digest": canonical sha256 text
}
event.record × complete PrefixDelta
BlobManifest
ClaimGrantEnd {
  "schema": "wipd.claim-grant-end/1",
  "grant_id": same ULID,
  "verified_prefix": ClaimGrantStart.prefix.end,
  "verified_blob_manifest_digest": ClaimGrantStart.blob_manifest_digest,
  "complete": true
}
```

The delta is Step 6's complete `PrefixDelta` from the exact installed anchor
through grant `as_of`, including every concurrent event and every acquisition
event through the receipt's accepted range. The manifest is Step 6's complete
`BlobManifest` at that same `as_of`. Every blob needed for offline claim work
is `pin-before-use`; unrelated snapshot-visible entries remain `lazy`. A
prefix mismatch refuses before acquisition submission. After submission, a
transfer failure cannot undo the claim.

The authority durably binds the grant ID, start/end anchors, manifest digest,
claim, acquisition identity, and receipt. A response-lost caller uses:

```text
ClaimGrantQuery {
  "schema": "wipd.claim-grant-query/1",
  "domain_id": canonical domain ULID,
  "acquire_command_id": canonical ULID,
  "acquire_request_hash": canonical sha256 text
}
```

A matching successful receipt returns the same pinned grant product. Pending,
not-found, conflict, and non-success use the exact Step 5 query outcomes and do
not manufacture a grant. Large/retried tails and manifests use Step 6 segment,
resume, digest, and atomic-install rules. The Environment atomically installs
the receipt, complete tail, manifest, immutable claim metadata, and a new empty
claim journal before exposing the grant as held.

## 4. Hydration and readiness

Installed claim state is one of `hydrating`, `held`, `offline-ready`,
`returning`, `quarantined`, `releasing`, or `closed`. Authority acquisition
creates `hydrating`; it does not assert local readiness. The Environment may
record this closed local product only after Step 6 verification:

```text
ClaimReadiness {
  "schema": "wipd.claim-readiness/1",
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "grant_id": canonical ULID,
  "claim": {"id": canonical ULID, "epoch": positive uint64},
  "as_of": exact grant PrefixAnchor,
  "manifest_digest": exact grant manifest digest,
  "required_entry_count": uint64,
  "verified_pinned_entry_count": uint64,
  "state": hydrating | failed | offline-ready
}
```

`offline-ready` requires every `pin-before-use` entry to have complete
nonoverlapping coverage, exact byte length, verified full digest, and a durable
pin bound to this grant. Counts alone never establish readiness. Missing,
partial, failed, wrong-length, wrong-digest, evicted, or merely cached bytes
block it. Empty required closure may be ready after durable recording. Lazy
entries do not block. Hydration cancellation leaves the claim held and not
ready; verified ranges MAY remain resumable evidence.

An Environment MUST NOT locally accept or execute a claim command requiring a
pin until readiness is `offline-ready`. The authority still revalidates every
blob reference at return; readiness is advisory local state and never a grant,
receipt, or authority assertion.

## 5. Exact claim admission and durable journal

Every command whose selected operation definition has delivery `claim` MUST
carry the exact active `{id, epoch}` in the immutable command. Before local
pending-return acceptance, the Environment verifies:

- command domain and authority epoch equal the installed grant;
- authenticated Environment, context Worktree, and claim owner agree;
- claim ID and claim epoch equal the held active claim;
- every declared and resolved guard/write footprint remains inside the one
  Matter subtree and excludes aggregate effects;
- all referenced `pin-before-use` bytes are verified and durably pinned;
- the command operation/version remains negotiated; and
- no earlier journal head, unknown outcome, quarantine, release freeze, or
  closed/fenced state blocks admission.

The authority repeats all checks against current truth immediately before the
Step 5 submission point. A missing, closed, replaced, wrong-owner, wrong-ID,
wrong-claim-epoch, or wrong-authority-epoch command is `claim.fenced`; it gains
no submission or receipt. A cross-Matter or aggregate footprint that reaches
semantic guard evaluation receives a terminal `result.refused` receipt and no
model effect. A local check never weakens the authority check.

Local acceptance is one atomic durable transaction that allocates the next
Environment sequence, writes exact canonical command bytes and recomputed hash
at the next positive journal position, records its dependency on the preceding
position, rebuilds the provisional overlay, and marks `pending-return`. Only
that commit permits `submission.accepted` locally. Journal ID is a fresh ULID;
positions and Environment sequences are contiguous within a journal
generation. Entries and command identities are immutable.

At reachable command start, release, or explicit sync, Step 6's per-domain lane
returns the head before pull or new work. Each exchange is exactly one
`ReturnCommand` and one `FoldResult`. Claim ID/epoch MUST be non-null and equal
both the immutable command and current authority claim. Successful installation
of the exact receipt, complete prefix delta, and complete manifest atomically
acknowledges that position before the next position is eligible.

`result.succeeded` may continue to the independently eligible next position.
`result.rejected`, `result.refused`, or `result.failed` installs its terminal
receipt, then stops and quarantines that head and dependent suffix. An
admission refusal such as `claim.fenced` also stops and quarantines the head but
has no receipt. No suffix may return, pull may not pass the eligible head, and
no dependent command may be admitted. A capture or another journal never
allows a claim journal to skip its head.

## 6. Explicit abandon and replacement

Repair is never mutation, deletion, retry under a changed hash, or a force
advance. `claim.journal-repair@v1` names the exact claim, blocked journal ID,
head position, head command ID/hash, and one action:

```text
abandon {
  "terminal_receipt": exact non-success receipt, or null,
  "not_submitted_proof": definitely-unsent | same-epoch-receipt-not-found | null
}

replace {
  same proof fields,
  "replacement_canonical_command": new exact `wipd.command/1` bytes,
  "replacement_request_hash": recomputed digest
}
```

The proof MUST establish terminal non-success or definite current-epoch
non-submission. `receipt.pending`, `outcome-unknown`, an inaccessible authority,
history regression, an old-epoch not-found, or an asserted local flag cannot be
abandoned or replaced normally. A successful repair authority transaction
records the decision and returns its Step 5 receipt/tail before local change.

The Environment then atomically archives the entire blocked generation as
immutable quarantined evidence. `abandon` opens a fresh empty journal.
`replace` opens a fresh journal whose position 1 is the supplied new command,
with a new command ID, request hash, and Environment sequence. The replacement
is fully revalidated against the current claim; the old dependent suffix is
not copied. Any desired dependent work is reconstructed as new commands only
after replacement success. This preserves contiguous positions and sequences
without rewriting or skipping evidence.

## 7. Normal release and terminal-receipt barrier

Normal release is owner-only and has no timeout or implicit path. Failure,
process exit, disconnect, heartbeat loss, local lock loss, certificate expiry
or revocation, hydration failure, and an idle claim do not release it.

The Environment first freezes claim admission in its per-domain lane, finishes
all eligible returns, installs every result, resolves every quarantine, and
seals the active journal. The release request is:

```text
ClaimRelease {
  "schema": "wipd.claim-release/1",
  "canonical_command": exact `claim.release@v1` `wipd.command/1` bytes,
  "request_hash": recomputed canonical sha256 text,
  "barrier": JournalBarrier,
  "deadline": canonical UTC RFC3339Nano text or null
}

JournalBarrier {
  "schema": "wipd.journal-barrier/1",
  "journal_id": canonical active journal ULID,
  "claim": {"id": exact claim ULID, "epoch": exact claim epoch},
  "entry_count": uint64,
  "last_position": uint64,
  "terminal_receipt_count": uint64,
  "entries_digest": canonical sha256 text,
  "sealed": true,
  "unresolved_count": 0,
  "quarantined_count": 0
}
```

For an empty journal, `last_position` is zero. Otherwise it equals
`entry_count`, positions are exactly `1..entry_count`, and every entry has its
exact locally installed Step 5 terminal receipt. `entries_digest` is this
deterministic chain. `B0` is SHA-256 of `"wipd/journal-barrier/v1" || NUL`.
For each position in ascending order, concatenate: position as unsigned
64-bit big-endian; the 26 ASCII bytes of command ID; the raw 32 request-hash
bytes; one result byte (`00` succeeded, `01` rejected, `02` refused, `03`
failed); then `00` for a null accepted range, or `01`, the 26 ASCII bytes each
of first and last event ID, and event count as unsigned 64-bit big-endian.
Then:

```text
B_i = SHA-256(UTF8("wipd/journal-barrier-step/v1") || 00 ||
             B_(i-1) || entry_components)
```

`entries_digest` is `sha256:` plus lowercase hex of the final `B_i`. The
authority recomputes the same facts from retained receipts and claim return
acknowledgments. A missing, pending, unknown, conflicting, nonterminal,
uninstalled, quarantined, omitted, or extra entry; an unsealed journal; or a
digest/count mismatch returns `result.refused` with
`refusal.claim-release-barrier` and no close. A terminal non-success entry
satisfies terminality only after §6 repair has removed quarantine.

On success, one terminal transaction records owner Environment, claim ID/epoch,
barrier, and Dispatch close; appends the close events; invalidates the claim
epoch; and stores the release receipt. The returned complete tail and manifest
install before local state becomes `closed`. Same-ID/hash replay returns the
same receipt/close product. No subsequent command under the old epoch can be
submitted.

## 8. Cross-Environment stand-down

Stand-down is an explicit loss operation, never a force release or a substitute
for normal owner release. The authenticated acting Environment MUST differ from
the claim owner. Its request is:

```text
ClaimStandDown {
  "schema": "wipd.claim-stand-down/1",
  "canonical_command": exact `claim.stand-down@v1` `wipd.command/1` bytes,
  "request_hash": recomputed canonical sha256 text,
  "target": {
    "claim_id": exact active claim ULID,
    "claim_epoch": exact active claim epoch,
    "owner_environment_id": exact owner Environment ULID
  },
  "reason": nonempty NFC text,
  "acknowledge_unreturned_work_loss": true,
  "owner_authorization": `wipd.signed-artifact/1` carrying an exact
                         `wipd.owner-attestation/1`,
  "deadline": canonical UTC RFC3339Nano text or null
}
```

**Step 9 amendment:** the owner authorization sits outside the canonical
command, binds its ID/hash plus the exact claim, both Environments, loss, and
reason digest, and is consumed atomically on success. Missing proof is a
closed-map `protocol.malformed-message`; invalid, expired, reused, or
wrong-scope proof is pre-submission `auth.owner-attestation-invalid`. The
reason remains semantic input but its exact text is restricted audit material,
not routine log text or permanent domain truth. A missing reason or
loss-acknowledgment field is `protocol.malformed-message` before submission. A
present empty reason is `result.rejected` with
`validation.stand-down-reason`; a present false loss acknowledgment is
`result.refused` with
`refusal.claim-loss-not-acknowledged`. Same Environment, wrong owner, wrong
claim ID, or wrong claim epoch is `result.refused` with
`refusal.claim-stand-down-fenced`. Terminal cases have receipts; all cases have
no claim/model effect.

Successful stand-down explicitly permits unknown or unreturned owner journal
work to be lost from future execution. One terminal transaction records both
Environments, the reason digest, accepted loss acknowledgment, old claim/epoch,
owner-attestation nonce consumption, and Dispatch close; appends close/loss
events; invalidates the old epoch; and stores the receipt. Exact reason text is
retained only in the encrypted restricted audit sink under Step 9's bounded
policy. Stand-down never deletes the old Environment's local evidence. A later
acquisition uses a new claim ID and higher claim epoch. Any old return, release,
repair, or claim command refuses `claim.fenced` without submission.

## 9. Authority handoff and promotion fencing

Planned handoff quiescence requires no active claim and no unresolved claim
operation. A handoff bundle retains closed claim records, receipts, and any
unconsumed grant needed by the selected source epoch as Step 6 requires, but a
destination MUST NOT activate while an active claim remains.

Disaster restore/promotion starts the destination at the next authority epoch
and fences every old-epoch grant, readiness record, active claim, journal
return, release, repair, and stand-down. It does not execute unresolved old
commands. Old local evidence is preserved as `history-regressed`/loss evidence
for owner-attested abandon or reconstruction under a newly acquired current
claim with new command IDs. A restored closed claim epoch is never reopened.

## 10. Cancellation and uncertainty

Cancellation follows the owning durable boundary, not the message name:

| State when cancel/deadline wins | Required result |
|---|---|
| Acquire/release/repair/stand-down before Step 5 submission | `transport.cancelled-before-submission` or deadline equivalent; no lifecycle receipt/effect. |
| Any lifecycle operation after Step 5 submission | Waiting stops only; operation continues to one receipt. Query/retry the same ID/hash. No rollback or implicit release. |
| Claim command before local journal commit | No journal entry, overlay, authority submission, or receipt. |
| Claim command after local journal commit but before authority submission | Caller wait stops; immutable entry remains `pending-return`. |
| Return after possible/known authority submission | Entry remains unresolved; same-ID/hash query/retry resolves it before any dependent or release. |
| Grant/tail transfer after acquisition submission | Claim remains active; query/resume the same pinned grant. Partial transfer is not readiness. |
| Hydration | Claim remains held; no false `offline-ready`; verified ranges may resume. |
| Release/stand-down close-product transfer after terminal success | Old claim is already fenced; query/resume and install the close product. |

`control.cancel`, stream reset, disconnect, process exit, deadline, auth expiry,
or caller abandonment MUST NOT delete a journal entry, manufacture a receipt,
close a claim, advance a position, abandon evidence, invalidate a receipt
barrier, or mean rollback. `outcome-unknown` and `pending-return` block their
dependents exactly as Steps 4–6 require.

## 11. Stable problems, logging, and limits

| Code | Meaning/effect |
|---|---|
| `refusal.claim-contended` | Another active claim/Dispatch owns the Matter; acquisition terminal refusal, no partial effect. |
| `refusal.claim-cross-boundary`, `refusal.claim-aggregate-authority-required` | Actual footprint is not one-Matter claim-local; terminal refusal, no model effect. |
| `claim.fenced` | Authority epoch, claim ID/epoch, holder, or state is stale; no command submission. |
| `claim.not-ready` | Required local pin closure is incomplete; no local journal acceptance. |
| `claim.grant-incomplete` | Grant tail/manifest is not atomically installable; claim may already be active. |
| `journal.return-blocked`, `journal.quarantined` | Head or dependent suffix cannot advance. |
| `refusal.journal-repair-proof`, `refusal.journal-repair-conflict` | Repair lacks safe proof or changed identity; terminal refusal, no repair. |
| `refusal.claim-release-barrier` | Full sealed receipt barrier is not proven; terminal refusal, claim remains active. |
| `refusal.claim-loss-not-acknowledged`, `refusal.claim-stand-down-fenced` | Stand-down guard terminally refused; old claim remains active. |

Control records fit Step 4's negotiated frame limit. Grant/close tails,
manifests, and blobs use Step 6 segmentation. One `ReturnCommand` still carries
one command. Routine logs may contain verified domain/authority epoch,
Environment, claim ID/epoch, Matter, lifecycle operation, stable outcome, and
event-range IDs. They MUST NOT contain reason text, command/input bytes,
journal payload, blob data/digests, grant/transfer tokens, receipt output, or
full request hashes. Metrics never prove claim ownership, readiness, return,
barrier completion, or closure.

## 12. Decision and question traceability

| Source | Step 7 closure | Later-owned boundary |
|---|---|---|
| **D121 / Q15** | One-Matter exclusion; atomic anonymous-Batch reuse/create and Dispatch open; successful receipt plus pinned complete tail/manifest; local verified pin readiness; exact claim fencing. | Step 8 publishes standalone schemas and doubles; M3/M4 implement authority/local stores. |
| **D122 / Q12 / Q16** | Immutable contiguous claim journal; one-command `ReturnCommand`/`FoldResult`; stop/quarantine; proof-gated abandon or new-generation replacement; no skip. | Step 6 transfer schemas stay unchanged; Step 8 adds properties/fuzzing. |
| **D123 / Q16** | Every returned and lifecycle command uses exact Step 5 submission, ID/hash, receipt, event range, and uncertainty rules. | Production persistence remains M3/M4. |
| **D125** | Claim commands cannot bypass Environment sequence or provisional birth ordering; cursor/environment delivery remains distinct from claim ownership. | Cursor operation/event schemas remain later-owned. |
| **D126 / Q17** | Normal owner release requires sealed complete terminal receipts and resolved quarantine; cross-Environment stand-down requires exact epoch, reason, accepted loss, and Step 9 owner authorization; both fence the old epoch. | Operational UI remains later work. |
| **D128** | Per-domain lane serializes journal acceptance, return/install, repair, release, and pull; cancellation never advances state. | Scheduler implementation remains M3/M4. |
| **D129** | Planned handoff requires no active claim; promotion fences every old grant/claim/journal and never executes unresolved old work. | Bundle ceremony and recovery conformance remain Steps 8–9. |
| **D130** | Migration cannot mint, reopen, or bypass a claim; migrated history remains Step 6 prefix evidence. | Migration executor/proof remains later work. |
| **D131 / Q23** | Claim provenance/problems are explicit; routine logs omit reason, payload, hashes, tokens, and blob/journal contents. | Step 9 performs final privacy/security review. |
| **Q20–Q21** | Pre-boundary cancel has no effect; post-journal/submission cancel stops waiting only and preserves claim/journal/receipt obligations. | Step 8 exercises transport races. |
| **Q24** | Authority assigns claim ID/epoch/as-of and recomputes command, footprint, receipts, and barrier; Environment derives hydration readiness; Step 4 authenticates both actors. | Durable portable signatures remain Step 9-owned. |
| **Q25** | Deterministic diagnostic claim vectors and strict focused validators cover acquisition through fencing/cancellation. | Step 8 owns canonical wire products and independent implementations. |

`claim-lifecycle-vectors.json` uses diagnostic notation
`wipd.claim-lifecycle-vector/1`. JSON is not wire encoding, canonical command
encoding, event encoding, or hash input. The focused validator checks exact
identities, state order, contention/cross-boundary refusal, return/fold and
barrier behavior, stand-down guards, hydration, promotion fencing, and
cancellation.

This step changes no Step 6 transfer/read schema and adds no daemon, listener,
authority store, migration, fresh authority state, production claim code,
current CLI behavior, tracker/WIP/outbox mutation, or M3/M4 implementation.
Step 8 owns broad conformance; Step 9 owns final cross-contract/security review.

There are no unresolved Step 7 decisions. Changes to one-Matter exclusion,
claim/epoch assignment, grant closure, readiness, claim admission, journal
repair, receipt barriers, stand-down proof, fencing, or cancellation require a
new negotiated protocol contract; they are not implementation choices.
