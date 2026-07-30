# `tiers` — worked examples

PLAN 1.1's seven worked examples, produced against the real `tier-verbs`
implementation — real rows, real keys, real `wip status` output, no mocks.
Presentation order (1–7) carries no sequencing meaning (D51); each is
independent. Example 7 is deliberately scoped to tiers, key resolution, and
`status` output only, per the seed card and PLAN's own framing note.

Every transcript below is real command output, captured either directly from
the built binary or from `internal/cli/worked_examples_test.go`'s `t.Logf`
lines (noted per example) — the same file mechanically re-verifies every
example on every `go test ./...` run, so this document cannot drift out of
sync with the behavior it describes without a test failing first.

## Example 1 — three clones, one Repo (fresh, moved, linked worktree)

A fresh clone (`widget-a`), a second clone (`widget-b`) moved on disk after
`init` and recovered via `doctor`'s relink offer rather than a duplicate row,
and a linked worktree of `widget-b` with its own Worktree row. All three
resolve to the same Repo.

```
$ wip doctor            # from widget-b, moved to widget-b-moved
this clone is unknown, but its remote matches known repo 01KYRN07G1T460JXM19RMB9BHD
  relink: wip clone relink <clone-locator>   (this clone moved)
  new clone: wip init                        (a separate clone of the same repo)
  existing clones of this repo:
    widget-a             01KYRN07G1T460JXM19RMB9BHE
    widget-b             01KYRN07J5QJF0PQ2SH39JGT75
$ wip clone relink widget-b
relinked clone "widget-b" to /private/tmp/wip-examples-work/widget-b-moved/.git

$ wip status   # from widget-a
acme/widget
  widget-a           Clone · current
  widget-b           Clone
  widget-b-feature   Worktree (of widget-b)

$ wip status   # from widget-b (moved+relinked)
acme/widget
  widget-a           Clone
  widget-b           Clone · current
  widget-b-feature   Worktree (of widget-b)

$ wip status   # from widget-b-feature (linked worktree)
acme/widget
  widget-a           Clone
  widget-b           Clone
  widget-b-feature   Worktree (of widget-b) · current
```

`wip clone list --json` after recovery shows exactly two Clone rows for this
Repo — recovery via relink, never a duplicate.

## Example 2 — a linked worktree, then `git worktree move`d

A Worktree row created via `wip init` inside a linked worktree, then moved
with `git worktree move`. The Worktree's natural key (its git-assigned name,
MODEL §7) is unaffected by the filesystem move — only a Clone's
common-dir-keyed identity is move-detection's concern.

```
$ wip init   # inside the linked worktree, before the move
{"repo":"01KYRN0EH9SHFHSBFJ7WQPT9V9","repoCreated":false,"remoteUrl":"github.com/acme/widget","clone":"01KYRN0EH9SHFHSBFJ7WQPT9VA","cloneLabel":"widget","worktree":"01KYRN0EJJ07G2SWPXD4E6CB0Y","worktreeName":"widget-feature"}

$ git worktree move widget-feature ../widget-feature-moved

$ wip status --json   # from the new location
{"hostWide":false,"repo":{"id":"01KYRN0EH9SHFHSBFJ7WQPT9V9","header":"acme/widget","clones":[{"id":"01KYRN0EH9SHFHSBFJ7WQPT9VA","label":"widget","current":false,"worktrees":[{"id":"01KYRN0EJJ07G2SWPXD4E6CB0Y","name":"widget-feature","current":true}]}]}}
```

The Worktree's `id` (`…4E6CB0Y`) and `name` (`widget-feature`) are byte-for-byte
identical before and after the move — the natural key never referenced the
filesystem path at all, only git's own worktree name.

## Example 3 — a moved clone, same common-dir

A clone directory renamed such that `git rev-parse --git-common-dir` still
resolves to the same absolute value it always did (here: `git init
--separate-git-dir=<external>`, so the working tree can move freely without
moving the actual git directory). Confirmed a no-op: no relink offered, no
new row — the natural key genuinely has not changed. This is what
distinguishes it from example 1's Clone-B case, where the common-dir value
itself changed.

```
$ wip init
{"repo":"01KYRN0T1B4Z2A2XE162FYJSE5","repoCreated":true,"remoteUrl":"github.com/acme/detached","clone":"01KYRN0T1B4Z2A2XE162FYJSE6","cloneLabel":"detached","worktree":"01KYRN0T1B4Z2A2XE162FYJSE7"}

$ mv detached detached-renamed && cd detached-renamed

$ git rev-parse --path-format=absolute --git-common-dir   # unchanged
/private/tmp/wip-examples-work3/external-git

$ wip doctor --json   # known=true — no relink offer, because nothing moved
{"known":true,"clone":"01KYRN0T1B4Z2A2XE162FYJSE6"}
```

A second `wip init` at the renamed location refuses as
`validation.already-initialized`, not as a new clone — confirming a new row
was never even tempting.

## Example 4 — a repo gaining a remote after local-only init

`wip init` on a repo with no remotes at all (Repo row, `natural_key = NULL`);
`git remote add origin <url>` added afterward; the next verb run adopts the
key in place — same Repo ULID, newly-populated natural key.

```
$ wip init   # no remotes at all
{"repo":"01KYRN0T3TAYB8A89EMHPQB3EZ","repoCreated":true,"clone":"01KYRN0T3TAYB8A89EMHPQB3F0","cloneLabel":"local-only","worktree":"01KYRN0T3TAYB8A89EMHPQB3F1"}

$ git remote add origin git@github.com:acme/local-only.git

$ wip status --json   # same Repo ULID, now-populated remote
{"hostWide":false,"repo":{"id":"01KYRN0T3TAYB8A89EMHPQB3EZ","header":"acme/local-only","clones":[{"id":"01KYRN0T3TAYB8A89EMHPQB3F0","label":"local-only","current":true}]}}
```

`01KYRN0T3TAYB8A89EMHPQB3EZ` is the same Repo identity before and after —
adoption updated the natural key in place (MODEL §5.3, "acquired, not
assigned"), never minting a second Repo.

## Example 5 — a fork: different origin, different Repo

Two clones of genuinely different remotes (a fork and its upstream, neither
using `--identity-remote`) resolve to two distinct Repo rows — the
multi-remote rule's *default* behavior requiring no override.

```
$ wip init   # upstream
{"repo":"01KYRN0T76JJ8G6RXHZ6Q8X55K","repoCreated":true,"remoteUrl":"github.com/acme/widget","clone":"01KYRN0T76JJ8G6RXHZ6Q8X55M","cloneLabel":"widget-upstream","worktree":"01KYRN0T76JJ8G6RXHZ6Q8X55N"}

$ wip init   # fork, no --identity-remote needed
{"repo":"01KYRN0T905YDAD72BJTZZE9C2","repoCreated":true,"remoteUrl":"github.com/someone/widget-fork","clone":"01KYRN0T905YDAD72BJTZZE9C3","cloneLabel":"widget-fork","worktree":"01KYRN0T905YDAD72BJTZZE9C4"}

$ wip status --json   # host-wide, from neither clone
{"hostWide":true,"repos":[
  {"id":"01KYRN0T76JJ8G6RXHZ6Q8X55K","header":"acme/widget","clones":[{"id":"01KYRN0T76JJ8G6RXHZ6Q8X55M","label":"widget-upstream","current":false}]},
  {"id":"01KYRN0T905YDAD72BJTZZE9C2","header":"someone/widget-fork","clones":[{"id":"01KYRN0T905YDAD72BJTZZE9C3","label":"widget-fork","current":false}]}
]}
```

Two distinct Repo ULIDs, two distinct normalized remotes
(`acme/widget` vs. `someone/widget-fork`) — the override documented in the
Brief exists for the case where a fork's own `origin` should key it against
its *upstream* instead; this example demonstrates the unoverridden default,
which is what makes a fork safe without one.

## Example 6 — an unknown clone: hard-failure path and doctor's offer

Two sub-cases from the Brief's "Move detection" section.

**6a — remote known, common-dir unknown.** `wip doctor` reports both the
relink and new-clone offers by name; the accepted new-clone path is `wip
init` succeeding normally.

```
$ wip doctor
this clone is unknown, but its remote matches known repo 01KYRMZT2ZEHTH0T13QVXN8J2E
  relink: wip clone relink <clone-locator>   (this clone moved)
  new clone: wip init                        (a separate clone of the same repo)
  existing clones of this repo:
    widget-known         01KYRMZT2ZEHTH0T13QVXN8J2F

$ wip init
attached the main worktree as clone "widget-new-clone" (repo 01KYRMZT2ZEHTH0T13QVXN8J2E)
```

**6b — remote also unknown.** Any verb needing a resolved Clone hard-fails
per MODEL §11, directing to `wip init`, with no relink offer attempted.

```
$ wip doctor --json
{"error":{"code":"refusal.unknown-clone","message":"refused — this clone is unknown to wip; run `wip init` here first"}}
$ echo $?
3
```

Both captured verbatim from `TestWorkedExample6_UnknownClone`'s `t.Logf`
output — the first live exercise of `guards`'s eventual unknown-clone check
foundation.

## Example 7 — cross-repo Batch: four Matters, two Repos, one clone (scoped)

Two Repo rows and four Matter rows (two per Repo) seeded through the store's
data-access layer directly, as test fixtures — `write-surface` (the
Matter-birth verbs) does not exist yet, which is this Stage's stated posture,
not a shortcut around a missing verb (`internal/cli/worked_examples_test.go`,
`TestWorkedExample7_CrossRepoBatch`). All four Matters join one Batch row.
Batch "keys at no tier" (MODEL §6, D39), so spanning two Repos is legal by
construction — and it is still an *execution* event requiring a Clone and
Worktree context (D56, `batch.*` alone carries clone+worktree with a null
repo): the dispatching clone here is `repo-a`'s.

```
$ wip status   # dispatched from a clone of repo-a; the batch spans repo-a + repo-b
acme/repo-a
  repo-a             Clone · current
```

This is the point of the example: `status`'s own repo-wide/current-clone-marked
scope (this Stage's step-08) is completely unaffected by the Batch's
cross-repo membership — it never mentions `repo-b`, because a tier-scoped
`status` doesn't render Matter or Batch content at all. `store.BatchMembers`
confirms all four Matters (two from each Repo) are live members of the one
Batch, directly against the store. Run and dispatch mechanics for this same
scenario are `appendix-orchestration.md` item 7, Phase 2 — not attempted here.
