# Tracker project decisions

Date: 2026-09-19

Matter: `tracker-project`

This record supersedes the clause of decision L1 in
`tracker-provider-linear-decisions.md` that keeps project assignment outside
the provider contract. BDS-209 is the driving issue.

## Operative decisions

| Local | Operative decision |
|---|---|
| P1 | **Supersede L1's project clause with a Repo-tier `tracker.project` config key.** L1 kept project assignment outside the first provider contract. `wip plumbing outbox project <project-uuid>` now records an opaque project value, mirroring `tracker.target`. The value is a Repo-tier config key, not an event, and it is stored and read the same way `target` is. |
| P2 | **Validate the project UUID locally and never resolve it by name.** As L4 does for the team target, the adapter validates the project value as a UUID before it makes a request and never queries Linear to resolve a project name to an identifier. |
| P3 | **Send the project on `issueCreate` only.** `create` outbox rows come only from backlog delegation, so the project value only ever needs to reach issue creation. Boundary writes are `state` and `comment` operations against an already-bound tracker reference; neither carries or needs a project. |
| P4 | **Reconcile reads do not assert the project.** The UUID-filter reconcile reads from L8 look up an issue or comment by its client UUID. They deliberately do not assert that the found issue's project matches the configured value. |

## Consequences

Setting a project is optional and Linear-only. GitHub and GitLab continue to
ignore it and read the project from the clone's remote URL, as they already
do for `target`.

Because the project is validated locally and never resolved by name, an
unset or malformed project value fails the same way an unset or malformed
target does: before any network access, with no query cost.

Only `issueCreate` mutations carry `projectId`. `state` and `comment`
mutations act on a tracker reference obtained from a prior bind or create,
so they carry no project value and reconcile does not need one.

## Sources

- BDS-209
- `tracker-provider-linear-decisions.md` (L1, L4, L8)
