# M6 Step 11 acceptance register

This register distinguishes implemented, focused evidence from remaining
acceptance. It does not waive any original Step 11 requirement or the separate
Step 10-inclusive default-suite check.

## Covered in the current candidate

- `route.resolve@v1` is a read-only local IPC query. On Linux, wipd authenticates
  the peer process, discovers the checkout through its opened live CWD, and
  derives the selected remote; callers cannot provide a path, remote, Repo, or
  Clone assertion.
- A private immutable local D54 route profile selects the connection profile.
  Standalone prebinding startup exposes only `route.resolve@v1` and does not
  open Repo-bound client state or an Environment journal.
- The selected canonical Environment credential is used for a negotiated
  Environment-mTLS M2 `route.resolve` query. The authority read checks domain
  and expected epoch and returns the authority route index snapshot; a fresh
  miss is empty and has no durable write effects.
- Focused tests cover authenticated local route IPC, D54 match rules, authority
  M2 fresh miss/hit response validation, real mTLS enrollment/query miss,
  prebinding startup without Repo state, exact D54 configuration retry and
  conflict, and route-index transaction/replay behavior. Authority command
  handling now includes explicit null-key-only `repo.adopt-remote@v1`: an
  accepted adoption preserves Repo/domain/epoch, exact command replay returns
  its retained receipt, and a new command against an already-bound Repo records
  a refusal without changing the index. The prebinding integration test also
  exercises committed enrollment with a deliberately lost response, same
  pending-key/CSR/grant recovery, exact certificate reuse without a second
  signer call, local-install failure/restart recovery, and concurrent recovery
  without requesting another certificate. Concurrent first-use calls converge
  on one identity/key/CSR/certificate and one signer invocation. Asymmetric
  pending-identity conflicts preserve the competing private key and CSR.
  Wrong-key and alternate-certificate replacement attempts fail without
  changing the canonical record; a valid but unregistered alternate certificate
  is rejected by the authority over mTLS without changing the route index.
  Wrong owner/domain/epoch profiles fail closed without issuer re-entry.
  Same-identity journal reopen preserves Environment sequence and
  journal-position order; stale epoch/Environment identities are refused
  without database mutation. A separate isolated-store test advances the
  authority's current epoch, verifies the epoch-1 Environment cannot reopen,
  and confirms the existing journal and installed client state remain byte
  unchanged. The test-only epoch change is a direct authority DB update because
  this lab exposes no epoch-promotion API. Authoritative certificate revocation
  rejects both prebinding lookup and Repo-bound Step 11 negotiation before
  journal creation; route, client state, and journal remain unchanged.
- The D115 v5 lifecycle is implemented and covered to the reviewed contract:
  owner-invoked prepare/import/explicit-submit/status/cancel over authenticated
  local IPC, isolated launcher/TLS/AuthorityStore acceptance, durable
  receipt-first recovery, cumulative outcome-unknown handling, and bundle
  durability recovery. The production owner signer remains nil and fails
  closed; no real key/provider is configured. The broader Step 11 acceptance
  does not imply attach-once or remote adoption is complete.
- Owner-approved environment attach contract v1 review-fixes is frozen at
  SHA-256 `686cf977777906f4198d65991694643c71dcef8af92c5e31afe40fc31d0d2ee7`.
  The authority-side M2 transaction validates current owner-root-signed grant
  and revocation artifacts, authenticated Environment SPKI and authority SPKI
  audience, serialized terminal-time eligibility, exact receipt replay, and
  atomic one-use consumption/event/projection/receipt effects. Focused
  `authoritystore` tests exercise attach consumption/replay, attach not
  binding routes, and refusal without partial effects. The local attempt ledger
  is owner-private, durable, serialized, and has focused reuse/conflict,
  concurrency, reopen, and stale-temp recovery tests. These are component
  evidence only: public daemon attach is not yet wired to a durable
  `wipdremote` handler, and no authenticated end-to-end attach/install
  acceptance is claimed.
- Linux pidfd liveness polling retries `EINTR`; a deterministic injected-poll
  regression test verifies the retry and preserves fail-closed handling for
  other poll errors. The authenticated BatchSweep lost-local-response case
  passed five race-enabled repetitions after this correction.
- Darwin checkout discovery remains fail-closed before authority IPC. The
  supported evidence is build/cross-compilation only; Darwin runtime validation
  has not been performed.

## Still required before Step 11 is complete

- The broader D117 journal/race and identity/certificate matrix remains
  incomplete. Same-identity journal creation/reopen and stale reopen after the
  isolated authority's current epoch transition are covered, along with
  concurrent first-use, asymmetric pending, replacement refusal, wrong
  owner/domain/epoch, revocation, and pre-journal refusal.
- Public `environment.attach-once@v1` lifecycle and durable receipt-driven
  canonical-common-dir → Clone / git-dir → Worktree registration, including
  signed portable-receipt verification, atomic local Repo projection, replay,
  conflicts, crash recovery, stale mapping, and concurrent attach. The
  authority transaction and local attempt ledger have component coverage, but
  public IPC is not wired to a real handler and no local receipt installer is
  implemented or claimed. D115 and D117 artifacts remain non-substitutable.
- Registered-checkout proof at `repo.route-bind@v1` and null-key-only
  `repo.adopt-remote@v1` through the authenticated public daemon path, including
  authority-wide key uniqueness, receipt replay, conflict refusal, and no
  partial effects. Current authority transaction tests do not establish
  registered-checkout proof at the public route-bind boundary or authenticated
  daemon integration for either operation.
- The complete connected public path and acceptance matrix for D54 precedence,
  ambiguity, foreign-domain/stale-epoch refusal, certificate/revocation/epoch
  fences, PID/CWD reuse races, journal creation/reopen ordering, and all four
  Step 11 mutations.
- A separately authorized trusted Darwin checkout-discovery mechanism and
  runtime validation remain outstanding for full cross-platform M6 parity.
- Root's pinned Step 10-inclusive `nix develop --command make test` on the prior
  candidate failed in the nested BatchSweep lost-local-response case when
  pidfd polling returned EINTR. The check now retries EINTR and focused race
  acceptance passes five repetitions; the exact full suite still needs a rerun
  on the final candidate. No Step 10 test is skipped or weakened.

Disposition: **incomplete; not ready for High review**.
