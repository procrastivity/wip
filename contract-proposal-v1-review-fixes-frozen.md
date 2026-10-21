# `environment.attach-once@v1` — normative contract proposal

**Status:** revision 2 incorporates the four corrections required by the first
independent High review. It is a design-only contract proposal pending a fresh
full review; it is not authorization to implement or use real signing
keys/providers.

**Basis:** read-only inspection of `/home/dev/Code/wip-wt/step11-routing-01`
and the source findings supplied with the assignment. The 87-path candidate,
the retained source-owner checkout, and immutable D115 v5 are not edited by this
draft. The checked source worktree contains pre-existing uncommitted changes;
this document does not claim those changes are part of a frozen candidate.

The key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative for
this contract proposal, subject to independent review.

## 1. Purpose and scope

`environment.attach-once@v1` consumes one domain-owner-authorized artifact to
attach one already enrolled Environment's authenticated current checkout to an
exact Repo and daemon-derived normalized route intent in the selected domain.
The route-index key may be unbound or already bound to that same Repo; it need
not have been bound before attachment. The authority, not the CLI, assigns the
resulting CloneID and WorktreeID and commits the membership/consumption/receipt
result. The local daemon then installs those authority identities against its
locally-derived Git directories in a separate local transaction. Attachment
does not bind or otherwise mutate the route index; a later explicit
`repo.route-bind@v1` uses the receipt-registered checkout proof to do that.

This is not Repo creation, Environment enrollment or rotation, Repo-route
binding/adoption, connectivity provisioning, or domain creation. An attach grant
is a new, attach-specific artifact; it MUST NOT be inferred from the journal,
`GrantStart/1`, a D115 grant, a D117 enrollment/rotation grant, a D54 profile
name, or the AuthorityStore owner-UID administration boundary.

The operation remains `AccessMutation`, `DeliveryAuthority`, with no declared
external effect. It is not deferable or queueable. D115's new-default-domain
semantics and trust scope remain unchanged. D116 adoption remains separate.
D54 remains connectivity/route-profile selection only. D117 enrollment and
rotation grants remain unchanged and valid only for their existing scopes.

## 2. Source facts and design constraints

The following are observed facts, not assumptions that attachment is already
implemented:

1. [`internal/operation/route.go:66-98`](../../../../../wip-wt/step11-routing-01/internal/operation/route.go)
   defines the catalogued `EnvironmentAttachOnceInput` with only
   `GrantArtifact`; operation metadata presently requires Repo, Clone, and
   Worktree context; the output names RepoID, DomainID, epoch, EnvironmentID,
   CloneID, and WorktreeID. The contract below makes the necessary pre-send
   context change explicit: only the authority-resolved Repo is input context;
   CloneID and WorktreeID are minted by the authority and returned in output.
2. [`docs/wipd/m6-operations.tsv:68`](../../../../../wip-wt/step11-routing-01/docs/wipd/m6-operations.tsv)
   labels the operation proposed/not-registered/not-implemented, names the
   one-use grant and route-match guards, and explicitly says `GrantStart/1` is
   unchanged and not inferable from journal identity.
   [`docs/wipd/m6-step11-acceptance.md:69-75`](../../../../../wip-wt/step11-routing-01/docs/wipd/m6-step11-acceptance.md)
   identifies the missing authority transaction, durable receipt-driven
   canonical-common-dir-to-Clone and git-dir-to-Worktree registration, and
   absence of an attach-grant schema/trust source.
3. [`internal/authoritystore/environments.go:89-174,340-369`](../../../../../wip-wt/step11-routing-01/internal/authoritystore/environments.go)
   implements D117 enrollment/rotation authorization only. It verifies against
   current domain owner key and active epoch before consumption lookup, allows
   only `environment-enroll`/`environment-rotate`, checks the CSR SPKI, nonce,
   grant lifetime, and one-use consumption. This contract does **not** inherit
   that replay order or those scopes. Environment epoch is immutable
   (`internal/authoritystore/migration.go:45-51`) and peer verification fences
   an Environment whose epoch is no longer current; §8 therefore does not
   promise cross-epoch receipt retrieval.
4. [`internal/authoritystore/d115_authority.go:261-280`](../../../../../wip-wt/step11-routing-01/internal/authoritystore/d115_authority.go)
   performs D115 command replay classification first in its serialized
   transaction, verifies the stored owner proof, compares request hash and grant
   digest, and only then proceeds to current first-use trust checks when no
   command exists. That ordering is D115-specific; §8 defines attach's own
   ordering and does not make a conflicting command ID a receipt oracle.
   `internal/wipdseed/d115_client.go:239-268` installs the D115 receipt and
   ledger disposition but does not create a local Repo row; standalone
   Environment enrollment likewise creates no Repo-bound state
   (`internal/wipdseed/environment_credential.go:42-46`). §9.2 defines the
   missing local projection from the exact D115 receipt without minting a
   RepoID or binding a route.
5. [`internal/wipd/peer_checkout_linux.go:21-48,76-119,187-253`](../../../../../wip-wt/step11-routing-01/internal/wipd/peer_checkout_linux.go)
   establishes the existing Linux pattern: derive Git identity from the
   kernel-authenticated peer's opened CWD, inspect twice, revalidate the CWD,
   and derive common-dir, git-dir/worktree name, selected remote, and normalized
   route. [`internal/wipd/server.go:45-60`](../../../../../wip-wt/step11-routing-01/internal/wipd/server.go)
   and [`internal/wipd/platform_linux.go:133-165`](../../../../../wip-wt/step11-routing-01/internal/wipd/platform_linux.go)
   show the peer-process and Linux peer-credential boundary. The connected
   D54 resolver currently accepts no caller path or remote:
   [`internal/wipdseed/route_resolve.go:31-95`](../../../../../wip-wt/step11-routing-01/internal/wipdseed/route_resolve.go).
6. [`internal/tiers/git.go:11-53`](../../../../../wip-wt/step11-routing-01/internal/tiers/git.go)
   and [`internal/tiers/init.go:22-54`](../../../../../wip-wt/step11-routing-01/internal/tiers/init.go)
   derive common-dir and per-worktree git-dir identity. The current local init
   path writes Repo/Clone/Worktree directly; it is not the proposed
   receipt-verified attach flow. [`internal/tiers/adopt.go:19-67`](../../../../../wip-wt/step11-routing-01/internal/tiers/adopt.go)
   derives and normalizes remotes for D116-style adoption and is not an attach
   grant verifier.
7. [`internal/authoritystore/route_index.go:25-50,101-175`](../../../../../wip-wt/step11-routing-01/internal/authoritystore/route_index.go)
   resolves normalized route key to Repo/domain/active epoch and enforces route
   uniqueness when binding. These operations alone do not prove a key came from
   the currently authenticated registered checkout. The new attach path MUST
   add the process-bound checkout proof described in §6.
8. [`docs/wipd/result-receipt-idempotency-contract.md:43-79,101-125,183-212,217-294`](../../../../../wip-wt/step11-routing-01/docs/wipd/result-receipt-idempotency-contract.md)
   defines authority submission, domain-lifetime command identity, exact replay,
   submitted/pending execution ownership, terminal event ranges/projections,
   signed portable receipts, conflict privacy, and unavailable/outcome-unknown
   semantics. The authority and local client store cannot share one transaction;
   §9 defines their separate commit and recovery boundary.
9. [`docs/wipd/frame-security-execution-contract.md:54-77,79-109`](../../../../../wip-wt/step11-routing-01/docs/wipd/frame-security-execution-contract.md)
   requires authenticated local peer credentials and authenticated
   Environment mTLS, and says neither a connection nor a profile grants
   semantic authority. [`docs/wipd/m6-step11-acceptance.md:58-60,86`](../../../../../wip-wt/step11-routing-01/docs/wipd/m6-step11-acceptance.md)
   records Darwin checkout discovery as fail-closed; only build/cross-compile
   evidence is available, not Darwin runtime evidence.
10. The verified immutable D115 v5 contract at
    `/home/dev/Code/wip/.amp/in/artifacts/m6-d115-create-domain-contract-01/contract-proposal-v5.md`
    (SHA-256
    `5ceb008be7aceb48639293e351ca59b32f84dee60e42125290e516bc5207dadc`, mode
    `0444`, §§2 and 10) creates a remote-less new default domain and Repo without
    a normalized route key or route-index row; it places D117 enrollment and
    attach later, then keeps route binding separate and dependent on registered
    Clone proof. The M6 Step 11 acceptance text likewise requires receipt-driven
    Clone/Worktree registration before registered-checkout route-bind proof.
    This frozen basis establishes the create-local → enroll → attach → local
    registration → route-bind order; it does not approve route-less attachment.

## 3. Contract invariants

1. Only a canonical, valid, unconsumed attach grant for the exact current
   authority/domain/epoch, owner key, Repo, daemon-derived normalized route,
   Environment ID, and Environment SPKI can authorize a first successful
   attachment. At attachment time that route key MUST be either unbound in the
   selected domain or already bound to the same Repo; a binding to another
   Repo/domain refuses. The selected domain's Repo membership already exists;
   attachment does not create it or bind the route index.
2. The authenticated local daemon derives checkout evidence. No caller-supplied
   path, RepoID, CloneID, WorktreeID, route, domain, epoch, EnvironmentID, or
   SPKI is trusted as proof. The owner grant itself contains no local filesystem
   path.
3. The authority commits attachment membership, all authority-minted resulting
   identities, the one-use grant consumption, and the exact terminal receipt
   atomically. It either commits the complete success or commits no successful
   attachment/consumption.
4. A local `Repo`/`Clone`/`Worktree` registration is made only after verifying
   the exact authority terminal receipt. If the local Repo projection is
   absent, its exact RepoID is materialized from the matching D115 creation
   receipt; attach never mints or changes an authority Repo or route binding.
   Local projection and Clone/Worktree installation are atomic and idempotently
   recoverable from the same receipt/command. No claim of cross-store
   atomicity is made.
5. A command that may have reached the authority remains outcome-unknown until
   its exact M2 terminal receipt is recovered. An M2 submitted-but-pending
   command remains owned by its original execution owner and is coalesced on
   retry. No new command ID, second CloneID/WorktreeID, refund, or alternate
   grant is created to recover it.
6. A grant can authorize at most one successful attach transaction. Exact
   command replay returns the original receipt while its Environment remains
   active in the current epoch; a different command ID using the consumed grant
   cannot repeat the effect.

## 4. Operation and local-interface contract

### 4.1 Semantic operation

The registered semantic identity is `environment.attach-once@v1`. Before it is
registered, its contract/schema MUST be corrected as follows:

```text
EnvironmentAttachOnceInput {
  grant_artifact: canonical unpadded base64url of the complete signed artifact,
  checkout_proof: canonical closed AttachCheckoutProof bytes
}

EnvironmentAttachOnceOutput {
  repo_id: authority Repo ULID,
  domain_id: authority Domain ULID,
  authority_epoch: positive uint64,
  environment_id: Environment ULID,
  clone_id: authority-minted Clone ULID,
  worktree_id: authority-minted Worktree ULID
}
```

`checkout_proof` is generated only inside the authenticated daemon path; the
local IPC request does not accept it. The semantic command context contains the
authority-resolved `RepoID`; `CloneID` and `WorktreeID` are null in the request
and become non-null only in the authority result. Therefore the operation
metadata MUST change from requiring Repo+Clone+Worktree context to requiring
Repo context only. This is a pre-registration contract correction, not a change
to an already published wire operation. The request hash covers the exact
canonical operation, context, input, proof bytes, environment, epoch, and
command ID under the repository's existing M2 command-hash rule.

The operation remains an authority mutation; it adds no journal-only or
deferable execution mode. Its result is the ordinary exact terminal receipt
carrying the typed output above. Receipt identity is `(domain_id, command_id)`.

### 4.2 Authenticated local IPC

Add one dedicated local `AttachOnce(grant_artifact)` entry point to the
authenticated client/server contract. The closed local request contains only
the operation name/version and grant artifact. It MUST reject extra fields.
It accepts no caller path, remote, normalized key, Repo/Clone/Worktree ID,
Environment identity, profile name, or proof blob. The daemon:

1. authenticates the OS peer using the existing local IPC peer boundary and
   derives/revalidates the live Git checkout from that peer as in §6;
2. obtains the current D117 Environment credential and decodes the supplied
   grant sufficiently to derive its exact digest and local attempt key;
3. looks up an existing local attempt under
   `(authenticated_environment_id, SHA-256(exact decoded grant bytes))` before
   any prospective route resolution or current grant-eligibility check. If
   found, it verifies the live checkout and pinned D115 receipt still match the
   attempt, then resumes the exact pinned M2 command; it does not rerun
   `route.resolve`, expiry, or revocation preflight. Current authenticated
   mTLS and the authority's M2 execution/replay checks remain mandatory;
4. only when no local attempt exists, validates the exact durable D115 creation
   receipt for the selected domain, then selects connectivity using existing
   D54 provisioning and resolves the daemon-derived normalized route through
   authenticated `route.resolve@v1`. The result must be an unbound key in the
   selected D54 domain/epoch or a key already bound to the exact grant
   Repo/domain/epoch; a foreign binding or any other result refuses; and
5. atomically creates the new local attempt with its validated D115 receipt,
   checkout, grant, and route evidence pinned, then submits that canonical M2
   command with its daemon-generated proof. The authority independently checks
   current eligibility inside the single M2 execution owner before terminal
   effects.

The API returns an exact terminal M2 receipt/output (success or refusal), the
ordinary M2 `submission.accepted`/pending status, a stable pre-submission
refusal/unavailable result, or `outcome-unknown` with the same command identity
retained locally. A public retry with the same grant resumes
the same attempt; it does not allocate another command ID. An internal status
lookup MAY expose only the local attempt's disposition and its exact receipt,
never grant contents or stored path material.

### 4.3 Durable local attempt identity

Before any authority bytes are sent, the daemon durably `get-or-create`s one
local attempt under `(authenticated_environment_id, SHA-256(exact decoded grant
artifact bytes))`. Before creating the row it MUST have validated the durable
D115 creation receipt for the selected domain against its durable D115 attempt,
request hash, and recorded authenticated endpoint verification, and confirmed
that its exact DomainID/RepoID agree with the grant. The row pins a locally
generated command ID, exact canonical request bytes/hash, exact grant
bytes/digest, exact D115 receipt bytes/digest and originating D115 attempt
reference, Environment ID/SPKI, authority domain/epoch, derived normalized
route, checkout-proof
bytes, and local M2 disposition. Creation and retrieval are serialized.
Simultaneous identical calls return the same attempt and command ID. A missing
or invalid D115 receipt refuses before attach submission; recovery never
recreates the domain or invents a RepoID.

If a row already exists, a repeated call MUST resume it using its exact pinned
bytes. It MUST NOT silently replace its route proof, grant bytes, Environment,
or command ID. A mismatch is an opaque local conflict and sends nothing. The
key includes the Environment identity so a grant copied to another Environment
cannot alias a local attempt; the grant's Environment binding still causes
authority refusal. The local Store MUST retain enough exact bytes and receipt
evidence across restart to resume this mapping. There is no automatic queue:
`unavailable` requires a later explicit same-intent call.

## 5. Signed grant artifact

### 5.1 Closed envelope

The proposed artifact reuses the repository's deterministic signed-artifact
convention from `internal/authoritystore/artifacts.go:27-99`; it does not reuse
the D115 grant or D117 grant schema.

```text
SignedAttachGrant {
  schema: "wipd.signed-artifact/1",
  kind: "environment-attach-grant",
  domain_id: canonical Domain ULID,
  authority_epoch: positive uint64,
  signer_role: "owner",
  signer_key_id: canonical SHA-256 DER-SPKI ID,
  key_generation: null,
  artifact_sequence: null,
  previous_artifact_digest: null,
  issued_at: canonical UTC RFC3339Nano,
  payload_schema: "wipd.environment-attach-grant/1",
  payload_digest: SHA-256(exact canonical payload bytes),
  payload: deterministic CBOR bytes,
  signature: exact Ed25519 signature
}

AttachGrantPayload {
  schema: "wipd.environment-attach-grant/1",
  grant_id: canonical ULID,
  nonce: exactly 16 random bytes,
  domain_id: canonical Domain ULID,
  authority_epoch: positive uint64,
  owner_key_id: canonical SHA-256 DER-SPKI ID,
  scope: "environment.attach-once@v1",
  audience: exactly one closed AttachAudience/1 variant from §13.3,
  repo_id: canonical Repo ULID,
  normalized_repo_key: canonical normalized remote Repo key,
  environment_id: canonical Environment ULID,
  environment_spki_digest: SHA-256 of exact DER-SPKI bytes,
  issued_at: canonical UTC RFC3339Nano,
  expires_at: canonical UTC RFC3339Nano
}

AttachAudience/1 (only this variant is valid; no extension fields):
  AuthoritySPKIAudience/1 {
    schema: "wipd.attach-audience/1",
    kind: "authority-spki",
    authority_spki_digest: "sha256:" + lowercase hex SHA-256(DER SPKI)
  }
```

Every map is closed and canonical: duplicate, omitted, unknown, noncanonical,
or wrong-type fields fail before authority admission. The payload time values
MUST be canonical UTC `Z` timestamps with no alternate spelling. The artifact
MUST have a decoded envelope no larger than 1 MiB, matching the existing
signed-artifact verifier bound; its canonical unpadded base64url operation field
MUST also be no larger than 1 MiB, matching the existing grant-artifact field
bound. Both limits are checked before authority admission. The
envelope and payload domain/epoch/time fields MUST agree. The signature
preimage is the repository's domain-separated deterministic signed-artifact
preimage over the exact wrapper with `signature` omitted. Grant digest is
SHA-256 over the exact complete decoded envelope bytes; it is not over a
normalized/re-encoded substitute.

The owner-approved audience is `AuthoritySPKIAudience/1`; the generic constant
`wipd.environment.attach-once` is invalid.
AuthorityStore command/consumption state is local, and source exposes no stable
authority-instance ID. The signed SPKI digest binds first use to the intended
TLS key, subject to the one-active-root and restore constraints in §13.3.
Direct domain-owner-root signing is the only grant issuer: `signer_key_id` and
`owner_key_id` MUST identify the current domain owner root. No attach-only
signer registry or production private-key provider is defined or assumed; this
contract specifies validation, not a production signing mechanism.

The grant intentionally does not bind CloneID/WorktreeID: those identities are
minted once by the authority and returned in the receipt. It authorizes exactly
one Repo/route and one Environment certificate identity; §6 binds the exact
current checkout to that route in the command request.

### 5.2 Validity and first-use checks

The validity interval is `[issued_at, expires_at)`, checked against the
authority's synchronized UTC clock sampled only after its writer serialization
boundary is held. No clock skew is accepted. `issued_at <= now < expires_at`,
`expires_at > issued_at`, and `expires_at - issued_at <= 10 minutes` are
required. Ten minutes follows the existing D117 grant lifetime ceiling as a
bounded limit; it does not make D117 enrollment authority attach authority.

For an absent M2 command, the authority first durably records the ordinary M2
`submitted` entry and establishes one execution owner as required by §8. That
submission alone validates framing, canonical command hash, active authenticated
Environment/domain context, and current authority epoch, then durably pins that
authenticated context; it has no attach effect, grant consumption, event range,
or terminal receipt. The single execution owner then verifies, at terminal
completion under the serialized writer boundary, the exact closed
envelope/signature under the current domain
owner root; active domain and current epoch; exact owner key ID; attach scope
and Authority-SPKI audience; Repo membership and current normalized-route
index state; the Environment ID/SPKI captured in the authenticated M2
submission; validity interval; and absence of a matching durable revocation
record under §5.3. The route key
MUST be unbound or bound to the same Repo, and the Repo MUST NOT be bound to a
different normalized key. The authenticated TLS peer identity comes from the
completed mTLS exchange, not a command field. A stale epoch or mismatched
owner, route, Repo, Environment, SPKI, scope, audience, signature, interval, or
revocation cannot commit attachment effects or consume the grant. If the grant
expires or is revoked after submission but before terminal completion, the
execution owner records the M2 terminal refusal with no attachment event,
projection, or consumption.

### 5.3 Owner-authorized individual grant revocation

The owner-approved policy requires durable individual revocation. It uses the
same current domain-owner root as attach-grant issuance; it introduces no new
trust root, signer registry, D115 operator key, D117 enrollment/rotation grant,
or store-owner-UID authorization. Define this closed signed artifact:

```text
SignedAttachGrantRevocation {
  schema: "wipd.signed-artifact/1",
  kind: "environment-attach-grant-revocation",
  domain_id: canonical Domain ULID,
  authority_epoch: positive uint64,
  signer_role: "owner",
  signer_key_id: canonical SHA-256 DER-SPKI ID,
  key_generation: null,
  artifact_sequence: null,
  previous_artifact_digest: null,
  issued_at: canonical UTC RFC3339Nano,
  payload_schema: "wipd.environment-attach-grant-revocation/1",
  payload_digest: SHA-256(exact canonical payload bytes),
  payload: deterministic CBOR bytes,
  signature: exact Ed25519 signature
}

AttachGrantRevocationPayload {
  schema: "wipd.environment-attach-grant-revocation/1",
  domain_id: canonical Domain ULID,
  authority_epoch: positive uint64,
  owner_key_id: canonical SHA-256 DER-SPKI ID,
  grant_id: canonical ULID,
  grant_digest: "sha256:" + lowercase hex SHA-256(exact complete grant bytes)
}
```

The maps are closed and canonical. The complete revocation envelope MUST be
no larger than 1 MiB, matching the existing signed-artifact verifier bound.
Envelope/payload domain and epoch MUST match. The signature MUST use the same
domain-separated artifact preimage as §5.1 and verify directly against the
current domain owner root;
`signer_key_id` and `owner_key_id` MUST equal that root's key ID. `issued_at`
MUST be no later than the authority clock sampled after acquiring the writer
serialization boundary. The revocation artifact has no expiry: it remains
importable only while its signed domain epoch is current. After an epoch
transition, the owner must issue a new revocation artifact bound to the new
current epoch if the grant still needs revocation. This is current-epoch
owner-root authorization, not a new credential or trust source.

The sole request is the complete signed artifact bytes to the new in-process
`AuthorityStore.RevokeEnvironmentAttachGrant(artifact)` method. This method is
new implementation scope; no existing revocation API is claimed. It accepts no
caller-supplied domain, epoch, owner, grant ID/digest, reason, or authorization
boolean outside the signed artifact. Local UID/process access controls, if
applied by the caller boundary, are defense in depth only; they MUST NOT
substitute for the owner-root signature. No general network service or CLI is
introduced by this contract.

The method parses the closed artifact, acquires the same serialized writer
boundary as attach, samples current time, and verifies active domain, current
epoch, current owner root/key ID, signature, and issuance time before writing.
It atomically stores a durable revocation tombstone under `(domain_id,
grant_id)` containing the exact `grant_digest`, revocation-artifact digest,
owner key ID, epoch, and commit time. It need not receive or persist the grant
artifact itself. An exact repeated artifact, or an already stored record for
the same grant ID and exact grant digest, is an idempotent no-op. A record for
the same grant ID but a different digest returns an opaque conflict and MUST
NOT be overwritten. A revocation may be recorded before the authority has seen
the grant, or after that grant was consumed.

The M2 terminal execution owner checks this tombstone in the same serialized
transaction that would commit attach success. A matching `(domain_id, grant_id,
grant_digest)` revocation committed first refuses use with no consumption or
attachment. A tombstone for the same grant ID but a different digest fails
closed as an opaque grant-ID conflict. If terminal consumption commits first,
a later revocation is durably recorded but MUST NOT undo the attachment,
consumption, IDs, or receipt. The command-replay lookup in
§8 precedes current revocation checks, so an exact replay returns the original
receipt while its Environment remains active under D117 in the current epoch.
A later epoch promotion fences that Environment and makes API receipt retrieval
unavailable under this contract, although the receipt and revocation records
remain durable. Revocation and first use use the same writer serialization
boundary; commit order is decisive.

## 6. Checkout proof, paths, and route binding

The daemon MUST derive a stable checkout snapshot from the kernel-authenticated
peer, not from request fields or process environment variables. The checkout
MUST have a daemon-selected remote that yields a normalized route key. A
checkout with no selected/normalizable remote (including a remote-less checkout)
is intentionally unsupported by this contract until a route is explicitly
selected/configured through an independently defined trusted local path; this
contract adds no route-less attach case and accepts no remote override in its
attach IPC request. On Linux it
MUST use the opened peer-CWD pattern and revalidation represented by
`internal/wipd/peer_checkout_linux.go:21-48,76-119,187-253`; it MUST fail closed
if the peer exits, CWD changes, Git identity changes between reads, Git fails,
or the selected remote cannot be normalized. It MUST derive git-common-dir,
git-dir, worktree name, selected remote, and normalized route from that snapshot.
For locator digests, require UTF-8 absolute paths from Git, apply the platform's
`filepath.Clean` without resolving symlinks, and hash those exact cleaned bytes
with the stated domain separator. Reject non-UTF-8 or non-absolute results.

For a new local attempt only, before its first submission, the daemon MUST
resolve the normalized route through the existing read-only `route.resolve`
path and check one of two valid results:
(a) the key is unbound and the resolver's selected D54 domain/epoch equals the
grant's domain/epoch; or (b) the key is bound to the exact grant Repo/domain
and current epoch. A key bound to another Repo/domain, an unresolved D54 route,
or a stale epoch refuses before mutation submission. The authority MUST
independently recheck in the attach transaction that the Repo is a member of
the grant's domain, the key is either unbound or already bound to that same
Repo, and the Repo has no different normalized key. This check is read-only:
attach MUST NOT create, update, or delete a route-index row. A previous
route-resolution response is advisory evidence only, never a substitute for
the execution owner's terminal transactional recheck. Exact resume of an
existing local M2 attempt skips this prospective preflight so a route change
cannot prevent retrieval of an already committed receipt; a pending command
still receives current route validation from its original authority execution
owner before any terminal effect.

The new closed `AttachCheckoutProof` is daemon-generated and included in the
hashed semantic request:

```text
AttachCheckoutProof {
  schema: "wipd.attach-checkout-proof/1",
  normalized_repo_key: exact normalized key from the peer snapshot,
  common_dir_digest: SHA-256("wipd/attach-common-dir/v1" || 0x00 || canonical absolute git-common-dir bytes),
  git_dir_digest: SHA-256("wipd/attach-git-dir/v1" || 0x00 || canonical absolute git-dir bytes),
  worktree_name: empty for main worktree, otherwise git-derived worktree name,
  peer_snapshot_digest: SHA-256("wipd/attach-peer-snapshot/v1" || 0x00 || canonical CBOR map of normalized_repo_key, common_dir_digest, git_dir_digest, and worktree_name),
  route_snapshot_digest: SHA-256("wipd/attach-route-snapshot/v1" || 0x00 || canonical CBOR map of selected domain_id, authority_epoch, grant-bound repo_id, and normalized_repo_key),
  environment_id: authenticated Environment ULID,
  environment_spki_digest: exact authenticated Environment leaf SPKI digest
}
```

Digests are opaque stable locators, not authentication by themselves. The
route digest binds the route intent, not a claim that the key was already
indexed. The local OS peer check and authenticated Environment mTLS bind the
daemon assertion; the grant binds route/Repo/Environment authorization, and the
authority transaction independently checks Repo membership, current route
binding state, and uniqueness without mutating the route index. Raw absolute
paths, raw remote URLs, D54 profile names, private keys, and local peer PID/UID
MUST NOT appear in the semantic command, authority receipt, or routine logs.
The authority stores only the checkout digests and authority-minted identity
mapping needed for uniqueness and replay, not host path strings.

The local request cannot supply or override these proof fields. Generic
`ExecuteCommand` requests that claim `environment.attach-once@v1` without going
through the dedicated authenticated daemon attach handler MUST be rejected.
The operation input's proof is not a cryptographic proof against a compromised
daemon or same-UID caller; it asserts only the derived checkout under the
existing local-peer trust boundary. No stronger host attestation is claimed.

Darwin remains fail-closed before authority mutation until an independently
approved authenticated checkout-discovery implementation exists. Linux
acceptance does not imply Darwin runtime support.

## 7. Authority transaction and one-use state

Attach follows the ordinary M2 submitted-to-terminal lifecycle; it does not
replace it with a D115-style one-transaction command protocol. M2 first
authenticates the current Environment/domain/epoch context, computes the
canonical command hash, classifies an existing command, and durably records
`submitted` for a new command before semantic execution. An exact retry of a
submitted nonterminal command coalesces with the same durable execution owner
and returns M2 `submission.accepted`. It does not start another handler
or grant use. Recovery after process failure resumes that submitted command
under M2's execution-owner recovery rules.

Only that execution owner performs attach grant and route/Repo semantic
validation. It samples authority time after acquiring the serialized terminal
writer boundary and rechecks all current first-use requirements in §§5.2 and 6,
including owner root, expiry, revocation, audience, active domain/epoch,
route state, Repo membership, and identity uniqueness, using the Environment
identity/SPKI captured in the durable authenticated submission rather than
requiring the original TLS connection to remain open. A grant revoked or expired
after submission but before terminal completion cannot authorize an effect.
Two distinct submitted commands racing on one grant serialize here; at most
one can consume it successfully.

On success, one terminal transaction MUST atomically:

1. mint the stable CloneID and WorktreeID;
2. append exactly one effectful `environment.checkout-attached/1` event with
   the standard M2 envelope (domain ID, command ID, request hash, Environment
   sequence, and acted time) and payload fields Repo/Environment IDs,
   normalized route key, CloneID, WorktreeID, common-dir and git-dir digests,
   worktree name, grant ID, and exact grant digest (never raw paths, remote
   URLs, nonce, or credential bytes);
3. derive the attachment, Clone/Worktree, checkout-identity, and one-use
   consumption projections from that event and its exact grant record;
4. record the consumed grant ID/digest, owner-root key ID/evidence, request
   hash, authenticated Environment identity, Repo/route, resulting IDs, and
   commit time; and
5. persist the ordinary M2 terminal logical receipt with `accepted_events` set
   to the one-event `EventRange` (`first_event_id == last_event_id`,
   `event_count == 1`), typed output, and successful terminal submission
   state, together with its standard signed `portable-receipt` wrapper and
   signer-chain evidence.

The event, all derived projections, grant consumption, IDs, logical receipt,
signed wrapper, and terminal submission state commit atomically. The exact
event bytes and receipt/wrapper bytes are retained for the domain lifetime, as
required by M2 command-ID uniqueness and exact replay. A failure before
terminal commit leaves no event/effect/consumption or terminal receipt; the durable submitted record remains pending for M2
recovery. A terminal semantic refusal instead commits the ordinary M2 refusal
receipt and terminal state with `accepted_events: null` and `output: null`, no
effectful event range, attachment projection, minted IDs, or grant consumption.
No failure may leave a partially applied effect.

Unique keys MUST serialize `(domain_id, grant_id)` and the authority checkout
identity uniqueness scope. A second command ID using a consumed grant receives
a terminal refusal and cannot create another successful result. Two identical
concurrent commands converge on one command and terminal receipt. Conflicting
payload under an existing command ID cannot disclose the first request or
receipt. The revocation table has a unique `(domain_id, grant_id)` key and
retains the exact grant digest plus signed revocation evidence. Raw private
material is never present.

No authority-side Repo is created and no route-index row is created, changed,
or deleted by attach. If the target Repo is not already a member, the route key
is bound to another Repo/domain, or the Repo is bound to a different normalized
key, terminal execution refuses. An unbound route key is valid when it resolves
through the selected D54 route to the grant's domain/epoch and the grant Repo is
already a member there. After the exact receipt has driven local Repo
projection and Clone/Worktree registration, a later explicit
`repo.route-bind@v1` may bind that key using registered-checkout proof. D116
remote adoption remains separate and cannot be performed implicitly by attach.

## 8. Replay, conflict, and validation ordering

The order follows ordinary M2 submission/replay semantics and the existing
D117 active-Environment epoch fence:

1. Authenticate the local/remote transport at the existing security boundary.
   Obtain Environment ID and current leaf SPKI from the actual mTLS session;
   require that Environment to be active for the authenticated domain and
   current authority epoch. Bind the command's Environment ID, domain, and
   epoch to that authenticated context before command lookup. Ignore asserted
   peer fields. These checks are mandatory even for receipt recovery.
2. Enforce size/framing, decode the closed canonical command, recompute the M2
   request hash, and perform only syntax/context checks needed to locate
   `(domain_id, command_id)` safely. Do not return original command fields or
   receipt data.
3. Under M2's serialized command classification, look up the domain-lifetime
   command ID before attach grant, expiry, owner-root, revocation, route, or
   checkout semantic validation:
   - A terminal command returns its retained logical receipt and signed
     `portable-receipt` wrapper only when canonical request/hash, operation,
     grant digest, and authenticated Environment ID match exactly. No semantic
     handler, effect, model write, consumption, or ID allocator runs.
   - An exact matching `submitted` nonterminal command coalesces with its
     existing execution owner and returns M2 `submission.accepted`; the local
     attempt remains pending. It does not start a second handler. A differing
     request/hash,
     operation, grant digest, or Environment receives only generic
     `command.id-conflict`/opaque authentication refusal, without stored
     hashes, identity details, receipt bytes, or output IDs.
4. If no command exists, bind the new request's `checkout_proof.environment_id`
   and `environment_spki_digest` to the actual authenticated mTLS peer and its
   active D117 credential. Persist that peer-bound canonical request and the
   server TLS SPKI used for this M2 submission in the ordinary `submitted`
   record, then establish its single execution owner before semantic
   execution. Submission creates no attachment event, projection, grant
   consumption, minted ID, or terminal receipt. This
   operation-specific admission binding is captured in the immutable command;
   it does not reauthenticate the peer for terminal execution.
5. The execution owner alone validates the current grant and current owner
   root, scope, audience, expiry, revocation tombstone, the Environment
   ID/SPKI already bound to the submitted command, current route/Repo
   membership, checkout proof, and
   authority uniqueness under the terminal writer boundary. For
   `AuthoritySPKIAudience/1`, first use compares the signed SPKI digest with
   the server TLS key captured at M2 submission and the currently configured
   serving certificate identity; a key rotation before terminal completion
   therefore refuses this still-unconsumed command. The D54 pin authenticates
   the endpoint but grants no permission. A stale grant, audience, epoch, route, or revoked
   grant receives an ordinary terminal refusal with no attach effect.

The current-epoch check precedes command lookup because D117 makes Environment
epoch immutable and fences a prior-epoch Environment after promotion.
Therefore, an exact receipt is retrievable only while its original Environment
ID can still authenticate as active in the current domain epoch. A new-epoch
Environment cannot impersonate the old ID. Once promotion fences the original
Environment, its receipt remains durable but this contract does not promise
API replay/retrieval across epochs. Any such recovery needs a separately
approved authentication or migration mechanism; it cannot weaken or change
D117 implicitly.

Within an epoch, an owner-authorized D117 credential rotation may change the
active leaf SPKI for the same Environment ID. A terminal M2 replay requires
that currently active Environment identity and the exact committed request;
it does not require the current leaf SPKI to equal the historic proof SPKI,
because no new authorization or effect occurs. First use still binds the grant
and checkout proof to the authenticated SPKI. Exact terminal replay likewise
does not recheck grant expiry/revocation or the old `AuthoritySPKIAudience/1`;
it returns the stored receipt from the same active command lineage. A pending
command remains subject to current grant, route, and revocation validation by
its original execution owner before terminal effect. Thus a route changed after
a lost success response cannot block exact receipt recovery, while a fresh
command must pass current route eligibility. An authority TLS key rotation
permits replay from the same store after D54 is updated, but unconsumed grants
require reissue for the new key. A different store without the command record
fails first-use audience validation.

This ordering is the attach-specific rule. It does not change D117's
`ValidateEnvironmentEnrollment` ordering or D115's create-domain ordering.

## 9. Local installation, no-partial-effects, and recovery

### 9.1 Commit boundaries

There are three durable boundaries, with no distributed transaction:

1. **M2 submission:** the authority persists `submitted` and its execution
   ownership before semantic execution. This record alone creates no attach
   effect, IDs, grant consumption, or terminal receipt.
2. **Authority terminal completion:** the single M2 execution owner commits
   the effectful event range/projections, Clone/Worktree IDs, grant consumption,
   logical receipt, signed `portable-receipt` wrapper, and terminal submission
   state atomically; a refusal commits a no-effect terminal receipt instead.
3. **Local client Store:** after verifying the exact signed terminal receipt
   and its event range, the installer materializes/reuses the D115 Repo
   projection, adds Clone/Worktree mappings, and retains exact receipt and M2
   recovery evidence in one local transaction.

A crash after submission but before terminal completion leaves the M2 command
pending for its existing execution owner to recover; retries coalesce and do
not start another handler. A crash after terminal completion but before local
installation leaves one authoritative result and no complete local install; it
does not refund the grant, uncommit the attachment, or create replacement IDs.
Recovery uses only the same command ID/request bytes and retries local
installation idempotently.

### 9.2 Local preflight and receipt install

Before a new attempt's first network send, the local daemon checks that any
existing local route-to-Repo mapping for the normalized key is either absent
or names the exact grant Repo, and checks for common-dir-to-Clone and
git-dir/worktree-to-Worktree collisions. Any foreign/conflicting mapping is
rejected locally before submission. An absent authority route-index binding is
not a conflict and MUST NOT be locally fabricated as an attach side effect.
The daemon MUST NOT resolve a conflict by silently merging or rewriting local
identities. The daemon's route.resolve and local grant checks are prospective
preflight for a new attempt only; they MUST NOT run again while resuming an
existing M2 command or installing its terminal receipt. The authority's
original execution owner still validates current eligibility for a pending
command under §8. After authority terminal success, installation
rechecks only local RepoID projection and checkout-to-Clone/Worktree identity
guards in its transaction. It MUST NOT recheck current authority route
eligibility: if the route is later bound to another Repo, preserve that route
binding and install the already committed receipt without changing the index.
After successful receipt-driven local Repo projection and
Clone/Worktree registration, a separate explicit route-bind operation may
consume the registered-checkout proof and mutate the authority route index.

The installer accepts only an M2 terminal receipt and signed
`portable-receipt` wrapper whose signer chain verifies under the authority's
receipt rules and whose domain, command ID, request hash, operation/version,
authenticated Environment, Repo, epoch, result, typed output, and one-event
`accepted_events` range match the pinned local attempt. The authority's
terminal transaction MUST append the exact
`environment.checkout-attached/1` effect and matching projections. The local
installer verifies that the signed `accepted_events` range has one event
(`first_event_id == last_event_id`, `event_count == 1`) and that typed output
IDs, Repo, Environment, and command hash match its pinned request and proof.
The grant digest binding is established by comparing exact pinned canonical
request bytes and the recomputed request hash; the standard terminal receipt
need not add a separate grant-digest field.

In one local Store transaction, the installer validates the exact D115
creation receipt pinned in the attempt against its durable D115 attempt and
request hash, recorded authenticated endpoint verification, and the grant's
DomainID/RepoID. If the local Repo projection is absent, it MUST create a local
`repo.attached`
projection using exactly the RepoID from that receipt; D115's Repo is
remote-less, so this projection has no remote URL or identity-remote binding.
This is a local projection of the already-created authority Repo, not a new
Repo identity, authority effect, or route binding. If the exact remote-less
RepoID projection already exists, reuse it and preserve local-only presentation
metadata. A different RepoID, conflicting existing route ownership, or
nonidentical authority identity is a local conflict; do not merge, rewrite, or
adopt it. Then install the returned CloneID and WorktreeID using the pinned Git
directory mapping and persist the exact receipt, event evidence, and terminal
local attempt state. No route-index row is created or changed. A later explicit
`repo.route-bind@v1` may consume registered-checkout proof and mutate the
authority route index.

The local Repo projection, Clone/Worktree mappings, receipt, and terminal
attempt state commit together. If any local guard or write fails, preserve the
verified authority receipt and all prior local identities, and create no
partial Repo/Clone/Worktree mapping. A missing projection is not a blocker when
the pinned D115 receipt is present: materialize it as specified above. A
conflict does not cause another attach or a new authority ID pair. Replaying
the same receipt is idempotent only when all identity and mapping fields are
byte/semantically identical; it never sends a new authority command.

If the process crashes after M2 submission but before terminal completion,
restart coalesces with or recovers the same submitted execution owner. If it
crashes after authority terminal commit but before local installation, restart
resumes the exact command, recovers the same signed receipt, and installs it.
If local installation committed but the caller response was lost, restart
finds the exact installed receipt and returns the same IDs without another
authority effect.

### 9.3 Disposition and cancellation

Use the M2 outcome vocabulary without inventing attach-specific outcomes:

| State/evidence | Required behavior | Authority/local effects |
|---|---|---|
| `prepared` / definitely unsent | Exact bytes pinned, no request bytes reached an authority handler. Caller may explicitly retry; cancellation may stop before send. | No authority receipt, membership, or grant consumption. Local pinned attempt may remain for same-intent resume. |
| `unavailable` | Positive evidence that submission did not occur (e.g. connect failure before request bytes). Return unavailable, never queue or auto-submit later. | No authority effect. A later explicit same-grant call resumes the pinned ID. |
| `submitted` / pending | M2 confirms a durable submission but no terminal receipt exists. Keep the local attempt unresolved; exact retries coalesce with the same execution owner and never launch a second handler. | Submission record only: no attach event, projection, minted IDs, grant consumption, or success receipt until terminal completion. |
| `outcome-unknown` | Any request byte may have reached authority, response is lost/malformed, or local evidence is insufficient to prove no submission. Persist exact request, retain cumulative unknown, and recover only by exact receipt query/replay. | No new ID, no grant refund, no claim of failure. |
| `terminal success` | Exact receipt verified. Install locally in its own transaction. | Authority success and consumption are immutable; local install is recoverable. |
| `terminal refused/rejected` | Exact terminal receipt if the structurally valid command crossed authority admission; return its stable refusal. | Receipt only unless the success transaction above committed. No attach/consumption on refusal. |
| local preflight/authentication/closed-schema refusal | Refuse before authority send. | No authority receipt or state change. |

Once any bytes may have been sent, cancellation only stops waiting. It cannot
delete an attempt, mark it unavailable, or authorize a new command. M2's
`receipt.not-found` under the same active authority lineage permits retry only
with the same command ID/hash; it does not manufacture unavailable. Attach is
not a deferred command and has no retry queue.

## 10. Effects and information-disclosure matrix

| Case | Result | Authority effects | Local effects |
|---|---|---|---|
| Malformed/unknown envelope or command that cannot be canonically pinned | pre-submission refusal | no M2 submission or attach effect | retain only safe diagnostic disposition |
| Structurally valid submission with bad grant signature/scope/audience/owner/Repo/route/SPKI, expired/not-yet-valid grant, or matching revocation | M2 terminal refusal | signed terminal receipt; no effectful event, attachment projection, IDs, or consumption | no install |
| Wrong authenticated domain/epoch/Environment context | authentication/context refusal before command lookup | no receipt disclosure and no new submission | no install |
| Valid current-epoch owner-root revocation commits before first use | revocation accepted | durable grant-ID/exact-digest tombstone; no attachment or consumption | no install |
| First use commits before its revocation | revocation accepted after use | existing attachment, consumption, IDs, and receipt remain unchanged | exact replay returns original receipt while the Environment remains active in the current epoch |
| Existing local collision found before first send | local refusal | none | no identity rewrite |
| Route key unbound in the selected D54 domain/epoch, with exact Repo membership | eligible for first-use authority admission | attach does not write the route index | receipt-driven local install; later explicit route-bind may bind |
| Route key bound to another Repo/domain, Repo bound to a different normalized key, or D54 selection unresolved | pre-send refusal or transactional stable refusal if raced | no successful attach or route-index mutation | no install |
| No selected/normalizable route key (including remote-less checkout) | pre-send refusal; no route-less attach behavior is defined | none | requires a separately selected/configured route before retry |
| Authority unavailable before request bytes | unavailable | none | attempt remains exact and explicit-retry-only |
| Authority durably records `submitted`, but terminal execution is pending | M2 pending/submission status | one submitted record and execution owner; no terminal attach effect yet | retain attempt; retries coalesce with the same owner |
| Request may have reached authority and reply is lost | outcome-unknown | may be fully committed; never assumed absent | retain exact bytes; no install until receipt |
| Exact committed replay after route or grant state changes, while the original Environment still authenticates in the current epoch | same exact terminal receipt | none beyond M2 read/replay | skip prospective route/grant checks; idempotent local install/recovery |
| Existing command ID with any different bytes/hash/grant | opaque command conflict | preserve original; no second receipt/result | preserve pinned original; no replacement attempt |
| Same grant ID/digest with different new command ID | consumed-grant refusal | no second successful attachment; no second consumption | no alternate IDs installed |
| Receipt verified, local commit succeeds with no preseeded Repo projection | success | already committed atomically | materialize the exact remote-less Repo projection from the pinned D115 receipt, then commit Clone/Worktree and attach receipt together locally |
| Authority receipt committed, local installer fails/crashes | recoverable local-install-pending | unchanged; no refund | exact receipt/command retained; retry local transaction only |
| Local commit succeeded, response lost | same result on local replay | unchanged | exact installed IDs/receipt returned |

Malformed and conflicting attempts MUST NOT reveal a stored receipt, original
request hash/grant digest, other Repo's owner, or identity IDs. Stable refusal
codes MAY distinguish public validation categories, but not disclose retained
command payload. No log may contain grant bytes, nonce, signature, private key,
raw checkout path, raw remote URL, environment credential, or full proof. Logs
may contain a stable generic result code and, subject to the existing privacy
policy, command ID and nonsecret domain/environment identifiers. Authority
correctness records and receipts are not observability logs.

## 11. Crash and concurrency state machine

```text
no local attempt
  └─ authenticated local call + atomic get-or-create
       └─ prepared (stable command ID, request bytes/hash, grant/proof pinned)
            ├─ proven pre-send failure → unavailable/definitely-unsent
            │    └─ explicit same-intent call → same prepared command
            └─ possible authority submission → outcome-unknown
                 ├─ M2 `submitted` durable → one execution owner / pending
                 │    ├─ restart or exact concurrent retry → same owner
                 │    ├─ terminal success → effect + consumption + signed receipt
                 │    └─ terminal refusal → no-effect signed receipt
                 ├─ terminal receipt recovered → local Repo projection +
                 │    Clone/Worktree install → installed
                 └─ still uncertain → outcome-unknown (restart preserves it)
```

For two identical local calls, the local ledger's unique get-or-create key
serializes before either network send. For concurrent authority requests with
the same command ID and exact hash, M2 command classification coalesces with
one submitted execution owner or returns the exact terminal receipt. For two
different command IDs racing on one grant, the unique grant-consumption key
permits at most one terminal success. If expiry or owner-root-signed
revocation takes effect after submission but before terminal completion, the
execution owner commits a no-effect refusal. Revocation and successful
terminal completion serialize on the same writer boundary: revocation-first
blocks use; consumption-first is not undone. After terminal success, exact
replay bypasses current route, expiry, and revocation checks but still requires
the original Environment to authenticate in the current epoch. A changed
route blocks a fresh command, not replay of a committed result. No pending
command produces an effectful event or consumes the grant before terminal
success.

## 12. Acceptance and fault-injection matrix

The complete implementation assignment MUST retain public-boundary acceptance
tests for at least these cases. Unit-only verifier checks are not sufficient.

### Grant/schema/trust

1. Accept a valid canonical attach grant signed directly by the current domain
   owner root; reject unknown/duplicate/missing fields, alternate timestamp
   spellings, bad base64url, wrong digest/signature, oversize input, mismatched
   wrapper/payload fields, a non-owner signer, or an owner-key-ID mismatch.
2. Test exact `issued_at` inclusive and `expires_at` exclusive boundaries,
   not-yet-valid, expired, reversed, and greater-than-ten-minute lifetimes
   under a controllable authority clock sampled after writer serialization.
3. Reject wrong scope, audience, DomainID, epoch, owner key ID, RepoID, route,
   EnvironmentID, Environment SPKI, grant ID/nonce shape, and a route key
   normalized differently from the daemon-derived key.
4. Reject D115 create-domain grants, D117 enrollment and rotation grants,
   `GrantStart/1` artifacts, and values inferred from D54/profile or store-owner
   UID. Verify no consumption or membership effect. Run the same owner-signed
   attach grant against an endpoint with a different TLS SPKI and verify both
   daemon preflight and authority admission reject it without consumption; the
   D54 pin authenticates endpoint identity but never independently authorizes
   attachment. On the same AuthorityStore root, rotate the TLS key: an
   unconsumed old-audience grant MUST fail first use and require reissue; exact
   replay of a command committed before rotation, sent over the newly
   authenticated endpoint after updating D54's pin, MUST return the original
   receipt/IDs without rechecking the old audience. A different-key store with
   no matching command record MUST reject the grant. Verify the host-local
   writer lease rejects a second live open of the same root. Test supported
   restore only after preserving and revalidating committed commands, receipts,
   revocations, and grant consumption: exact replay recovers the original
   result while its Environment remains active in the current epoch, and
   another command with that grant cannot succeed. Labeled negative
   controls with an independent clone or stale pre-consumption backup MUST
   document that local state cannot detect clones or rollback and that those
   states are outside the one-use guarantee. If restoration cannot establish
   preservation, keep the root out of service for attach-once.
5. Test `SignedAttachGrantRevocation` closed-field/canonical parsing, direct
   signature verification under the current domain owner root, matching active
   domain/current epoch/owner key, and `issued_at <= now`. Reject altered
   grant digest, wrong domain or owner, stale/future epoch or future issuance,
   D115/D117 authorization artifacts, and store-owner UID without a valid
   owner-root signature. A valid revocation is durable and idempotent for the
   exact `(domain_id, grant_id, grant_digest)`; a different digest for the same
   grant ID conflicts without replacing the record. Race revocation and first
   use in both serial orders: revocation-first blocks use with no consumption;
   consumption-first permits recording revocation but does not undo the
   attachment, IDs, consumption, or receipt. After consumption-first, exact
   command replay returns the original receipt while the Environment remains
   active in the current epoch; a new command ID cannot attach. Verify an
   already committed tombstone survives epoch promotion; a new revocation
   artifact requires the current epoch.

### Authenticated route and local API

6. Through actual Linux local IPC, prove that route, RepoID, paths, CloneID,
   WorktreeID, Env identity, and proof cannot be supplied/overridden by the
   caller; wrong UID, absent peer context, changed CWD, changed Git snapshot,
   malformed remote, and non-Git CWD fail before submission.
7. Verify common-dir/main worktree and linked-worktree derivation is asymmetric
   and correct; raw absolute paths and raw remote URLs never enter semantic
   command bytes, receipts, or routine logs.
8. Exercise the approved D115 new-default-domain and D117 enrollment flow
   through their actual client paths. Use the exact D115 creation receipt and
   D117 Environment credential outputs, with no preseeded local Repo
   projection. For the remote-less authority Repo with no route-index row, D54
   selects the domain/epoch and `route.resolve` reports the checkout's
   normalized key unbound. Attach succeeds while the authority route index
   remains unchanged and materializes a local remote-less Repo projection using
   exactly the RepoID from the D115 receipt; it must not mint another RepoID.
   It also installs receipt-bound Clone/Worktree mappings. A later explicit
   `repo.route-bind@v1` with registered-checkout proof binds the key.
   Also accept an already-same-Repo-bound key without an index write. Refuse a
   key bound to another Repo/domain, a Repo already bound to another normalized
   key, unresolved/stale D54 selection, and any grant/route mismatch. Race
   route state between resolution and admission: unbound→same Repo remains
   admissible, while unbound→other Repo refuses. Verify authority rechecks
   membership/key uniqueness transactionally and attach never binds the index.
   A checkout with no selected/normalizable route key, including a remote-less
   checkout, refuses before submission; assert no route-less grant, implicit
   remote choice, invented key, route-index row, or consumption.
9. Verify the dedicated local AttachOnce API is the only accepted source of
   checkout proof; generic command submission with caller-constructed proof is
   rejected.

### One-use, replay, privacy, and concurrency

10. First valid use follows M2 submission, then one execution owner completes
    terminal success with exactly one effectful event range, derived
    projections, one CloneID/WorktreeID pair, exact grant consumption, typed
    output, logical receipt, and signed `portable-receipt` wrapper in one
    terminal transaction. Inspect every persisted component; do not merely
    assert no error or imply that attach minted a RepoID.
11. Two concurrent same-command/same-bytes submissions coalesce on one M2
    execution owner and eventually return identical exact signed receipt bytes
    and one ID pair. Two different command IDs racing with the same grant yield
    at most one terminal success, event range, membership, and consumption, and
    no second ID pair.
12. Same command ID with different request bytes, route proof, grant artifact,
    or hash returns an opaque conflict with no original receipt/hash/payload/IDs
    in response or logs. A valid signed grant cannot turn command ID probing
    into receipt disclosure.
13. Commit first use, then expire the grant and commit a matching owner-root-
    signed revocation; while the original Environment remains active in the
    current epoch, exact authenticated replay returns the original receipt
    before current validity/revocation/route checks. A fresh command ID with
    the same grant does not succeed. Also commit a matching revocation before
    first use and verify terminal refusal has no event, attachment, or
    consumption; test both serialization orders as specified in case 5.
14. Promote the domain epoch after a successful command. Verify D117 fences the
    original Environment before command lookup, so the old receipt remains
    durable but is not API-retrievable under this contract. A new-epoch
    Environment cannot impersonate the old ID; old-epoch submissions are
    refused and disclose no receipt. Do not add a cross-epoch authentication
    exception or change D117. An unauthenticated/revoked Environment cannot
    query or replay a receipt.

### Local installation and failures

15. Local Repo identity mismatch plus Clone/Worktree conflicts are caught before
    submission with zero authority requests. Race a local collision after
    preflight; verify the authority result remains retained and local install
    conflicts without rewriting the D115 Repo identity or creating another
    command.
16. Inject crash before local attempt commit, after get-or-create but before
    send, after durable M2 `submitted` but before terminal execution, during
    terminal work before commit, after terminal commit but before response,
    after receipt verification before local commit, and after local commit
    before response. Restart and verify the correct disposition, same command
    and execution owner, exact signed receipt recovery, and no duplicate event,
    consumption, or IDs. Race an exact concurrent retry while submitted; it
    must coalesce and never start a second handler.
17. A definitely-unsent connection failure returns unavailable and never
    automatically queues; after restart and authority recovery, no authority
    create occurs until explicit same-intent retry. That retry uses the original
    command ID and bytes.
18. Lose the response after attach terminal commit but before local install,
    then explicitly bind the same route key to a different Repo using another
    registered checkout. The exact retry of the pinned command MUST skip
    prospective route checks and return the original signed receipt/IDs. A
    fresh command with a distinct valid grant for the original Repo/key MUST
    fail current route eligibility with no event or consumption. Install of
    the old receipt MUST complete from its signed output without a fresh route
    check and never rewrites the now-different route index.
19. With no preseeded local Repo projection, inject local Store transaction
    failure during receipt-driven Repo/Clone/Worktree installation. No partial
    local projection/mappings may remain; the exact attach receipt and D115
    receipt remain pinned, and retry materializes the exact D115 RepoID then
    installs the same Clone/Worktree IDs idempotently. If local install already
    committed, replay returns identical IDs and receipt without authority
    mutation. Separately test a conflicting local Repo identity is preserved.
20. Fault terminal execution before its transaction commits: the M2 `submitted`
    record remains recoverable, but no event, projection, membership, IDs,
    consumption, or terminal receipt survives. Fault after terminal commit but
    before response: the complete event range, projections, consumption, IDs,
    logical receipt, signed wrapper, and terminal state survive together; exact
    recovery returns them.
21. Verify logs/redaction for success, invalid grant, route conflict, command
    ID conflict, unavailable, outcome-unknown, authority refusal, and local
    install failure. Assert no bearer artifact, nonce, signature, raw path,
    remote URL, or private credential appears.
22. Darwin checkout discovery fails closed before authority IPC. Record
    build/cross-compilation separately from runtime acceptance; do not claim
    Darwin support from Linux tests.
23. Hold a command at durable M2 submission before terminal validation, then
    advance the authority clock past grant expiry or commit a matching signed
    revocation before its execution owner completes. Exact concurrent retries
    and restart must coalesce with that owner; it must commit one terminal
    refusal with no event range, projection, IDs, or consumption. In controls
    where terminal success commits first, later revocation cannot undo it and
    an exact replay (while the Environment remains active in the current epoch)
    returns the original signed receipt.

## 13. Owner-approved trust and deployment contract

The owner has resolved the design choices in this section. These are normative
contract requirements, not open alternatives; this contract adds no permission
to provision production credentials or perform implementation work.

### 13.1 Attach-grant issuer — direct domain-owner root

The existing current domain-owner root directly signs each attach-specific
grant. Authority first-use verification uses the active domain's current epoch,
owner public key, and owner key ID from AuthorityStore. The grant's envelope
and payload domain, epoch, and owner key ID MUST match those current values.
There is no attach-only signer, signer registry, or additional trust source.
This does not change D117 enrollment/rotation scope or authorize use of a D117
grant as an attach grant. The contract specifies verification and signed
artifact formats only: it defines no owner private-key location, production
provider, signer service, or issuance workflow. Real artifacts must not be
issued until a separate owner-controlled signing mechanism is selected; test
keys, if later authorized, are confined to isolated tests.

Neither D115 operator trust, D117 enrollment/rotation grants, D54
connectivity/profile configuration, nor store-owner UID administration can
substitute for the current domain-owner-root signature. The current repository
has no attach-grant verifier or production signer provider; the verifier is
new implementation scope, and no provider is claimed.

### 13.2 Individual grant revocation — required

The owner-approved policy requires individual revocation through the
owner-root-signed artifact and new in-process AuthorityStore method specified
in §5.3. The durable tombstone binds both grant ID and exact complete-artifact
digest, and is authorized by the current domain owner root for the active
domain epoch. It remains durable across later epoch changes. A newly imported
revocation must carry the current epoch; a stale artifact cannot create or
change a tombstone. The tombstone and first-use grant consumption serialize on
the same writer boundary: revocation-first prevents use, while
consumption-first cannot be undone. Revocation does not suppress exact replay
while the original Environment remains active in the current epoch. A later
epoch promotion fences that Environment under unchanged D117, so the old
receipt remains durable but is not API-retrievable under this contract.
No D115 operator trust, D117 enrollment/rotation grant, or store-owner UID
substitutes for the owner-root signature.

### 13.3 Authority-instance destination binding — Authority SPKI

Only `AuthoritySPKIAudience/1` is accepted; the generic constant audience is
not a valid attach destination. Authority command/consumption state is local
to each store, and source provides no stable `AuthorityID`:
`internal/wipdauthority.Profile` carries origin, DomainID, epoch, AuthoritySPKI
pin, and owner-root SPKI, while `internal/wipdremote.PrebindingProfile`
likewise carries origin/domain/epoch and AuthoritySPKI pin. The SPKI audience
binds first use to the intended endpoint key; a D54 pin authenticates endpoint
identity only and grants no attach permission.

The owner-approved destination audience is exactly `AuthoritySPKIAudience/1`
defined in §5.1, containing exactly
the canonical `sha256:<lowercase-hex SHA-256 of the intended authority TLS
server certificate's DER SubjectPublicKeyInfo>`. The owner signs this value
into the grant. On first use the daemon MUST compare it to the SPKI of the
authenticated current authority TLS endpoint; the AuthorityStore admission
boundary MUST also compare it to the SPKI of the server certificate configured
for that authority endpoint. Exposing the configured server SPKI to attach
admission is new implementation scope; no existing AuthorityStore API is
claimed. A D54 pin match is endpoint identity evidence only; the owner
signature and grant claims remain the authorization. An unconsumed grant
becomes invalid after authority certificate-key rotation and must be reissued.
Exact replay of an already committed command remains receipt recovery, subject
to §8's requirement that the original Environment can authenticate in the
current epoch, and does not recheck the grant against the new endpoint key.
This choice is supported by
existing source interfaces: both profile types carry the pin and `Profile`
verifies it against the actual TLS peer certificate. However, a SPKI identifies
a TLS key, not a database instance. The owner-approved one-use boundary assumes
exactly one active AuthorityStore root for the endpoint SPKI. This contract
provides no clone detection or rollback protection: independent copies that
share the TLS key, concurrent split-brain roots, and stale-backup rollback can
violate one-use and are outside the guarantee. Operators MUST NOT treat this
audience binding as detecting or preventing those states. Any supported restore
MUST preserve and revalidate all committed command, receipt, revocation, and
grant-consumption records before the store returns to service. If their
preservation cannot be established, the restored root MUST remain out of
service for attach-once.
`internal/authoritystore/store.go` documents a single host-local writer lease
at lines 93–94 and acquires `root/writer.lock` with nonblocking `flock` at lines
458–491. This prevents two processes from opening the same root concurrently;
it does not identify a copied database at another root, detect rollback, or
provide a restore generation. The owner-approved one-active-root and
restore-preservation requirements are normative operational constraints, not
properties enforced by this source. No clone detection or rollback protection
is claimed.

## 14. Compatibility and non-goals

- Do not change D115 v5, its owner-mediated create-domain lifecycle, operator
  trust bundle, or new-default-domain semantics.
- Do not broaden D117 `environment-enroll`/`environment-rotate`, change
  `ValidateEnvironmentEnrollment`, or reinterpret `GrantStart/1`.
- Do not let D54 profile selection, profile names, authority connectivity,
  route-resolution success, or store-owner UID grant attach permission.
- Do not create an authority Repo or bind/adopt a normalized route as an
  authority side effect of attach. The local Repo projection may be materialized
  only from the exact verified D115 creation receipt as specified in §9.2;
  route binding and D116 adoption remain their own operations.
- Do not accept arbitrary host paths or expose them to the authority. Do not
  infer authorization from local journal identity.
- Do not introduce new signer services, signer credentials, key persistence,
  private-key fields, key export, or production provider assumptions.
- Do not claim total atomicity across AuthorityStore and local Store; recovery
  is exact receipt/command replay followed by idempotent local installation.
- Do not queue unavailable commands or create a new command ID to resolve
  uncertain authority outcome.

## 15. Source citation index

| Contract subject | Source |
|---|---|
| Current attach operation/input/output/context | `internal/operation/route.go:66-98` |
| Operation catalogue and GrantStart invariant | `docs/wipd/m6-operations.tsv:68` |
| Missing attach matrix and required local registration | `docs/wipd/m6-step11-acceptance.md:69-75` |
| D117 verifier, immutable Environment epoch, and active-epoch peer fence | `internal/authoritystore/environments.go:89-174,340-369`; `internal/authoritystore/migration.go:45-51` |
| D115 serialized replay-first example (not inherited blindly) | `internal/authoritystore/d115_authority.go:246-310` |
| D115 client persists receipt/ledger but does not project Repo; enrollment has no Repo binding | `internal/wipdseed/d115_client.go:239-268`; `internal/wipdseed/environment_credential.go:42-46` |
| Owner-signed artifact wrapper/canonical rules | `internal/authoritystore/artifacts.go:27-168` |
| Local peer checkout discovery/revalidation | `internal/wipd/peer_checkout_linux.go:21-48,76-143,187-253` |
| Authenticated peer and route resolver interface | `internal/wipd/server.go:45-60,81-115`; `internal/wipd/platform_linux.go:133-165` |
| D54 route discovery input boundary | `internal/wipdseed/route_resolve.go:31-95`; `internal/wipdremote/prebinding_routes.go:26-53` |
| Authority endpoint identity evidence, but no stable AuthorityID | `internal/wipdauthority/profile.go:49-70,145-180,215-253,314-322`; `internal/wipdremote/prebinding_routes.go:26-48` |
| Local common-dir/git-dir and tier registration behavior | `internal/tiers/git.go:11-65`; `internal/tiers/init.go:22-54,62-99`; `internal/tiers/adopt.go:19-67` |
| Route binding/membership transaction limitations | `internal/authoritystore/route_index.go:25-50,101-175`; `internal/authoritystore/commands.go:674-712` |
| Host-local single-root writer lease; no restore generation claimed | `internal/authoritystore/store.go:93-94,458-491` |
| M2 submitted/pending, terminal event/projection and signed receipt rules | `docs/wipd/result-receipt-idempotency-contract.md:43-79,101-125,183-212,217-294` |
| Authenticated local and remote security boundary | `docs/wipd/frame-security-execution-contract.md:54-109` |
| Darwin fail-closed boundary | `docs/wipd/m6-step11-acceptance.md:58-60,86` |
