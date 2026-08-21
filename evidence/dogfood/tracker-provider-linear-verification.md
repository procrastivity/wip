# Linear tracker provider verification

Date: 2026-08-21

Matter: `tracker-provider-linear`

Pass: 1

Verdict: **Negative**

## Reviewed revision

The review covered the uncommitted change on branch
`matter/tracker-provider-linear`, based on `origin/go`. The unrelated
untracked file `openai-models.md` was outside the review.

## Independent review scope

The Verifier read the Matter, Workplan, Linear decision record, inherited
guard contract, complete tracked diff, and all new Matter files. The review
mapped the adapter and tests against create, comment, state, classification,
config, factory, and hermetic-delivery obligations.

## Contract results

| Criterion | Result |
|---|---|
| Provider-neutral target and factory plumbing | Pass. `tracker.target` stays Repo-tier config, and `FactoryInput` carries its opaque value. |
| Static Linear registration | Pass. The stock registry contains GitHub and Linear. |
| Create replay | Pass. The focused test calls `Deliver` twice, observes one mutation, and requires the second result to be `Converged`. |
| Comment replay | **Fail.** The focused test proves reconciliation after an ambiguous accepted mutation, but it calls `Deliver` only once. The Workplan requires two deliveries, one external write, and `Converged` on the second delivery. |
| State guard rows | Pass in static review. The adapter reads before each possible write and covers blank leases, matching leases, drift, convergence, and competing terminal states. |
| GraphQL and HTTP classification | Pass in static review. The adapter checks GraphQL errors on HTTP 200, handles `RATELIMITED`, and separates retryable transport or server failures from permanent refusals. |
| No-socket hermetic transport | Pass. Tests inject `http.RoundTripper` implementations and do not open a test server. |

## Independent checks

| Check | Result |
|---|---|
| Complete static contract and diff review | Pass, with the comment replay discrepancy above. |
| Fresh credential-free focused Linear tests | Pass. |
| Fresh credential-free tracker package tests | Pass. |
| Fresh full repository test pass | Not used for the verdict. The run was interrupted after the decisive contract discrepancy was confirmed. |

## Required correction

Add one focused comment test that calls `Deliver` twice with the same outbox
entry. The first call must return `Delivered`. The second call must return
`Converged`, and both calls together must perform exactly one comment-create
mutation. Keep the existing ambiguous-write reconciliation coverage as a
separate row.

The `verified` gate stays open. A new Verifier must repeat the independent
pass after the correction.
