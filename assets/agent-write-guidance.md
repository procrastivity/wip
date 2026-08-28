if you are an agent following harness instructions: your prose input goes
through a verb argument, stdin, or a scratch-file reference — never a file
edit. `.wip/generated/` is a snapshot from the last `wip refresh`, not live
state. If it disagrees with `wip status` or `wip next`, run `wip refresh`
(or `wip refresh <locator>` if sealed) and re-read.

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

A linked git worktree is its own Worktree to wip. When a verb there refuses
because the location is unknown, run `wip init` in that worktree first: wip
attaches it to the clone it already knows, and the verb then works. Do not
switch to the main checkout to work around the refusal.
