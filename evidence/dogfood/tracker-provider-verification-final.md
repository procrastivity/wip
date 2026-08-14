# Tracker provider final verification

Date: 2026-08-14

Matter: `tracker-provider`

Verdict: **Negative**

The complete fresh pass confirms both Builder corrections. One temporary
GitHub failure can still become a permanent refusal. This unresolved
discrepancy prevents closure of `verified` under V1.

## Reviewed revision

The reviewed base revision is
`73a9a3e677c8cfc96d6f580dba9241255cf30b1a`. The provider change is an
uncommitted working-tree change on branch `matter/tracker-provider-1`.

The following SHA-256 values identify every reviewed changed implementation
file:

| File | SHA-256 |
|---|---|
| `internal/cli/outbox_e2e_test.go` | `bbc192f9d794f7e73e129079e6279b545fb2e2d9f96f2d320bc1f9115f147aae` |
| `internal/cli/root.go` | `2b404ca49618fed4519d2c193344c8050b90cc130b27f9034fd0cd0cfd6ce086` |
| `internal/verbs/outbox/outbox.go` | `b69ca5c682a75c35b0650ce290095f91240a57f0260623215d063e7e77d7104c` |
| `internal/tracker/registry.go` | `ee363424775fcef533c73be5919debe1330e7db2005937715d42b10f8d5b23fb` |
| `internal/tracker/registry_test.go` | `52851e33aa1e42f4be2751656c5c1bb8f34c89ab352f9e6e104893958113a28f` |
| `internal/tracker/github/github.go` | `d6042726bc24dabdf6336c3bddd60be83bd054328b0a802635a867806ec00dc5` |
| `internal/tracker/github/github_test.go` | `9d9c3cf933558c01f8e9c63d81b80dffdbc94333eea68d19e166c33d24f57340` |

## Review scope

The review included the complete Matter contract, Workplan, roadmap, findings,
and the complete current implementation change. It also included both prior
negative artifacts and both Builder correction findings. The pass followed
V1-V5 in `evidence/dogfood/verification-model-decisions.md`.

## Contract results

| Criterion | Observed result |
|---|---|
| Select and verify the first provider before implementation. | Pass. The Matter selects GitHub Issues and the versioned REST transport. GitHub lists `2026-03-10` as a supported API version. |
| Add a sibling provider package and register it behind the seam. | Pass. `internal/tracker/github` implements the seam. The CLI registers `github` through `tracker.Registry`. |
| Keep provider names out of domain events and projections. | Pass. The provider name occurs in static registration and Repo configuration. The event and projection vocabulary stays provider-neutral. |
| Map creation, comment, and state operations. | Pass with the recorded state limit. Creation and comments use the GitHub REST API. State delivery refuses because GitHub does not provide the required atomic lease guard. |
| Make half-failed creation and comment replay idempotent. | Pass. Focused tests deliver each entry twice and observe one POST. The second delivery finds the SHA-256 marker. |
| Keep the end-to-end suite hermetic without credentials. | Pass. Tests inject an in-memory HTTP transport. The complete suite passes with fresh cache and runtime directories. |
| Verify delegate, push, and rebind behavior in a sandbox. | Pass. A read-only check confirms issues 1 and 2 and the marked comment. The recorded state limitation remains unchanged. |
| Stop before the first external write for user confirmation. | Pass. The user selected the sandbox before the recorded issue creation. This pass made no external writes. |
| Close `reviewed-local` only after user acceptance. | Pass. The gate is closed, and the Matter records the user's acceptance first. |
| Correct the first 403 discrepancy. | Pass for all requested cases. `Retry-After` and `X-RateLimit-Remaining: 0` make a 403 retryable. An unmarked permission 403 stays permanent. |
| Keep changed integration documentation consistent with registry behavior. | Pass. The outbox package comment now describes the injected registry and the GitHub provider registered by the stock binary. A repository search found no other stale implementation description. |
| Classify temporary provider failures as retryable. | **Fail.** A secondary-rate-limit 403 can omit both checked headers. GitHub identifies that case in the response message. The adapter does not inspect the message and returns `PermanentRefusal`. |

## Independent checks

The pass used fresh directories at `/tmp/wip-final-verifier-cache.NEp5Qs` and
`/tmp/wip-final-verifier-runtime.DNJiSX`.

| Command or check | Result |
|---|---|
| `env GOCACHE=/tmp/wip-final-verifier-cache.NEp5Qs XDG_RUNTIME_DIR=/tmp/wip-final-verifier-runtime.DNJiSX TMPDIR=/tmp/wip-final-verifier-runtime.DNJiSX go test -count=1 ./internal/tracker/github ./internal/tracker ./internal/verbs/outbox ./internal/cli` | Pass. All packages passed. The outbox verb package has no direct test files. |
| `env GOCACHE=/tmp/wip-final-verifier-cache.NEp5Qs XDG_RUNTIME_DIR=/tmp/wip-final-verifier-runtime.DNJiSX TMPDIR=/tmp/wip-final-verifier-runtime.DNJiSX go test -count=1 ./...` | Pass. All packages passed. |
| `env GOCACHE=/tmp/wip-final-verifier-cache.NEp5Qs XDG_RUNTIME_DIR=/tmp/wip-final-verifier-runtime.DNJiSX TMPDIR=/tmp/wip-final-verifier-runtime.DNJiSX go vet ./...` | Pass. The command produced no output. |
| `gofmt -d internal/cli/outbox_e2e_test.go internal/cli/root.go internal/verbs/outbox/outbox.go internal/tracker/registry.go internal/tracker/registry_test.go internal/tracker/github/github.go internal/tracker/github/github_test.go` | Pass. The command produced no output. |
| `git diff --check` | Pass. The command produced no output. |
| `sha256sum` on every reviewed changed implementation file | Pass. The values match the reviewed-revision table. |
| `gh issue view 1 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 1 is open with its creation marker and one marked provider comment. |
| `gh issue view 2 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 2 is open with a different creation marker and no comments. |
| Repository search for stale stock-provider and seam-injection descriptions | Pass. Only the corrected package comment describes current implementation behavior. Prior negative evidence remains unchanged as historical evidence. |

## Discrepancies and resolutions

The first prior pass found that all HTTP 403 responses became permanent
refusals. The Builder added header-aware classification. Focused tests confirm
that `Retry-After` or `X-RateLimit-Remaining: 0` makes a 403 retryable. They
also confirm that an ordinary permission 403 stays permanent.

The second prior pass found a stale package comment. The Builder changed the
comment to describe provider-registry resolution and static GitHub
registration. The comment now matches `internal/cli/root.go` and
`internal/verbs/outbox/outbox.go`.

One discrepancy remains. GitHub states that a secondary rate limit can return
HTTP 403 or 429. The response has a rate-limit message. GitHub separately says
that `Retry-After` can be absent and `X-RateLimit-Remaining` can be nonzero in
this case. The caller must still wait before retrying.

`isRateLimited` checks only those two headers. A secondary-rate-limit 403 with
neither marker reaches the default `PermanentRefusal` branch in
`resultForError`. The durable outbox then withholds temporary work instead of
queuing a retry. The adapter must also recognize GitHub's secondary-rate-limit
response message. A focused test must cover a 403 with that message and without
either marker header.

Source:
[GitHub REST API rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)

## Final verdict

The verdict is negative because one documented temporary GitHub response still
gets a permanent durable outcome. The `verified` gate remains open. A Builder
must correct the secondary-rate-limit 403 classification. A new Verifier must
then repeat the complete V1 pass against the corrected revision.
