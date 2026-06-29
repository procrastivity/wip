# 0017 — Test harness: stay on the homegrown bash assert library; do not adopt Bats

- Status: accepted
- Date: 2026-06-28
- Source: test-suite-speedup initiative, Round 2 Lane docs (step-06); BRIEF.md; Round 1 step-01/step-02 + Round 2 step-05 measurements

## Context

The Round-2 framework question (Phase 4 / BRIEF) for the test-suite-speedup
initiative: should the suite migrate from the homegrown `test/helpers.sh` assert
harness to [Bats](https://github.com/bats-core/bats-core)?

The suite shape that question applies to: **42 `test/test-*.sh` files, ~8.0k
lines** of bash, run via `test/run` (the parallel runner that landed in step-01)
and `make check`. A migration to Bats would mean rewriting those ~8k lines into
Bats' `@test` idiom and adding the framework as a dependency everywhere the suite
runs. The bar, then, is concrete: a switch to Bats has to **buy** something the
homegrown harness cannot — measurably faster runs, a capability we need, or a
maintenance reduction that outweighs the migration — to be worth ~8k lines of
churn. This ADR frames that buy and records the answer.

## Decision

**Stay on the homegrown bash assert harness; do not adopt Bats.** Four
rationale points, each backed by a measurement the archives record (not memory):

1. **Parallelism was already won without a framework.** The suite is
   parallel-safe today — every test isolates via `mktemp` +
   `WIP_ROOT`/`WIP_NO_REGISTRY=1`, and none write into the repo — so step-01's
   `test/run` (`xargs -P`) captured the big lever, **~110s sequential → ~22s
   parallel (≈5×; step-02 notes a ~19s pass)**, with **zero** framework
   dependency. Bats would not add parallelism we don't already have.

2. **Startup/volume is the bottleneck, not the harness.** step-05's cost model
   found runtime is dominated by the **count of CLI invocations**: per-call cost
   lives in `bin/wip` / `bin/wip-plumbing` (sourcing `lib/wip/*`) at a ~14ms
   lib-source floor, ~250ms for a real plumbing run, and ~0.5–1.2s for a
   porcelain run — **not** the assert harness and **not** jq/yq (~3–5ms). A
   different test framework cannot touch that floor.

3. **`test/helpers.sh` is small and dependency-free.** ~239 lines of plain bash
   (`assert_eq` / `assert_file` / `assert_grep` / … plus the step-03 fixture
   builders), with no Bats and no other external dependency. CI and contributors
   need only bash already on the box; Bats would add a vendored or installed
   dependency to every environment that runs the suite.

4. **Bats' per-test subshell + TAP layer adds overhead for no measured win**, on
   top of the ~8k-line migration cost. Net: pure cost, no measured benefit.

**What WOULD change the call.** Reopen this decision if either of these becomes
a hard requirement:

- **Cross-test scheduling** — sharing or ordering state across files beyond the
  per-file independence that `xargs -P` already gives us.
- **Richer / structured reporting** — e.g. TAP or JUnit output that a CI
  dashboard consumes, which the homegrown `test_summary` cannot reasonably grow
  into.

Absent those, the homegrown harness stays.

## Consequences

- Contributors and CI need only bash — no Bats to install or vendor — and the
  suite stays editable with no framework idioms to learn.
- Future speed work targets the lever step-05 identified — the **count of CLI
  invocations** — not the assert harness, which is not on the critical path.
- The door is left open: the two "what would change the call" triggers
  (cross-test scheduling, structured/aggregated CI reporting) are the explicit
  re-open criteria for this decision.
- No test or source behavior changes from this ADR; it records a decision the
  BRIEF already locked under "Confirmed decisions (do not relitigate)".
