# Tracker trace 2 — Stage closure visibility

Status: **supported**.

A hermetic store ran at `narrated`, bound three references, created and
closed one Stage, and produced exactly one `comment` candidate for each
reference. Each payload named the Stage identity and title with action
`closed`. At `boundary`, the same Stage closure produces no comment.

The concrete GitHub adapter received a comment through an `httptest` server.
It searched for the idempotency marker, posted the comment once, and treated
a replay after a half-failed flush as delivered without a second post.

Executable evidence:

- `TestOffSuppressesCandidatesButBoundaryAndNarratedFanOut`
- `TestCommentDeduplicatesHalfFailedReplay`

The focused run on 2026-08-14 passed both tests.

Findings (D65, exhaustive): empty.
