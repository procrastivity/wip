# M3 Step 2: fresh authority-store substrate

`internal/authoritystore` owns its own explicitly supplied root. It never
discovers `WIP_DB_PATH`, opens `internal/store`, or imports a legacy DB. The
root layout is `authority.db` (SQLite WAL, synchronous FULL) and a retained
`writer.lock` file. `CreateEmpty` requires an absent directory, creates mode
0700 and installs SQL baseline v1, its migration marker and `user_version` in
one transaction. A failed create leaves an incomplete root for explicit
inspection; `OpenExisting` uses SQLite `mode=rw` and refuses missing, partial,
newer, altered or legacy schema rather than initializing or upgrading it.
There is no ordinary-open migration; a future version needs an explicit
backup/upgrade and recovery path. The SQL version is internal, distinct from
the protocol's `wipd.store/1` and any later projection-derivation version.

A held, nonblocking OS `flock` on the retained lock inode spans DB open,
validation, transactions and close. A competing writable open fails with
`ErrHeld`; process death releases the kernel lease and a subsequent open
validates the DB. The root must be private and its pathname must not contain
symlinks. This is host-local cooperative isolation, not a distributed lease:
callers must keep the dedicated root and lock file from being replaced or
unlinked by another process with the same filesystem authority, and cannot
place it on a filesystem without reliable `flock`/SQLite WAL semantics.

`BootstrapDomain` inserts one validated canonical ULID, Ed25519 **public**
owner root, its SHA-256 DER-SPKI key ID, positive initial/active epoch and first
Repo in one transaction. `AttachRepo` adds another Repo to an existing
history-empty domain. The v1 schema cannot yet write history, and the Step 4
write boundary must extend the admission guard once history exists. Repo ID is
the global primary key in the single database; simultaneous cross-domain
attachments cannot both commit. Owner root and initial epoch cannot be
updated or deleted through the v1 schema. There is no detach or rebootstrap.

The active epoch and append-only per-domain predecessor-fence/promotion-proof
digest rows reserve the Step 7 continuity boundary. Reopen checks the whole
contiguous chain from initial to active epoch and refuses missing or malformed
evidence. These digests are **not** verified owner attestations; Step 7 must
verify and atomically retain the exact signed proofs before activation. No
activation method exists here. Artifact-key certificates, signing keys and
Environment registries belong to Step 3; until they exist, this substrate
admits no commands and exposes no authority products. Private owner key bytes
are neither accepted nor persisted.
