# Tracker provider verification pass

Date: 2026-08-14

Matter: `tracker-provider`

Verdict: **Positive**

The complete fresh pass satisfies the Matter contract at the reviewed working
tree. The three Builder corrections resolve all prior discrepancies. No new
discrepancy remains.

## Reviewed revision

The reviewed base revision is
`73a9a3e677c8cfc96d6f580dba9241255cf30b1a`. The provider change is an
uncommitted working-tree change on branch `matter/tracker-provider-1`.

The following SHA-256 values identify every changed implementation file:

| File | SHA-256 |
|---|---|
| `internal/cli/outbox_e2e_test.go` | `bbc192f9d794f7e73e129079e6279b545fb2e2d9f96f2d320bc1f9115f147aae` |
| `internal/cli/root.go` | `2b404ca49618fed4519d2c193344c8050b90cc130b27f9034fd0cd0cfd6ce086` |
| `internal/verbs/outbox/outbox.go` | `b69ca5c682a75c35b0650ce290095f91240a57f0260623215d063e7e77d7104c` |
| `internal/tracker/registry.go` | `ee363424775fcef533c73be5919debe1330e7db2005937715d42b10f8d5b23fb` |
| `internal/tracker/registry_test.go` | `52851e33aa1e42f4be2751656c5c1bb8f34c89ab352f9e6e104893958113a28f` |
| `internal/tracker/github/github.go` | `3e456859a59d726280022b50208de141200032a946857696fa8853ddc6e2dd26` |
| `internal/tracker/github/github_test.go` | `4b59c37eece162b1c2f6b367660485c3a8cb12ae8d3a4472ce4ac4c0ecdc59fb` |

## Review scope

The review included the complete Matter, Workplan, roadmap, findings, and
current implementation change. It also included the three prior negative
artifacts and all three Builder correction findings. The pass followed V1-V5
in `evidence/dogfood/verification-model-decisions.md`.

The review checked the current GitHub REST documentation. GitHub supports API
version `2026-03-10`. GitHub documents primary and secondary rate-limit
responses as HTTP 403 or 429. A secondary limit can use only the JSON error
message as its rate-limit signal.

GitHub also states that conditional requests do not support unsafe methods
unless an endpoint documents an exception. The issue update endpoint documents
no exception. The provider therefore keeps state updates behind the recorded
atomic-lease refusal.

Sources:

- [GitHub REST API versions](https://docs.github.com/en/rest/about-the-rest-api/api-versions?apiVersion=2026-03-10)
- [GitHub REST API rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=latest)
- [GitHub REST API conditional requests](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api?apiVersion=2026-03-10)
- [GitHub issue endpoints](https://docs.github.com/en/rest/issues/issues?apiVersion=2026-03-10)

## Contract results

| Criterion | Observed result |
|---|---|
| Select and verify the first provider before implementation. | Pass. The Matter selects GitHub Issues and its versioned REST transport. GitHub currently supports API version `2026-03-10`. |
| Add a sibling provider package and register it behind the seam. | Pass. `internal/tracker/github` implements the seam. The CLI registers `github` through `tracker.Registry`. |
| Keep provider names out of domain events and projections. | Pass. The provider name occurs in static registration and generic Repo configuration. An exact-string search found no `"github"` literal in store, write-surface, or provider-neutral flush code. |
| Map creation, comment, and state operations. | Pass with the recorded state limit. Creation and comments use GitHub REST calls. State delivery returns the required atomic-lease refusal. |
| Make half-failed creation and comment replay idempotent. | Pass. Focused tests deliver each entry twice and observe one POST. The second delivery finds the SHA-256 marker. |
| Keep the end-to-end suite hermetic without credentials. | Pass. Tests inject an in-memory HTTP transport. The complete suite passes from fresh cache and runtime directories. |
| Verify delegate, push, and rebind behavior in a sandbox. | Pass. Read-only checks confirm issues 1 and 2 and the marked comment. The Matter records the rebind and state-refusal results. |
| Stop before the first external write for user confirmation. | Pass. The user selected the sandbox before the recorded issue creation. This pass made no external writes. |
| Close `reviewed-local` only after user acceptance. | Pass. The gate is closed, and the Matter records the user's acceptance first. |
| Classify temporary GitHub failures as retryable. | Pass. HTTP 408, HTTP 429, 5xx responses, transport errors, and all documented rate-limit 403 forms become retryable. |
| Keep ordinary permission failures permanent. | Pass. A 403 response with `Resource not accessible by integration` remains a permanent refusal. |
| Keep changed integration documentation consistent with behavior. | Pass. The outbox package comment describes registry resolution and the GitHub provider in the stock binary. No stale description remains in Go files. |

## Independent checks

The pass used fresh directories at `/tmp/wip-pass-verifier-cache.rSoG3Q` and
`/tmp/wip-pass-verifier-runtime.O7qeXR`.

| Command or check | Result |
|---|---|
| `env GOCACHE=/tmp/wip-pass-verifier-cache.rSoG3Q XDG_RUNTIME_DIR=/tmp/wip-pass-verifier-runtime.O7qeXR TMPDIR=/tmp/wip-pass-verifier-runtime.O7qeXR go test -count=1 -run TestForbiddenFailuresAreClassified -v ./internal/tracker/github` | Pass. The `Retry-After`, exhausted primary limit, message-only secondary limit, and permission-denial subtests all passed. |
| `env GOCACHE=/tmp/wip-pass-verifier-cache.rSoG3Q XDG_RUNTIME_DIR=/tmp/wip-pass-verifier-runtime.O7qeXR TMPDIR=/tmp/wip-pass-verifier-runtime.O7qeXR go test -count=1 ./internal/tracker/github ./internal/tracker ./internal/verbs/outbox ./internal/cli` | Pass. All packages passed. The outbox verb package has no direct test files. |
| `env GOCACHE=/tmp/wip-pass-verifier-cache.rSoG3Q XDG_RUNTIME_DIR=/tmp/wip-pass-verifier-runtime.O7qeXR TMPDIR=/tmp/wip-pass-verifier-runtime.O7qeXR go test -count=1 ./...` | Pass. All packages passed. |
| `env GOCACHE=/tmp/wip-pass-verifier-cache.rSoG3Q XDG_RUNTIME_DIR=/tmp/wip-pass-verifier-runtime.O7qeXR TMPDIR=/tmp/wip-pass-verifier-runtime.O7qeXR go vet ./...` | Pass. The command produced no output. |
| `gofmt -d internal/cli/outbox_e2e_test.go internal/cli/root.go internal/verbs/outbox/outbox.go internal/tracker/registry.go internal/tracker/registry_test.go internal/tracker/github/github.go internal/tracker/github/github_test.go` | Pass. The command produced no output. |
| `git diff --check` | Pass. The command produced no output. |
| `sha256sum` on every changed implementation file | Pass. The values match the reviewed-revision table. |
| Repository search for stale stock-provider descriptions | Pass. The search returned no matches in Go files. |
| Exact-string search for provider leakage | Pass. The search found no `"github"` literal in store, write-surface, or provider-neutral flush code. |
| `gh issue view 1 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 1 is open with its creation marker and one marked provider comment. |
| `gh issue view 2 --repo simensen/terminal-multiplexers --json number,state,title,body,comments,url` | Pass. Issue 2 is open with a different creation marker and no comments. |

## Discrepancies and resolutions

The first pass found that every HTTP 403 response became a permanent refusal.
The first correction added header-aware classification. `Retry-After` or
`X-RateLimit-Remaining: 0` now makes a 403 response retryable.

The second pass found a stale outbox package comment. The second correction
now describes injected registry resolution and the GitHub provider that the
stock binary registers.

The third pass found that a message-only secondary-rate-limit 403 response
remained permanent. The third correction parses the JSON error message and
recognizes the documented secondary-rate-limit phrase.

The focused test independently proves all four required 403 cases. The two
header forms and the message-only form are retryable. An ordinary permission
denial remains permanent. No discrepancy remains unresolved.

## Final verdict

The verdict is positive. Every contract criterion and independent check passes
at the identified working tree. The evidence exists before the `verified` gate
close, as V1 and V5 require.
