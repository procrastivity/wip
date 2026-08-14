# Verification doctrine decisions

2026-08-11. This record closes NEXT-0 loose end L3. It defines the
`verified` gate before the dogfood Repo declares it. The local labels V1-V5
preserve the decision order without allocating D-numbers early. Each label
is one operative decision.

Settled inputs remain in force. Gate declarations are static project
configuration (D4). Gate order cannot move to a finer scale (D12). Gate
configuration activates outer-loop roles (D14).

A gate closes at the scale
of its subject (D62). Human-owned gates can park work between Dispatches
(D60). This record does not apply new gates to sealed Matters.

| Local | Operative decision |
|---|---|
| V1 | **A verification pass has three ordered parts:** adversarial review of the complete Matter contract, independent rerun of every stated check from a reproducible clean test state, and a durable evidence artifact. A pass confirms every criterion and check at one reviewed revision. |
| V2 | **A negative verdict leaves `verified` open and follows the D60 parking shape:** the Verifier records actionable discrepancies and closes its role. The Orchestrator assigns correction to a new Builder, then a new Verifier repeats the complete pass against the new revision. |
| V3 | **Declare `verified` at Matter scale:** one verdict covers the complete Matter contract and one reviewed revision. `verified` and `reviewed-local` remain independent Matter-scale predicates with different owners. |
| V4 | **Use one uniform trial declaration for all Matters in this Repo:** implementation, design-record, and evidence work get no exemption. The first prose verification that finds the pass ceremonial creates a backlog entry to decide whether D4 needs explicit per-kind policy. Uniform coverage stays in force until that decision seals. |
| V5 | **Verifier owns the `verified` close:** one `role.spawned` to one explicit `role.closed` brackets a complete pass. A pass emits `gate.closed` only after its evidence exists. A failure emits no gate event. A reaped Verifier did not complete a pass. |

## V1: pass and verdict

The Matter contract is the in-force Workplan, Brief, findings, Steps, and
explicit done criteria. The Verifier reviews that contract adversarially.
The Verifier looks for an unmet criterion, a contradiction between the
record and the change, an unsupported claim, and an effect outside the
declared scope.

The Verifier then reruns every check that the Matter states. The rerun starts
from a reproducible clean test state. The Verifier does not use the Builder's
reported result as evidence that a check passed. When a stated check cannot
run reproducibly, the verdict is negative.

The evidence artifact identifies:

- the Matter and reviewed revision
- every criterion and its observed result
- the reviewed change or record
- each command and its result
- each discrepancy and its resolution
- the final verdict.

A pass quantifies over the full list. One unmet criterion, failed check,
unreproducible check, unresolved discrepancy, or missing evidence artifact
makes the verdict negative. Verification establishes conformance to the
stated contract at the reviewed revision. Verification does not claim that
no defect exists outside that contract.

## V2: failure and re-engagement

A negative Verifier writes the evidence artifact with a negative verdict and
actionable discrepancies. The Verifier does not close `verified`. The Matter
remains Done but unsealed, and the Run remains standing by. The Verifier
closes its role instance with reason `completed` because the verification
pass itself completed and returned a negative result.

Failure creates no new node state, gate event, automatic retry, or gate
reopen. The Orchestrator starts a later correction pass. It opens a fresh
claim Dispatch and assigns the discrepancies to a new Builder. The failed
Verifier does not repair work that it judged. After correction, a new
Verifier repeats V1 against the new revision. It does not verify only the
previous failures.

The loop ends when a Verifier passes and closes
`verified`, or when a human cancels the Matter.

## V3: Matter scale

One verification pass judges one complete Matter contract at one revision.
Matter scale gives that judgment one subject and one evidence artifact.

Step scale would verify intermediate revisions. Later sibling work could
invalidate earlier evidence. Step scale would also require repeated passes
before the complete Matter existed. Stage scale would make verification
depend on optional presentation shape. A flat Matter would get one verdict,
while a shaped Matter would get one verdict per Stage.

Matter scale avoids both problems and satisfies D62 directly. It is
compatible with `reviewed-local`, which is also at Matter scale. The gates
remain independent. The Verifier closes `verified`. A human closes
`reviewed-local`.

## V4: uniform trial coverage

The store does not classify Matters as implementation, design-record, or
evidence work. Those labels describe the work but cannot select a static D4
gate policy. One Matter-scale declaration therefore applies to every new
Matter in the Repo.

The trial has one explicit revisit trigger. It occurs after the first
completed verification of a prose Matter. When the pass adds no independent
assurance, the observation creates a backlog entry. The backlog entry decides whether D4
needs a stored per-kind policy amendment. The observation does not exempt
the Matter that produced it. Convention cannot remove `verified`, and
uniform coverage stays operative until the follow-up decision seals.

## V5: role and event mechanics

Declaring `verified` activates Verifier through D14. For each pass, the
Orchestrator or a manual driver opens or continues a claim Dispatch and runs:

```text
wip role spawn verifier
```

The command emits `role.spawned`. The event subject is the new role instance.
Its payload names the current Dispatch and `verifier`. Its actor is the
spawning caller. Only one open Verifier instance can occupy one Dispatch.

The open role instance brackets the complete V1 pass. Verifier writes that
enter the event log use the `role:verifier` claim. The write path accepts the
claim only while an open Verifier spawn backs it.

For a passing verdict, the Verifier writes the evidence first and then runs:

```text
wip --as-role verifier gate close verified <matter>
wip role close verifier
```

The first command emits `gate.closed`. Its actor is `role:verifier`, its
subject is the Matter, and its payload names `verified` and Matter scale.
The second command emits `role.closed` against the role instance. Its actor
is `role:verifier`, and its reason is `completed`.

For a negative verdict, the Verifier writes the evidence and runs only the
role close. No `gate.closed` event exists. When the Dispatch closes before
the explicit role close, the cascade closes the role with reason `reaped`.
Reaping means that the pass did not complete and gives no gate-close
authority. A human cannot close `verified`, and Verifier cannot close
`reviewed-local`.

## Worked traces

The traces use three work categories to test the uniform rule. The categories
do not add stored Matter kinds or conditional configuration.

### Trace A: implementation Matter

Subject: a Matter that changes Go source and tests.

1. The Verifier maps each Workplan criterion to the diff and resulting
   behavior. The review checks error paths, event attribution, migrations,
   and effects outside the stated scope when those areas are applicable.
2. From a clean worktree state, the Verifier independently runs every command
   in the Matter contract. Typical commands include focused tests,
   `go test ./...`, `go vet ./...`, formatting checks, and
   `git diff --check`.
3. The evidence records the revision, inspected files, criterion results,
   commands, outputs, discrepancies, and verdict.

When all criteria and checks pass, the Verifier writes the evidence, closes
`verified`, and closes the role. A test failure or contract mismatch produces
a negative artifact and no gate close. This pass adds assurance by separating
the Builder's implementation claims from the Verifier's observations.

### Trace B: design-record Matter

Subject: a Matter whose principal output is a decision record.

1. The Verifier maps every requested decision to one operative line. The
   review checks settled inputs, rejected alternatives, internal
   contradictions, downstream obligations, and the stated seal condition.
2. The Verifier independently reruns each stated mechanical check. Examples
   include reference searches, document lint, link checks, and any focused
   tests used to prove a model claim.
3. The evidence records the reviewed revision, the decision-to-requirement
   mapping, commands, discrepancies, and verdict.

Prose does not make the pass optional. An omitted case, ambiguous operative
line, stale reference, or failed lint check gives a negative verdict. If the
first completed prose verification adds no assurance beyond the existing
review record, V4 creates the follow-up backlog entry after the pass. The
current Matter still requires its verdict.

### Trace C: evidence Matter

Subject: a Matter whose output is a trace, transcript, or fidelity audit.

1. The Verifier maps every claimed observation to the raw event sequence,
   stored state, test fixture, or command output that supports it. The review
   checks omissions, ordering, event actors, subjects, payloads, and derived
   read claims when those fields are in scope.
2. The Verifier independently regenerates or replays every stated check from
   a clean fixture. A copied transcript without a reproducible source is an
   unmet criterion.
3. The verification artifact identifies the evidence under review and
   records the independent replay, discrepancies, and verdict. It is
   meta-evidence, not a duplicate of the original trace.

A clean replay and complete claim mapping permit the gate close. A fidelity
gap, stale capture, or unreproducible trace leaves `verified` open. Evidence
work therefore receives the same predicate while its contract supplies the
appropriate observations.

## Event sequences

The passing sequence is:

| Order | Event | Actor | Subject | Meaning |
|---|---|---|---|---|
| 1 | `role.spawned` | spawner | Verifier instance | A complete pass begins in the current Dispatch. |
| 2 | `gate.closed` | `role:verifier` | Matter | Evidence exists and the complete contract passed at the reviewed revision. |
| 3 | `role.closed` | `role:verifier` | Verifier instance | The pass bracket ends with reason `completed`. |

The negative sequence omits step 2. The durable artifact carries the negative
verdict and discrepancies. A premature Dispatch close replaces the explicit
role close with a cascaded `role.closed` whose reason is `reaped`. Neither
negative sequence closes `verified`.

## Dogfood configuration action

After the user ratifies V1-V5, the configuration change is mechanical:

```text
wip gate declare verified --scale matter
```

The declaration is project configuration and emits no domain event. It
activates Verifier and applies to unsealed work under the normal gate rules.
Sealed Matters remain untouched. The command is the user's action on this
record, not part of the verification-model Matter.

## Phase-close append

Append V1-V5 as the next free D-numbers during the applicable register
append. Update the gate doctrine to name the three-part pass, Matter-scale
coverage, D60 failure loop, uniform trial, and Verifier event bracket. Close
NEXT-0 loose end L3 when the user either declares `verified` or explicitly
declines the dogfood configuration change.
