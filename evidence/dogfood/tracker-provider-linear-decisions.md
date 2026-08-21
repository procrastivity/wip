# Linear tracker provider decisions

Date: 2026-08-21

Matter: `tracker-provider-linear`

This record closes the nine decisions assigned to Step 01. The decisions use
the provider-neutral tracker contract from the sealed predecessor Matters.
They also use the current Linear API documentation and schema.

## Operative decisions

| Local | Operative decision |
|---|---|
| L1 | **Use one provider-neutral `tracker.target` Repo-tier config key.** The Linear provider interprets the value as a team UUID. Project assignment is optional in Linear and stays outside the first provider contract. The `wip outbox target` command reads or writes the opaque value. |
| L2 | **Widen the factory input with values, not behavior.** `tracker.FactoryInput` carries `store.Repo` and the configured target string. The outbox and alignment call sites read both config values before registry resolution. A provider factory does not receive a store or a config reader. |
| L3 | **Store the Linear issue identifier as the tracker reference.** Creation returns an identifier such as `BDS-123`. Comment and state operations pass that identifier to Linear. Rebind stores the same identifier. |
| L4 | **Store a team UUID, not a team key.** `issueCreate` requires `teamId`. A UUID avoids a resolution query on each factory construction or flush. The adapter validates the target before it makes a request. |
| L5 | **Use only a deterministic client UUID for create and comment identity.** Derive 128 bits from SHA-256 over a kind-specific namespace and the outbox idempotency key. Set the UUID version and variant bits to the UUIDv4 form that Linear documents. Do not add an HTML comment marker because Linear does not promise that ProseMirror-to-Markdown conversion preserves HTML comments. |
| L6 | **Classify GraphQL errors before HTTP status defaults.** Transport failures, HTTP 429, HTTP 5xx, and GraphQL `RATELIMITED` errors are retryable. Authentication, authorization, invalid input, user errors, and unknown GraphQL codes are permanent refusals. Any nonempty GraphQL `errors` array makes that operation fail, including an HTTP 200 response. |
| L7 | **Use explicit token precedence.** `Options.Token` wins in tests and injected use. Production then reads `WIP_LINEAR_TOKEN` and `LINEAR_API_KEY` in that order. Personal API keys use the raw `Authorization` value without a `Bearer` prefix. OAuth is outside scope. |
| L8 | **Reconcile ambiguous creates through UUID filters.** Before a mutation, query `issues` or `comments` with `id.eq` and `includeArchived`. After any mutation failure, repeat the query. A hit returns `Converged`. An empty query result leaves the original failure classification in force. The adapter does not assume that a create mutation is an upsert, and it does not depend on trashed-issue lookup. |
| L9 | **Map dispositions to Linear workflow types.** Active maps to `started`, completed maps to `completed`, and canceled maps to `canceled`. Query the target team's live workflow states and select the lowest `position` for the required type, with the UUID as a stable tie-break. Any observed state with the requested type has converged. `triage`, `backlog`, and `unstarted` are behind active. A blank lease can move those states only for an active candidate. A blank lease can also move a `started` issue to a terminal candidate. `duplicate` and unknown terminal types are competing terminal states. Missing target types are permanent refusals. |

## Consequences

The target config stays provider-neutral and eventless. GitHub ignores the
target value. Linear rejects an empty or malformed team UUID before network
access.

Creation starts the Linear issue in the selected `started` state. This choice
lets the first empty-lease active candidate converge after its guard read. It
also avoids an unsafe assertion over a default Backlog or unstarted state.

Linear accepts client UUIDs on issue and comment creation, but its schema does
not call the UUID an idempotency key. Linear also does not document the exact
duplicate-ID error. The provider therefore uses a read-mutate-read sequence.
The same UUID prevents a second object even when a trashed object cannot be
read through the collection query.

The state guard reads `issue.updatedAt` immediately before each possible
write. A matching lease permits a forward write. A requested workflow type
that already holds returns `Converged`. A changed lease elsewhere returns
`LeaseMismatch`.

The sealed start-transition contract treats a backlog-like state as safely
behind an active candidate. Thus, an active candidate with no prior lease can
move `triage`, `backlog`, or `unstarted` to `started`. A blank lease cannot
move those states directly to a terminal type. An observed `started` state can
converge on active or move to a terminal candidate. Other blank-lease rows
return `LeaseMismatch`.

Linear can have several workflow states with the same type. The BDS workspace
has two `started` states. Selection by state name or a uniqueness requirement
would therefore reject a normal Linear workflow. Type and position provide a
stable provider rule without adding Linear names to wip events.

## API facts that remain undocumented

Linear does not document the duplicate-ID mutation error, GraphQL lookup of a
trashed issue, or HTML comment preservation after a Markdown round trip. The
hermetic suite must not invent guarantees for those cases. Live sandbox
verification can record observed behavior, but those observations do not
replace the guarded UUID design.

The rate-limit page gives two different hourly request totals. The adapter
does not hardcode either total. It uses the response code, GraphQL error code,
and rate-limit headers. Its queries remain far below the per-query complexity
limit.

## Sources

- [Linear GraphQL getting started](https://linear.app/developers/graphql)
- [Linear API rate limiting](https://linear.app/developers/rate-limiting)
- [IssueCreateInput schema](https://studio.apollographql.com/public/Linear-API/variant/current/schema/reference/inputs/IssueCreateInput)
- [CommentCreateInput schema](https://studio.apollographql.com/public/Linear-API/variant/current/schema/reference/inputs/CommentCreateInput)
- [IssueFilter schema](https://studio.apollographql.com/public/Linear-API/variant/current/schema/reference/inputs/IssueFilter)
- [CommentFilter schema](https://studio.apollographql.com/public/Linear-API/variant/current/schema/reference/inputs/CommentFilter)
- [WorkflowState schema](https://studio.apollographql.com/public/Linear-API/variant/current/schema/reference/objects/WorkflowState)
- [Linear agent API best practices](https://linear.app/developers/agent-best-practices)
