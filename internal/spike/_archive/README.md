# Archived spikes

`store-fork` step-06. The losing spike is preserved here, not deleted — the
Matter's seal condition says so in those words.

## `tables/` — Spike B, tables + audit-log

Written as `internal/spike/tables/` and moved here unchanged when the fork
closed in favour of the event-sourced shape. Its own `report.md` and the file
paths quoted inside it refer to the original location; nothing else about it
was edited.

It was last live, buildable, and part of `./...` at commit **`10ace2f`**
(`feat(store-fork): spike B — tables + audit-log`).

**It no longer compiles against the current harness, on purpose.** After the
fork closed, the envelope gained `actor`, `causation` and `correlation` and
every verb gained an actor parameter (see `docs/store-fork/scenario.md`
§"Amendment"). Only the winning spike was carried forward; retrofitting a
shape the register has already rejected would be work spent on evidence
nobody will run. To get a tree where it builds and passes:

```sh
git worktree add /tmp/spike-b 10ace2f && cd /tmp/spike-b
go test ./internal/spike/tables/
```

## Why the underscore

The Go tool ignores any directory whose name begins with `_`, so this subtree
is invisible to `go build ./...`, `go test ./...`, and `golangci-lint run` —
archived code neither ships nor gates CI. It is still ordinary Go and can be
run when named explicitly:

```sh
go test ./internal/spike/_archive/tables/
```

That is the point of archiving rather than tagging: the losing spike stays
readable here and runnable at the commit above, so a future re-opening of D46
gets working evidence rather than a diff.

## What it is worth reading for

Three ideas outlived the shape and are carried into `schema` by the decision
record — the event taxonomy as a **table** rather than a Go constant (D56
checked in the schema, literally), the column-level constraints on the log,
and the rows-affected assertion on a guarded transition. Its
`TestNodeWritesCannotBypassTheLog` — fourteen bypass attempts executed as raw
SQL *around* its own API — is the best-argued test either spike produced, and
the technique transfers to whatever the store becomes.
