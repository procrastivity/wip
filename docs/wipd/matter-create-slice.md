# M1 Step 4: `matter.create` vertical slice

Status: implemented. Only `wip plumbing matter create` crosses the in-process
`operation.Registry` boundary in this step. No other operation family is
registered or migrated.

## Adapter and semantic boundary

The existing `internal/verbs/matter` adapter still owns current-directory and
flag access, store opening, Repo discovery, error adaptation, and human/JSON
rendering. After resolving those ambient inputs, it constructs a
`matter.create@v1` request containing only actor, Repo identity, title, and
optional locator. The request contains no Cobra value, stream, environment
lookup, path, or store handle.

The registered handler is composed in that same adapter with the already-open
store. It invokes the unchanged `writesurface.CreateMatter` owner, maps the
returned node or error to `operation.Result`, and returns through dispatcher
result validation before the adapter renders. Thus the path is:

```text
Cobra parse / checkout discovery
  -> semantic Request
  -> Registry.Dispatch
  -> writesurface.CreateMatter
  -> semantic Result
  -> existing error and output renderers
```

This deliberate composition boundary keeps `internal/writesurface/birth.go` as
the only event decision/commit owner. `internal/verbs/matter/matter.go` remains
the only production caller of `writesurface.CreateMatter`; it has not gained a
second store opener or commit path. The handler appends the same single
`matter.created` event and reads the same resulting Node projection.

## Preserved behavior

The adapter preserves the previous output field order and exact line endings in
both human and JSON modes. Structured validation/refusal/internal errors retain
their code, message, renderer, and exit classification. An unexpected raw
execution error becomes `result.failed` while inside the semantic boundary and
is converted back to a raw adapter error, preserving the existing CLI's usage
exit classification until that broader legacy behavior is deliberately changed.

Focused tests pin success and failure bytes, exit codes, the complete
`matter.created` payload and relevant envelope fields, and every projected Node
field. A static ownership test rejects any second production caller of
`writesurface.CreateMatter`.

No daemon, socket, wire encoding, event envelope, domain type migration, remote
transport, external effect, or storage ownership change is part of this slice.
