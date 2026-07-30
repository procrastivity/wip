# `agent-path` — decisions this Matter made

The design of record is `workplans/agent-path.md` (external, `wip-reboot`).
`agent-path` earns no Brief of its own — Steps only (register). This file
records what building its eight Steps forced, and — unusually for this
register — how much of that work upstream Matters had already done, because
this Matter's own workplan existed as written text before any of them
opened.

Status: **all eight Steps — 01 through 08 — locally complete.** The seal
condition — an agent-shaped session can intake→plan→work→gate through verbs
only; every prose write visible as an event — is discharged directly by
`TestAgentPath_AcceptanceSession_IntakeThroughGate`
(`internal/cli/agent_path_e2e_test.go`). The `reviewed-local` gate is the
user's to close.

## What this Matter builds on: four upstream Matters read its workplan first

HANDOFF §1.6 accepts early-planning risk; this Matter is the register's
clearest instance of the opposite direction — a *later*-numbered Matter's
already-authored resolution reaching backward into earlier Builders' work,
because every workplan in this dogfood was written up front (HANDOFF §1: "no
just-in-time workplans"). By the time `agent-path` itself opened, four
Matters had already read `workplans/agent-path.md` and implemented or
confirmed the pieces it specifies:

- **Steps 01–02 (per-verb argument shapes; `--file` mechanics) — implemented
  by `write-surface`.** Its `content-prose` Stage explicitly defers argument
  shape to this Matter's own resolution; since that resolution already
  existed as text, `write-surface` implemented it verbatim
  (`internal/verbs/content`) rather than build a placeholder. See
  `docs/write-surface/decisions.md`, "Content verb argument shapes:
  `agent-path`'s resolution, implemented here" for the full account. Verified
  here, not re-implemented: `wip brief`/`wip workplan`/`wip body` take stdin
  by default or `--file <path>`, no positional prose argument; `wip finding
  add <locator> [text]` takes a trailing positional by default, falling back
  to stdin/`--file`; every verb declares `plumbing` (D53); `--file` reads the
  file's full contents exactly once and never touches, renames, or deletes
  it (`TestContent_FileFlag_SameSinglewriterPath`,
  `internal/cli/writesurface_content_test.go`, plus this Matter's own
  `TestAgentPath_ScratchFileConsumption_LiveDispatch` against a *live*
  dispatch's real scratch dir rather than a synthetic fixture file).
- **Step 03 (scratch-dir handoff mechanic) — built by `render-scratch`.**
  `wip refresh`'s dual-purpose dispatch-open/re-render path
  (`internal/render`, `internal/verbs/refresh`) already prints/returns the
  dispatch-id and scratch-dir path in both human and `--json` mode
  (`refreshPayload{Dispatch, ScratchDir, ...}`) — the mechanism this Matter's
  step-03 needed to exist before it could define the consuming half. This
  Matter's own contribution is confirming that mechanic is what a launching
  process reads and hands to a spawned agent at spawn time — an environment
  variable or an initial-context line, harness-specific and out of scope for
  P1's roleless posture (no such launcher is built here, or anywhere in P1;
  HANDOFF §1.1). `TestAgentPath_ScratchFileConsumption_LiveDispatch` and
  `TestAgentPath_AcceptanceSession_IntakeThroughGate` both open a real
  dispatch via `wip refresh` first and read its dispatch-id/scratch-dir back
  from the command's own output, exactly as a launcher would, rather than
  composing either value.
- **Step 04(a) (the 0444 message's harness-guidance text, wired verbatim) —
  built by `vocabulary` and `manifest-install` together.** `vocabulary`
  step-08/step-13 drafted and seeded the harness-guidance paragraph at
  `assets/agent-write-guidance.md` (the `(D45)` citation dropped per its own
  flagged snag); `manifest-install` step-07 projects that asset verbatim into
  the generated Claude Code `SKILL.md`
  (`internal/harness/claudecode.Generate`), confirmed byte-identical by
  `TestGenerate_ProjectsHandAuthoredTextVerbatim` in
  `internal/harness/claudecode/claudecode_test.go`. Nothing in this Matter
  re-verifies that wiring beyond reading it — re-testing an already-tested
  fact from a second Matter would only duplicate coverage without adding
  confidence.
- **Step 04(b) (the honest limit on wiring the raw OS `EACCES`) — nothing to
  build.** `render-scratch` step-04 writes every file under
  `.wip/generated/` `0444` immediately after render; the permission-denied
  error an agent's own file-editing tool receives on a direct write attempt
  originates entirely inside that tool's own process, outside every wip
  process, with no call site for wip to intercept. This Matter's entire
  contribution to that message's ergonomics is the upstream priming in step
  04(a) — there is no runtime-interception code to write, because none is
  possible, and none is written here.
- **Step 05 (the D40 boundary) — confirmed, not built, by `render-scratch`
  step-10; a companion negative test added here.** `render-scratch` already
  confirmed no code path reads `.wip/work/` as state on its own initiative.
  This Matter adds the test that exercises it end-to-end against a live
  dispatch rather than trusting the confirmation in prose alone:
  `TestAgentPath_D40Boundary_UnconsumedScratchFileHasNoEffect` opens a real
  dispatch, drops a file in its scratch dir, never passes it to any verb,
  and asserts a second `wip refresh` produces no content event and the
  matter carries no content of any kind — the companion to step-08's
  positive case below.

## What this Matter actually had left to build: the tests steps 06–08 asked for

- **Step 06 (events-for-free across every shape) — cross-shape refusal was
  the one case not yet exercised.** `write-surface`'s own
  `TestContent_CreateOnceVerbs_OneEventEachKindDiscrimination` already
  asserts one event per shape per verb and that a *same-shape* repeat is
  refused with no second event. The workplan's own step-06 text asks
  specifically for the cross-shape case — "a create-once verb's second call
  via a *different* shape than its first ... is still refused" — which is
  what could, in principle, slip through a naive implementation that keyed
  refusal off the shape rather than off the store. This Matter's
  `TestAgentPath_CreateOnce_CrossShapeRefusal` writes each of
  brief/workplan/body once via stdin, then attempts a second write via
  `--file`, and confirms the refusal, the zero-events-appended property, and
  that the original stdin content is untouched.
- **Step 07 (the acceptance test) — the seal condition's direct discharge.**
  `TestAgentPath_AcceptanceSession_IntakeThroughGate` scripts the exact
  sequence the workplan specifies: spawn (`wip refresh` opens a dispatch),
  intake (`wip matter create`), plan (`wip workplan` via stdin), work (`wip
  step create` directly under the Matter per D2, `wip start` — cascading the
  Matter to In Progress — two `wip finding add` calls via the positional
  shape, `wip finish` on the Step then the Matter), gate (`wip gate close
  reviewed-local`), stand-down (`wip dispatch close`, asserted `reason =
  completed` against the session's own dispatch-id). It asserts the Matter's
  full event sequence by type, in order
  (`matter.created, content.created, matter.started, matter.finished,
  gate.closed`), the Step's full sequence likewise (including both
  `content.appended` findings events with `payload.kind = findings`), that
  `wip status --json` reports the Matter `locallyComplete: true` and
  `sealed: true` once its one gate closes (D13's predicate, computed by
  `read-surface`, observed here as evidence rather than re-derived), and
  that every event in the session carries `actor: human` (see "the raised
  open call," below).
- **Step 08 (real scratch-file consumption from a live dispatch).**
  `TestAgentPath_ScratchFileConsumption_LiveDispatch` runs a real `wip
  refresh`, writes a draft file into the dispatch's real
  `.wip/work/<dispatch-id>/` directory (confirming the scratch dir really is
  namespaced by its own dispatch-id, not a fixed path), consumes it via
  `--file` on `wip brief`, and confirms exactly one `content.created`, the
  correct content, and that the source file is left exactly as written
  afterward.

## The raised, not resolved, open call: what actor an agent-driven verb stamps

The workplan raises this deliberately without resolving it: P1's `actor`
vocabulary is `human` alone (`role:<name>` waits for Phase 2's roles,
`system:<source>` has no P1 emitter — `schema`'s Brief §A), so every verb
invoked through the CLI — whether a human types it or an agent's harness
issues it — stamps `human`. No `--actor` flag is invented to paper over the
gap; an unverified self-declared actor would be worse than a known-coarse
one. `TestAgentPath_AcceptanceSession_IntakeThroughGate` records what the
acceptance test actually stamps (every event in the session, `actor:
human`) as the concrete evidence this call asks for, flagged here again for
`orchestration`, which is where a dispatched agent becomes a thing wip
*spawned* rather than a thing that ran a command.

## Everything else

Every other property the workplan's Steps ask for — that the four content
verbs remain `plumbing` under every shape (D53, confirmed by
`write-surface`'s own registration, unchanged here); that no code path
anywhere composes a dispatch-id or scratch-dir path itself rather than
reading it from `wip refresh`'s own output — is exercised as documented
above, with no divergence from any of the four upstream workplans this
Matter reads.
