## When to reach for wip

Reach for `wip` whenever you are tracking real work — a Matter, a Stage, a
Step — rather than narrating it in chat. Use it to see what is unblocked
(`wip next`), to check where things stand (`wip status`), and to record a
write (create, amend, gate) through a verb rather than by editing a file
directly: every write through a verb becomes an event for free, and every
file under `.wip/generated/` is a snapshot from the last `wip refresh`,
not live state. If a command below refuses with a permission-denied
message pointing at `.wip/generated/`, that is the snapshot saying so —
the fix is a verb call followed by `wip refresh`, never a file edit.

## Generated files are a snapshot

Files under `.wip/generated/` are rewritten only by `wip refresh`. They
lag every write until the next refresh. A bare `wip refresh` skips sealed
Matters; those need `wip refresh <locator>`. Live facts come from
`wip status`, `wip next`, and `wip session`. A session's first look at
the work is `wip refresh` then `wip status`. If a generated file
disagrees with those verbs, run `wip refresh` (or `wip refresh <locator>`
if sealed) and re-read.

## How much shape a Matter earns

Shape is earned, never default. A small fix worked start-to-finish in one
sitting needs no `wip workplan` prose — its step titles *are* the plan,
and decisions made along the way land as `wip finding add` on the Matter.
Write `wip workplan` prose when the Matter will outlive the session that
plans it: someone (you, later, or another session) must pick it up cold,
so record intent, inputs already in place, and the seal condition. A
`wip brief` is rarer still — only when other Matters will re-read this
one's decisions as *their* input. When in doubt, start with steps and
findings; prose can be added when it is earned, but a Matter is never
blocked on prose nobody needs. The working cadence within any shape is
the same: `wip start` → `wip next --set` → do the work → `wip finding
add` for each decision the diff cannot show → `wip finish` → `wip next`
to see what's next, then `wip next --set` it or `wip next --clear` if it
is genuinely undecided — surface the choice, never invent one.

## Devin invocation

In Devin, this skill is exposed as `/wip` for both user and model invocation.
