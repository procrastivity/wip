# Tracker provider reverification

Date: 2026-08-14

Matter: `tracker-provider`

Verdict: **Negative**

The corrected GitHub error classification passes. The complete pass found one
new documentation discrepancy in the changed CLI integration. The discrepancy
prevents closure of `verified` under V1.

## Reviewed revision

The reviewed base revision is
`73a9a3e677c8cfc96d6f580dba9241255cf30b1a`. The provider change is an
uncommitted working-tree change on branch `matter/tracker-provider-1`.

The following SHA-256 values identify all reviewed implementation files:

| File | SHA-256 |
|---|---|
| `internal/cli/outbox_e2e_test.go` | `bbc192f9d794f7e73e129079e6279b545fb2e2d9f96f2d320bc1f9115f147aae` |
| `internal/cli/root.go` | `2b404ca49618fed4519d2c193344c8050b90cc130b27f9034fd0cd0cfd6ce086` |
| `internal/verbs/outbox/outbox.go` | `f2298a3c56c6f4a59142965b389de5b7c6245212e9f880d95a2797cc1bad22f9` |
| `internal/tracker/registry.go` | `ee363424775fcef533c73be5919debe1330e7db2005937715d42b10f8d5b23fb` |
| `internal/tracker/registry_test.go` | `52851e33aa1e42f4be2751656c5c1bb8f34c89ab352f9e6e104893958113a28f` |
| `internal/tracker/github/github.go` | `d6042726bc24dabdf6336c3bddd60be83bd054328b0a802635a867806ec00dc5` |
| `internal/tracker/github/github_test.go` | `9d9c3cf933558c01f8e9c63d81b80dffdbc94333eea68d19e166c33d24f57340` |

## Contract results

| Criterion | Observed result |
|---|---|
| Select and verify the first provider before implementation. | Pass. The Matter finding selects GitHub Issues and records the REST transport decision. GitHub documents `2026-03-10` as a supported API version. |
| Add a sibling provider package and register it behind the seam. | Pass. `internal/tracker/github` implements the seam. The CLI registers `github` through `tracker.Registry`. |
| Keep provider names out of domain events and projections. | Pass. The provider name occurs in provider registration and Repo configuration. Outbox entries and tracker events stay provider-neutral. |
| Map creation, comment, and state operations. | Pass with the stated state limit. Creation and comments use the GitHub REST API. State delivery returns a permanent refusal because GitHub has no required atomic lease guard. |
| Make half-failed creation and comment replay idempotent. | Pass. Focused tests deliver each entry twice and observe one POST. The second delivery finds the SHA-256 marker. |
| Keep the end-to-end suite hermetic without credentials. | Pass. Tests inject an in-memory HTTP transport and need no provider credentials. The complete test suite passes from fresh test directories. |
| Verify delegate, push, and rebind behavior in a sandbox. | Pass. A read-only check confirms that sandbox issues 1 and 2 remain open with distinct creation markers. Issue 1 has one marked provider comment. The recorded state candidates remain subject to the atomic-lease refusal. |
| Stop before the first external write for user confirmation. | Pass. The user selected the sandbox before the recorded issue creation. This pass made no external writes. |
| Close `reviewed-local` only after user acceptance. | Pass. The gate is closed, and the Matter records the user's acceptance first. |
| Classify temporary provider failures as retryable. | Pass. HTTP 408, HTTP 429, 5xx responses, transport errors, and marked rate-limit 403 responses become retryable failures. |
| Keep changed integration documentation consistent with behavior. | **Fail.** The package comment in `internal/verbs/outbox/outbox.go` describes the removed seam-injection behavior and says that the stock binary has no concrete provider. The CLI now injects a provider registry and registers GitHub. |

## Independent checks

The pass used fresh directories at `/tmp/wip-reverifier-cache.SmaH7d` and
`/tmp/wip-reverifier-runtime.4ov4iv`.

| Command or check | Result |
|---|---|
| `go test -count=1 ./internal/tracker/github ./internal/tracker ./internal/verbs/outbox ./internal/cli` | Pass |
| `go test -count=1 ./...` | Pass |
| `go vet ./...` | Pass, no output |
| `gofmt -d` on all reviewed Go files | Pass, no output |
| `git diff --check` | Pass, no output |
| `gh issue view 1 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 1 is open with its creation marker and one marked provider comment. |
| `gh issue view 2 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 2 is open with a different creation marker and no comments. |

## Discrepancies and resolutions

The prior artifact found that all HTTP 403 responses became permanent
refusals. The Builder corrected that behavior. A 403 response with
`Retry-After` or `X-RateLimit-Remaining: 0` now becomes a retryable failure.
An ordinary permission 403 remains a permanent refusal. Focused tests prove all
three cases. The behavior matches the
[GitHub rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).

The complete review found a separate unresolved discrepancy at
`internal/verbs/outbox/outbox.go:1`. The package comment says that the flush
command accepts an injected seam and that the stock binary has no concrete
provider. The changed command accepts a registry, and `internal/cli/root.go`
registers the GitHub provider. Update the package comment to describe the
registry and configured-backend behavior.

## Final verdict

The verdict is negative because the reviewed change contains one factual
contradiction between its documentation and behavior. The `verified` gate
remains open. A Builder must correct the package comment. A new Verifier must
then repeat the complete V1 pass against the corrected working tree.
