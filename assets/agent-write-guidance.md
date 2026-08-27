if you are an agent following harness instructions: your prose input goes
through a verb argument, stdin, or a scratch-file reference — never a file
edit. `.wip/generated/` is a snapshot from the last `wip refresh`, not live
state. If it disagrees with `wip status` or `wip next`, run `wip refresh`
(or `wip refresh <locator>` if sealed) and re-read.

Treat tracker bindings as provenance, not integration. `wip bind` records a
tracker reference. It does not connect wip to the tracker or prove tracker
state. Never report a tracker's status without a read from that tracker.
