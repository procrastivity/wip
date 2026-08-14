# Tracker provider verification

Date: 2026-08-14

Matter: `tracker-provider`

Verdict: **Negative**

The provider implementation satisfies the stated delivery, replay, live-test,
and hermetic-test criteria. One unresolved error-classification discrepancy
prevents closure of `verified`.

## Reviewed revision

The reviewed base revision is
`73a9a3e677c8cfc96d6f580dba9241255cf30b1a`. The provider change is an
uncommitted working-tree change on branch `matter/tracker-provider-1`.

The following SHA-256 values identify the reviewed files:

| File | SHA-256 |
|---|---|
| `internal/cli/outbox_e2e_test.go` | `bbc192f9d794f7e73e129079e6279b545fb2e2d9f96f2d320bc1f9115f147aae` |
| `internal/cli/root.go` | `2b404ca49618fed4519d2c193344c8050b90cc130b27f9034fd0cd0cfd6ce086` |
| `internal/verbs/outbox/outbox.go` | `f2298a3c56c6f4a59142965b389de5b7c6245212e9f880d95a2797cc1bad22f9` |
| `internal/tracker/registry.go` | `ee363424775fcef533c73be5919debe1330e7db2005937715d42b10f8d5b23fb` |
| `internal/tracker/registry_test.go` | `52851e33aa1e42f4be2751656c5c1bb8f34c89ab352f9e6e104893958113a28f` |
| `internal/tracker/github/github.go` | `6c658f319b3625dbca7d2eed93f61a3ae6f3bfb95a1abad6d308ab92da5624ac` |
| `internal/tracker/github/github_test.go` | `f68bb33dca8bd0c1d16157828f095cacd215c26cad5e7041ee0769e112f7b475` |

## Contract results

| Criterion | Observed result |
|---|---|
| Select and verify the first provider before implementation. | Pass. The Matter finding selects GitHub Issues and records the verified REST limits. |
| Add a sibling provider package and register it behind the seam. | Pass. `internal/tracker/github` implements the seam. `internal/cli/root.go` registers `github` through `tracker.Registry`. |
| Keep provider names out of domain events and projections. | Pass. The provider name exists only in provider registration and Repo configuration. Outbox entries and tracker events stay provider-neutral. |
| Map creation, comment, and state operations. | Pass with the intended state limit. Creation and comments use the GitHub REST API. State delivery returns a permanent refusal because GitHub has no required atomic lease guard. |
| Make half-failed creation and comment replay idempotent. | Pass. Focused tests deliver the same entry twice and observe one POST. The second delivery finds the SHA-256 marker. |
| Keep the end-to-end suite hermetic without credentials. | Pass. Tests inject an in-memory HTTP transport and open no sockets. The full suite passes without provider credentials. |
| Verify delegate, push, and rebind behavior in a sandbox. | Pass. Issues 1 and 2 in `simensen/terminal-multiplexers` contain creation markers. Issue 1 contains the Stage-closure comment marker. Rebind queued issue 2 as the destination. State candidates for both references were withheld with the atomic-lease refusal. |
| Stop before the first external write for user confirmation. | Pass. The user selected the sandbox before issue creation. |
| Close `reviewed-local` only after user acceptance. | Pass. The user accepted the implementation and live evidence before the gate closed. |
| Classify temporary provider failures as retryable. | **Fail.** A GitHub rate-limit response can use HTTP 403, but the adapter classifies every 403 as permanent. |

## Independent checks

The verification used fresh directories at `/tmp/wip-verifier-cache.ZUlClE`
and `/tmp/wip-verifier-runtime.1nEC8C`.

| Command or check | Result |
|---|---|
| Focused provider, registry, outbox, and manifest tests with `go test -count=1` | Pass |
| `go test -count=1 ./...` | Pass |
| `go vet ./...` | Pass |
| `gofmt -d` on all changed Go files | Pass, no output |
| `git diff --check` | Pass, no output |
| Independent `gh issue view` for sandbox issues 1 and 2 | Pass. Both issues are open and contain distinct creation markers. Issue 1 contains one marked provider comment. |

## Unresolved discrepancy

`internal/tracker/github/github.go:274` defaults API errors to
`PermanentRefusal`. It changes the outcome only for HTTP 408, HTTP 429, and
5xx responses. GitHub documents that primary and secondary rate limits can
return HTTP 403 or HTTP 429. A rate-limited 403 therefore becomes withheld
permanent work instead of a queued retryable failure.

The correction must distinguish a permission 403 from a rate-limit 403.
Response headers such as `Retry-After` and `X-RateLimit-Remaining: 0` provide
the required signal. Tests must prove that a marked rate-limit 403 is
retryable and that an ordinary permission 403 stays permanent.

Source:
[GitHub REST API rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)

## Final verdict

The verdict is negative because one provider failure class has the wrong
durable outcome. The `verified` gate remains open. A Builder must correct the
discrepancy. A new Verifier must repeat the complete pass against the corrected
revision.
