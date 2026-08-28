if you are an agent following harness instructions: your prose input goes
through a verb argument, stdin, or a scratch-file reference — never a file
edit. `.wip/generated/` is a snapshot from the last `wip refresh`, not live
state. If it disagrees with `wip status` or `wip next`, run `wip refresh`
(or `wip refresh <locator>` if sealed) and re-read.

When a verb refuses with "this clone is unknown to wip", wip has never been
set up in this repo. Stop and report that to the user, then offer `wip
init`. That command is a write, so when you are in a read-only mode, ask
the user to run it rather than running it yourself. Do not answer the
question by reading wip's source, its database, or another repo's `.wip/`;
`--help` and this skill are the whole reference. A linked worktree of a
clone wip already knows is a different case: run `wip init` there to
attach it.

Treat tracker bindings as provenance, not integration. `wip bind` records a
tracker reference. It does not connect wip to the tracker or prove tracker
state. Never report a tracker's status without a read from that tracker.

Approval is the human boundary. `wip backlog delegate`, `wip finish` and
`wip cancel` only queue outbox work; nothing reaches a tracker until a
person runs `wip outbox approve` and `wip outbox flush`. Do not run either
unless the user asked for it in the current turn. A queued outbox row is a
decision waiting for a person, not unfinished work — leave it queued and
say so.

The backlog is local by default. `wip backlog delegate` is an explicit exit
that queues a tracker item; `wip backlog decline` drops the entry with a
reason. When asked to "clean up the backlog", do not guess which one is
meant — ask.

Configuring a tracker takes five commands, in this order:

1. `wip init` attaches the clone to wip.
2. `wip outbox backend <name>` picks the tracker; the registered names
   are `github`, `gitlab` and `linear`, and an unknown name is refused
   with the list.
3. `wip outbox target <team-uuid>` is for Linear only; GitHub and GitLab
   read the project from the clone's remote URL and ignore the target.
4. `wip outbox level boundary|narrated` is optional; once a backend is
   set the level defaults to `boundary`, and only `narrated` must be
   chosen on purpose.
5. `wip bind <locator> <ref>` records which tracker item a Matter's
   boundary writes address.

Credentials come from the tracker CLI or environment already on the host,
never from wip: GitLab reads `WIP_GITLAB_TOKEN`, `GITLAB_TOKEN`, then
glab's own config; GitHub reads `WIP_GITHUB_TOKEN`, `GH_TOKEN`,
`GITHUB_TOKEN`; Linear reads `WIP_LINEAR_TOKEN`, `LINEAR_API_KEY`. None of
these commands contacts the tracker. Writes queue in the outbox and reach
the tracker only after a person runs `wip outbox approve` and
`wip outbox flush`, as the approval paragraph above says.

A linked git worktree is its own Worktree to wip. When a verb there refuses
because the location is unknown, run `wip init` in that worktree first: wip
attaches it to the clone it already knows, and the verb then works. Do not
switch to the main checkout to work around the refusal.
