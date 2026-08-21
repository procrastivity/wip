# Linear tracker provider verification pass

Date: 2026-08-21

Matter: `tracker-provider-linear`

Pass: 2

Verifier role: `01M0J22H5QYBGXMDVQ0RFK7K2Z`

Verdict: **Positive**

The fresh independent pass confirms the pass-1 correction and finds no new
discrepancy in the hermetic contract. Create and comment replay each perform
one mutation across two deliveries. The second delivery returns `Converged`.

## Reviewed revision

The review covered the uncommitted change on branch
`matter/tracker-provider-linear`, based on `origin/go` at
`ebc5c44daba582c57cb2792e816ca39b4fb624b3`. The unrelated untracked file
`openai-models.md` was outside the review.

The review included the Matter, Workplan, Linear decision record, pass-1
negative evidence, and inherited provider and guard-read evidence. It also
included the complete tracked diff and every intended new Matter file.

The following SHA-256 values identify every reviewed changed Go file:

| File | SHA-256 |
|---|---|
| `internal/cli/alignment_e2e_test.go` | `e293d8ff2b2573c0753f4b8a97f713980cb156bb41a8ac6be3b1423e38dea19d` |
| `internal/cli/outbox_e2e_test.go` | `8b5701597c896eb7887adf446f54d3e4c5644f16a8834ff12c520384f600598a` |
| `internal/cli/root.go` | `6eaa002033858f92248b150c3059b1fc3f30ade5a444e23410b5a11959c57b46` |
| `internal/cli/tracker_start_transition_e2e_test.go` | `63e618cd27e25c0807dddab485c1395f9e188af7c728ad85b57290cd0bdea70d` |
| `internal/cli/linear_tracker_e2e_test.go` | `584d07ff027622efcbe16e10f639e8d8c84ec4d99fd2aec89e1d453c36f62e78` |
| `internal/scheduler/loop_test.go` | `70969bc8f3e3956a4806e8539de3947a3d47826173c260262c6b8fb70b4c9c51` |
| `internal/store/config.go` | `077e0a94f7ac3e0f90c03160bea5c85b0fef07b774d859091264e58e171386fc` |
| `internal/store/tracker_config_test.go` | `02f8c59e81bbfcdc85bb89299bd23a629077efb6af3e13e05a1f4e78a526367b` |
| `internal/tracker/alignment.go` | `0f6c9ce880b642dcb0656c6b161d5e41272c2bb84522edec5175d67fb0901277` |
| `internal/tracker/alignment_test.go` | `863fcb33bf857dcda32fedd3fe254d876fa158b510d972e7770b57101157abf3` |
| `internal/tracker/github/github.go` | `e9aacce92ec2a5fd75f6e2a62c8519a7add44e786b3f1f73eb79020ab8ecb7e9` |
| `internal/tracker/github/github_test.go` | `9dc7be5a9d57f14e1d0a119aae7f957b27c95f7bfe335d0c94565b6437bb5593` |
| `internal/tracker/registry.go` | `6ccda3d52761737dfeb37f6c23d97db4307ee835d65a40176a98ae4f2d2ed9cf` |
| `internal/tracker/registry_test.go` | `bb2fafdd49774f4f4602136c208a1c8273ff2f1d57cc2fa48e8b603fdb8e134b` |
| `internal/tracker/linear/linear.go` | `b38057d25e00432651c771cd4317c468b3714a82abd55efb85bdd4aca628e7a7` |
| `internal/tracker/linear/linear_test.go` | `1c7808576f5c1164b1cca991f20f2fa336b58997f4a81accd836c241d949d2a7` |
| `internal/verbs/outbox/outbox.go` | `eb12e5f4e7f466cf40f932c0ecafabe0bdc1b22830dee955d7962253ef82ce37` |

## Pass-1 correction

`TestCommentConvergesOnSecondDeliveryWithoutAnotherMutation` now exercises
the missing replay row directly. It calls `Deliver` twice with one immutable
comment entry. The first result must be `Delivered`, and the second must be
`Converged`. Both deliveries use the same deterministic UUID, perform two
lookup reads, and perform exactly one `CreateComment` mutation.

The existing ambiguous-write test remains separate. It still proves that a
single delivery can reconcile an accepted mutation after a failed response.

## Hermetic contract matrix

| Criterion | Evidence | Result |
|---|---|---|
| Sibling provider and static registration | `internal/tracker/linear` implements the seam. The stock registry registers `linear` next to `github`. | Pass |
| Provider-neutral target and factory plumbing | `tracker.target` is opaque Repo-tier config. `FactoryInput` carries `Repo` and `Target`. GitHub ignores the target. Alignment and flush pass the target without giving providers store behavior. | Pass |
| Create delivery and replay | The create test calls delivery twice, expects `Delivered` then `Converged`, and observes one issue mutation. Deterministic UUID lookup also reconciles an ambiguous mutation. | Pass |
| Comment delivery and replay | The corrected comment test calls delivery twice, expects `Delivered` then `Converged`, and observes one comment mutation. The separate ambiguous-mutation row also passes. | Pass |
| State delivery behind a guard read | The adapter selects by workflow `state.type`, reads `issue.updatedAt` before every possible write, and returns provider-neutral outcomes with the required lease. | Pass |
| Empty-lease guard rows | Backlog, triage, and unstarted can advance to active. Active can advance to a terminal candidate. A matching state converges. Backlog-to-terminal and competing terminal states return `LeaseMismatch`. | Pass |
| Recorded-lease guard rows | Matching lease writes. Changed lease elsewhere and competing terminal states return `LeaseMismatch`. A reached candidate converges, and completed is safely beyond an active candidate. | Pass |
| Classification rows | HTTP 200 and 400 `RATELIMITED`, HTTP 408, HTTP 429, HTTP 503, and exhausted rate-limit headers are retryable. Authentication, authorization, bad input, unknown GraphQL codes, and complexity exhaustion are permanent. | Pass |
| Replay identity validation | Deduplicated issues must have one matching UUID, a valid identifier, and the configured team. Deduplicated comments must have one matching UUID. | Pass |
| Backend and target end to end | CLI tests persist and reread both values, pass the opaque target into the factory, flush a reconciled Linear creation, persist `BDS-240`, and expose the target command in the manifest. | Pass |
| No credentials and no sockets | All commands ran with Linear and GitHub token variables unset. Tests inject `http.Client` transports and use `httptest.NewRecorder`; no listener, server, or dial call occurs in the Linear or CLI test surface. | Pass |
| Provider-neutral event and projection vocabulary | Exact searches find no provider-name literal in the event taxonomy, payloads, projections, schemas, provider-neutral flush, alignment, or write-surface code. | Pass |

Live sandbox verification and the user-owned `reviewed-local` gate are not
hermetic assertions in this pass. This pass makes no external request or
credentialed write.

## Independent checks

The pass used fresh directories:

- `GOCACHE=/tmp/wip-linear-reverify-gocache.tNWygf`
- `XDG_RUNTIME_DIR=/tmp/wip-linear-reverify-runtime.Yh9VMp`
- `TMPDIR=/tmp/wip-linear-reverify-runtime.Yh9VMp`

Each Go command removed `WIP_LINEAR_TOKEN`, `LINEAR_API_KEY`,
`WIP_GITHUB_TOKEN`, `GH_TOKEN`, and `GITHUB_TOKEN` from its environment.

| Command or check | Exact result |
|---|---|
| `go test -count=1 -v ./internal/tracker/linear ./internal/tracker ./internal/verbs/outbox ./internal/cli` | Pass. Linear passed in 0.013s, tracker passed in 4.811s, outbox has no direct test files, and CLI passed in 46.516s. The corrected comment replay test passed. |
| `go test -count=1 ./...` | Pass. Every package passed. The slowest package was store at 89.534s. |
| `go vet ./...` | Pass. Exit 0 with no output. |
| `gofmt -d` on all 17 changed Go files | Pass. Exit 0 with no output. |
| `git diff --check` | Pass. Exit 0 with no output. A second run against `origin/go` also passed. |
| Socket search for `net.Listen`, `httptest.NewServer`, `http.ListenAndServe`, `ListenAndServeTLS`, `net.Dial`, `DialContext`, and `ListenPacket` in the Linear adapter and affected CLI tests | Pass. Exit 1 with no matches. |
| Case-insensitive provider-name search in `taxonomy.go`, `payloads.go`, `project.go`, and `schema_v*.go` | Pass. Exit 1 with no matches. |
| Exact `"github"` or `"linear"` literal search in provider-neutral outbox write-surface, flush, and alignment code | Pass. Exit 1 with no matches. |

## Final verdict

The verdict is positive. The pass-1 correction supplies the required direct
comment replay proof, every hermetic criterion passes, and no new discrepancy
remains. This evidence exists before the `verified` gate close.
