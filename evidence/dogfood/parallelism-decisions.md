# Parallelism decisions

Session G, 2026-08-03. This record closes the execution-ordering half of D51.
The operative lines below are ready for the next free numbers in MODEL §12
during Session K. The local labels G1–G5 preserve their order without
allocating D-numbers early. Each label is one operative decision.

| Local | Operative decision |
|---|---|
| G1 | **Ready siblings are eligible for parallel dispatch:** no `blocked-by` edge means no execution ordering; sibling sort order remains presentation-only. |
| G2 | **`blocked-by` is the only plan-ordering mechanism:** Stages do not declare sequential or parallel modes, and no separate “parallel” edge exists. |
| G3 | **One scheduler applies the same rule at every plan-node scale and across a Batch:** it selects Ready work from the in-force dependency graph. |
| G4 | **The concurrency cap belongs to the Run:** all work that the Run has engaged counts against one cap; Matters and Stages add no nested caps. |
| G5 | **“Lane” is not a model noun:** a dependency track is descriptive prose for a chain in the graph, never stored identity or scheduling input. |

## Rationale and consequences

### G1 — absence of an edge permits parallel dispatch

D64 already gives one complete readiness predicate: a Planned node is Ready
when every in-force `blocked-by` edge is satisfied at locally complete. Once
more than one node is Ready, the scheduler may engage those nodes together,
up to the Run cap. An absent edge carries no hidden sequence.

“Eligible” does not mean “must run concurrently.” The cap, available agents,
claims, and configured D30 handling can reduce the selected set. A cap of 1
remains ordinary sequential execution under D31. When it needs a subset, the
scheduler applies a selection policy that does not read sibling sort order;
the `scheduler` Matter owns that policy.

This closes D51 without changing its first half: sibling order remains a sort
key for display only. Reordering siblings can never change the Ready set or
the legal concurrent set.

### G2 — the graph is the plan

A Stage-level sequential/parallel property would create an ordering source
beside `blocked-by`. It would also make the meaning of the same edge depend on
its container and require precedence rules for cross-container edges. An
explicit “parallel” marker has the inverse problem: every independent pair
would need an affirmation, and adding a new sibling would silently require a
quadratic set of declarations or another implicit rule.

The dependency graph already expresses sequence, fan-out, and join. Keeping
one mechanism makes D24 structural inside a Matter as well as across a Batch:
grouping schedules attention, while edges order work.

This decision deliberately accepts that old or under-analysed plans can expose
several Ready siblings. The safe response is a conservative Run cap, not a
second plan language. A user who requires sequence records the dependency.

### G3 — one scheduler, one readiness rule

The scheduler consumes one graph even though dispatch brackets remain at the
Matter and Worktree boundary. For each active Matter, it applies D64 to the
Matter's Stages and Steps; across the Batch, it applies the same predicate to
member Matters. The implementation can use separate queues or passes, but it
must not give those queues different ordering semantics.

This keeps intra-Matter fan-out and cross-Matter Batch dispatch in the
`scheduler` Matter. A future optimization may change selection strategy, but
not Ready or the meaning of an absent edge.

### G4 — one cap belongs to one Run

The Run is the execution bracket that owns a pass over a Batch. Its cap counts
all concurrently engaged work, including work inside one Matter and work
spread across Matters. A nested per-Matter or per-Stage cap would be a second
configuration dimension with surprising multiplication: a Run cap of 4 and a
Matter cap of 4 could mean four total workers or four per Matter.

One Run cap gives one answer. When four slots exist, the scheduler can fill at
most four across its whole graph. A cap of 1 remains D31's only sequential
mode. Fairness among Ready Matters or tracks is a selection-policy question;
it cannot exceed or partition the cap implicitly.

### G5 — dependency track replaces “lane” only in prose

The retired “lane” noun suggested a stored container or scheduling class. The
graph needs neither. When documentation must describe one mostly linear chain
among parallel work, it can call that chain a **dependency track**. A track
has no identity, lifecycle, membership, cap, or ordering effect beyond its
actual `blocked-by` edges.

A branch or join can make the description stop being a track. That is useful:
the prose follows the graph instead of forcing a general DAG into named lanes.

## Worked frontier traces

The traces use `→` for `blocked-by`: `A → B` means B is blocked by A. A
frontier contains Ready Planned nodes only. An engaged node is In Progress
and therefore no longer appears in the frontier. When more nodes are Ready
than the Run has slots, the scheduler selects a legal subset; the unselected
nodes stay Ready. Frontier presentation order never selects work.

### Case A — three independent Steps, then a join

Graph: `1 → 4`, `2 → 4`, `3 → 4`. Run cap: 3.

| Moment | Change | Ready frontier | Engaged |
|---|---|---|---|
| A0 | Matter starts | `1, 2, 3` | none |
| A1 | Scheduler fills three slots | none (`4` blocked) | `1, 2, 3` |
| A2 | `2` completes | none (`4` still blocked by `1, 3`) | `1, 3` |
| A3 | `1` completes | none (`4` still blocked by `3`) | `3` |
| A4 | `3` becomes locally complete | `4` | none |
| A5 | Scheduler engages `4` | none | `4` |
| A6 | `4` becomes locally complete | none | none |

The completion order of `1`, `2`, and `3` changes no legal frontier. Step 4
becomes Ready only after all three edges satisfy D29.

### Case B — two dependency tracks converge at a Stage end

Graph: `A1 → A2 → A3` and `B1 → B2 → B3`, all six Steps in one Stage. Run
cap: 2. The Stage lifecycle is In Progress during the trace.

| Moment | Change | Ready frontier | Engaged |
|---|---|---|---|
| B0 | Stage starts | `A1, B1` | none |
| B1 | Scheduler fills two slots | none | `A1, B1` |
| B2 | `A1` completes; its slot is refilled | none | `A2, B1` |
| B3 | `B1` completes; its slot is refilled | none | `A2, B2` |
| B4 | `B2` completes; its slot is refilled | none | `A2, B3` |
| B5 | `A2` completes; its slot is refilled | none | `A3, B3` |
| B6 | `B3` completes | none | `A3` |
| B7 | `A3` completes | none | none |

At B7 the Stage can finish because all children are Done. “Stage end” is a
lifecycle close, not another Ready node and not a hidden join edge. If later
work must wait for the Stage, it records a `blocked-by` edge to the Stage and
that edge satisfies when the Stage becomes locally complete.

### Case C — sequence, fan-out, join

Graph: `1 → 2`; `2 → 3`, `2 → 4`, `2 → 5`; and `3 → 6`, `4 → 6`, `5 → 6`.
Run cap: 3.

| Moment | Change | Ready frontier | Engaged |
|---|---|---|---|
| C0 | Matter starts | `1` | none |
| C1 | Scheduler engages `1` | none | `1` |
| C2 | `1` completes | `2` | none |
| C3 | Scheduler engages `2` | none | `2` |
| C4 | `2` becomes locally complete | `3, 4, 5` | none |
| C5 | Scheduler fills three slots | none (`6` blocked) | `3, 4, 5` |
| C6 | `4` completes | none (`6` still blocked by `3, 5`) | `3, 5` |
| C7 | `5` completes | none (`6` still blocked by `3`) | `3` |
| C8 | `3` becomes locally complete | `6` | none |
| C9 | Scheduler engages `6` | none | `6` |
| C10 | `6` becomes locally complete | none | none |

The graph expresses sequence, fan-out, and join without a Stage mode or a
parallel marker.

### Case D — four-Matter Batch, shared blocker, cap 2

Batch members are `A`, `B`, `C`, and `D`. Graph: `A → C` and `A → D`; B is
independent. Run cap: 2.

| Moment | Change | Ready frontier | Engaged |
|---|---|---|---|
| D0 | Run starts | `A, B` | none |
| D1 | Scheduler fills two slots | none (`C, D` blocked by A) | `A, B` |
| D2 | A becomes locally complete | `C, D` | `B` |
| D3 | The scheduler gives the free slot to C | `D` (waiting for capacity) | `B, C` |
| D4 | B becomes locally complete | `D` | `C` |
| D5 | The free slot takes D | none | `C, D` |
| D6 | C becomes locally complete | none | `D` |
| D7 | D becomes locally complete | none | none |

At D3, D is Ready rather than blocked. The Run cap alone keeps D out of the
engaged set. The graph also permits D to take the slot instead. Reordering C
and D changes only their display; it cannot choose between them, change
readiness, or permit more than two engagements.

## `wip next` parallel-frontier output

When an open Run has several Ready nodes, `next` shows the full parallel
frontier with the Run cap and available slots. It does not predict which
cap-limited subset the scheduler will engage, because `next` is a read and
presentation order cannot select work.

The ratified draft uses Case C at C4 with a Run cap of 2:

```text
$ wip next
release-docs · run-02             Run · open
  3 ready in parallel · cap 2 · 2 slots available
  docs-refresh · step-03          Step · planned
  docs-refresh · step-04          Step · planned
  docs-refresh · step-05          Step · planned
  order shown is presentation-only
```

When no slot is available, the same full frontier appears and the header says
`0 slots available`. With one Ready node, the existing singular
positioned-node output remains sufficient. With no open Run, the existing
no-cursor candidate output remains unchanged: it describes work that a user
can select, not a scheduler selection.

The scheduler owns the selection. `next` does not engage work, move the
cursor, reserve a slot, or promise that the first displayed node runs first.
A later call can show a different frontier after Run state changes; it cannot
change the frontier by reading it.
