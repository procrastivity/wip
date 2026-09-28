# M1 Step 2: semantic operation contract

Status: implemented by `internal/operation`. This record defines the
transport-neutral boundary that M1 Step 3's in-process dispatcher may adopt.
It does not define a wire encoding, daemon, socket, journal, new event envelope,
or storage owner.

## Boundary

An operation is identified by the pair `(name, version)`, rendered only for
diagnostics as `name@vN`. Names use lowercase dot-separated semantic nouns and
verbs. The first definition is `matter.create@v1`; Step 8 adds the narrowly
scoped `step.create@v1` provisional-birth operation below. The numeric version changes
only for an incompatible semantic input, output, or behavior contract. M2 owns
how this pair is negotiated or encoded.

`operation.Request` carries only:

- the operation ID;
- an explicit actor token (`human`, `role:<name>`, or `system:<source>`);
- already-resolved Repo/Clone/Worktree identities required by metadata;
- an exact claim ID/epoch when metadata requires one;
- the operation's typed semantic input; and
- named staged-blob references.

The transport adapter, not the handler, owns Cobra parsing, current-directory
and environment discovery, terminal streams, and presentation. Arbitrary file
paths and open streams are not blob inputs. `context.Context` remains the
in-process cancellation argument to `Handler`; it is not semantic request
data.

`operation.Result` has one stable disposition:

| code | meaning | permitted problem-code namespace |
|---|---|---|
| `result.succeeded` | typed semantic output | none |
| `result.rejected` | malformed or invalid semantic request | `operation.*`, `validation.*`, `not-found.*` |
| `result.refused` | a model guard refused valid intent | `refusal.*` |
| `result.failed` | unexpected execution/storage failure | `internal.*` |

Problem messages remain presentation text; callers branch on the stable code.
M2's unavailable and `outcome-unknown` protocol outcomes are deliberately not
semantic result codes.

## Static metadata

Every `Definition` supplies all of these dimensions:

1. read or mutation;
2. one delivery class (`authority`, `claim`, `provisional`, `capture`, or
   `environment`; reads explicitly use `none`);
3. required tier-context identities;
4. complete guard footprint;
5. complete write footprint;
6. named staged-blob inputs;
7. exact-claim requirement; and
8. external effects outside authority events/projections.

For slice-valued dimensions, `nil` means “classification omitted” and is
invalid. An allocated empty slice means “classified: none.” This distinction
makes an incomplete definition fail at construction and in focused tests.

`matter.create@v1` is the canonical definition:

| dimension | value |
|---|---|
| access | `mutation` |
| delivery | `provisional` |
| context | Repo |
| guards | `repo.matter-locators` |
| writes | `newborn-matter-subtree` |
| blobs | none |
| exact claim | none (provisional return establishes its implicit claim) |
| external effects | none |

The delivery classification is D120/D127's future class. This step does not
change the current in-process write path, locator behavior, event/projection
shape, or ownership in `internal/writesurface` and `internal/store`.

### Step 8: `step.create@v1`

`step.create@v1` creates one Planned Step directly under a Matter born by
`matter.create@v1` in the same Environment. Its typed input is
`StepCreateInput{ParentID, Title}` and its canonical input map is exactly
`{parent_id, title}`. The parent is a canonical Matter ULID in the requested
Repo. The caller supplies the Matter's implicit birth claim at epoch 1 and
sets both causation and correlation to the Matter birth command. The authority
requires that birth command to have a terminal-success receipt in the same
Environment and Repo, with a lower Environment sequence; it will not infer a
claim from fixture or legacy state.

The definition is a mutation with provisional delivery, Repo context, no blob
inputs, no external effects, and these complete footprints:

| dimension | value |
|---|---|
| guards | `repo.matter-locators`, `matter.implicit-birth-claim`, `step.parent`, `matter.step-locators`, `step.sibling-sort-key` |
| writes | `newborn-step` |
| claim | implicit Matter birth claim `{id: parent_id, epoch: 1}` |

Within the authority transaction, the parent must exist in the command Repo
and the claim/causation/correlation checks above must hold. The authority
assigns the Step ULID, the next sibling locator `step-%02d` (starting at
`step-01`), and the next sort key (maximum sibling key plus 1000, or 1000 for
the first Step). The resulting state is `planned`. Successful output is
exactly `{id, parent_id, matter_id, locator, title, sort_key, state}`; both
parent fields equal the input parent, and the remaining assigned fields equal
the authority projection. The operation emits exactly one `step.created`
event with the Step ULID as `subject_id` and payload
`{title, locator, parent, sort_key}`. Its terminal receipt accepts exactly
that one-event range. Authority event, Step projection, and terminal receipt
commit atomically.

Successful `matter.create@v1` also establishes the Matter's immutable epoch-1
implicit birth claim in the same transaction as its event, projection, and
receipt. This Step 8 claim is a direct birth dependency, not M3
`claim.acquire`, and authorizes no other operation. A Step command cannot be
admitted or folded if that exact Matter birth is absent, unsuccessful, from a
different Environment/Repo, or not earlier in the Environment sequence.

The connected command path MUST journal the immutable command and pass through
the shared D120/D128 command-start coordinator. It returns eligible pending
work and installs the authority tail before guard evaluation, keeps the
per-domain lane through the terminal authority outcome, and installs the
receipt/event tail/overlay atomically. Exact replay resolves from the original
identity and receipt without another Step event; conflicting intent under the
same command ID is refused without changing or disclosing the original.

## Mechanical coupling rule

Definition construction recursively checks input and output DTOs. DTOs may use
plain exported data owned by `internal/operation`; they may not contain types
from Cobra, `internal/iostreams`, `internal/store`, `database/sql`, `io`,
`context`, or another runtime/transport package. Functions, interfaces,
channels, unsafe pointers, unexported fields, and raw input bytes are rejected.
Large input bytes must be represented by declared staged blobs.

This is intentionally narrower than “anything Go can serialize.” It prevents
an in-process convenience from becoming a semantic argument that a future
protocol cannot honestly represent.

## Adoption and deferrals

Step 3 may register `operation.Handler` values and validate requests/results
against `Definition`; it must not add transport or move storage ownership.
Step 4 may route `matter.create` through that dispatcher and retain the current
CLI renderers byte for byte.

The Step 1 census remains the complete behavior inventory. Operations enter the
code catalogue as their DTOs and exact footprints are made concrete; this step
does not guess unresolved composite seal/render, tracker credential, Backlog
finding, seal-time Batch, or local-tool classifications.

Command IDs, canonical bytes and hashes, domain/authority epoch,
Environment sequence, acted time, correlation, authenticated claim proofs,
blob transfer, pagination, framing, compatibility negotiation, availability,
and outcome uncertainty remain M2 protocol-design work.
