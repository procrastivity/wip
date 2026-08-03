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
remains ordinary sequential execution under D31. The scheduler can also pick
Ready nodes in stable presentation order when it needs a deterministic subset;
that choice does not turn the sort key into a dependency.

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
