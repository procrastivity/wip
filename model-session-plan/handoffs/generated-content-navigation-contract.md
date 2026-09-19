# Contract — generated-content-navigation (frozen by step-02)

Status: FROZEN. Steps 03–06 implement this document mechanically. Any
deviation must be recorded as a finding on the Matter and justified against
the constraints below before code changes.

Amended 2026-09-19 after step-02 verification rejected the first freeze
with six defects; all six are resolved in place (§3.1, §3.3, §3.6 new,
§7 guidance text, §8, §9 rows 3.6b/3.10b/3.20/4.1 and the step-06 block,
§10). Amended again after re-verification: the §3.6/§7/step-06 alternative
route now names `wip plumbing status --all` (plain status hides sealed
Matters older than the 14-day window), the guidance sentence covers all
three excluded variants, 3.20's `<root>` substitution uses the root as the
binary resolves it, and §3.3 cross-references §3.6. The amendment findings
on the Matter carry the correction record.

Binding inputs: the Matter's findings (seven rebaseline findings, the
verifier's two corrections, the planning finding) as rendered in
`.wip/generated/generated-content-navigation/matter.md`, and the Matter
Workplan's scope and seal condition.

---

## 0. Vocabulary used by this contract

- **Root** — the absolute path of the invocation worktree
  (`git rev-parse --show-toplevel`, resolved fresh per call; it is a mutable
  attribute, never a key — D37). `render.Current.Root` already carries it;
  the `next` verb layer must resolve the same value once per invocation
  (reuse `render.ResolveCurrent`, or resolve the toplevel in the verb layer
  and thread it down — implementation's choice, the value is the contract).
- **Owning Matter** — for any `store.Node n`: `n` itself when
  `n.Kind == ScaleMatter`, else the node `v.Node(ctx, n.Matter)`.
- **generatedDir of a node** — the absolute path
  `<Root>/.wip/generated/<owning-matter-locator>`
  (i.e. `filepath.Join(render.GeneratedDir(Root), matter.Locator)`).
- **Accepted locator** — a string wip verbs resolve. A Matter locator
  (`generated-content-navigation`) and a `matter/segment` form
  (`m/stage`, `m/step-NN`) are accepted. A printed Step *address*
  (`m · step-NN`, `m/stage · step-NN`) is NOT accepted — the ` · `
  separator is display-only (verified in step-01, correction 1).

---

## 1. Path representation (decision a)

**All new path fields are absolute paths.** Both `generatedDir` (on next
nodes) and every entry of `generatedFiles` (from refresh) are absolute,
rooted at Root.

Rationale (frozen): `scratchDir` — the only path field the surface already
has — is absolute; an absolute path is directly consumable (open the file,
no joining rule to teach); and Root is resolved fresh per call, so printing
the current resolution is truthful without making the path a key.

Consequences:
- Paths are valid in the worktree where the command ran. That is the
  workflow's own worktree, per the guidance text in §7.
- No repo-relative or `.wip`-relative form appears anywhere in JSON or
  human output. Do not add a second representation.

---

## 2. `next` node metadata — placement and field names (decision b)

**Per-node, on the shared node object.** Two new fields on
`internal/verbs/next/next.go`'s `nodeJSON`:

```go
type nodeJSON struct {
    ID           string `json:"id"`
    Address      string `json:"address"`
    Kind         string `json:"kind"`
    Lifecycle    string `json:"lifecycle"`
    Matter       string `json:"matter,omitempty"`
    GeneratedDir string `json:"generatedDir,omitempty"`
}
```

- `matter` — the owning Matter's locator. This is the accepted refresh
  locator: `wip plumbing refresh <matter>` renders the whole Matter tree
  regardless of which node inside it you care about (render granularity is
  per-Matter, `internal/render/refresh.go`), and the locator form is the
  only way a sealed Matter renders. This field is the locator round-trip
  fix mandated by the step-01 findings.
- `generatedDir` — as defined in §0.

JSON emission order is the struct order above: `id, address, kind,
lifecycle, matter, generatedDir`.

**Where the fields appear**: everywhere `nodeJSON` renders — `node`,
`candidates[]`, `inProgress[]`, `unmet[]`, `met[]`, `blocked[].node`,
`blocked[].blockedBy[]` — via one change in `toNodeJSON`. No per-result
field exists (rejected: Run-frontier `ready[]` and candidate lists span
Matters; a per-result field would be a lie on those shapes).

**Presence rule**: both fields are always populated for a node whose
`Repo` equals the resolved current Repo — which is every node in every
list today. The `omitempty` tags exist only for the boundary case of a
foreign-repo node reached through a ULID-created depend edge appearing in
`unmet`/`met`/`blocked[].blockedBy[]`: for such a node BOTH fields are
omitted (a foreign Matter's locator is ambiguous in this repo and its
generatedDir under this Root would be false). Tests assert presence on
same-repo nodes, not mere `omitempty` tolerance.

**Unchanged**: `stage` (`{locator, index, total}`), `pendingGates` (fields,
producers, and its Done-only conditionality — the new fields are
unconditional and fully orthogonal to it), all existing `kind`
discriminator values, `reason`, `backlogUnprocessed`, `waiting` (stays
human-only).

---

## 3. Per-shape behavior (decision c)

### 3.1 JSON, ordinary shapes

| shape (`kind`) | where the new fields appear |
|---|---|
| `bare-matter` | `node` (matter == node's own locator) |
| `positioned` | `node`; also `unmet[]`/`met[]` entries when present |
| `no-cursor` | every `candidates[]` entry |
| `in-progress-no-cursor` | every `inProgress[]` entry |
| `nothing-unblocked`, blocked variant (`blocked[]` non-empty) | every `blocked[].node` and `blocked[].blockedBy[]` entry |
| `nothing-unblocked`, idle variant (`blocked[]` absent — readsurface/next.go's second NothingUnblocked return sets Waiting only) | nowhere — no node objects exist; recorded exclusion, §3.6 |
| `everything-sealed` | nowhere — no node objects exist; recorded exclusion, §3.6 |
| `choose-next` (reason `sealed`/`canceled`) | `node`, plus every `candidates[]`/`inProgress[]` entry |
| `choose-next` (reason `removed`), lists non-empty | `node` stays absent (unchanged — a tombstoned target resolves to nothing); `candidates[]`/`inProgress[]` entries carry the fields |
| `choose-next` (reason `removed`), `candidates[]` and `inProgress[]` both empty | nowhere — no node objects exist (`waiting` fills, human-only); recorded exclusion, §3.6 |

No shape gains or loses any other field. The seven `kind` values are
unchanged. `waiting` stays human-only — no JSON projection of it is added
by this Matter (see §3.6).

### 3.2 JSON, Run-frontier shape

The Run-frontier payload (in `internal/verbs/next/runfrontier.go`):

1. **Discriminator (additive)**: the payload gains `"kind": "run-frontier"`
   as its first field. Justification: the frontier payload *replaces* the
   ordinary payload (verifier correction 2 — `renderRunFrontier` returns
   handled and `next.go` returns early), and today shares no field with
   it, so a caller cannot tell which contract it is reading. `run-frontier`
   cannot collide with the seven ordinary values.
2. **Node convergence**: the private `nodeJSON` in `runfrontier.go` is
   deleted; `ready[]` entries use the shared `nodeJSON` of §2. Net change
   to each entry: gains `lifecycle`, `matter`, `generatedDir`; `id`,
   `address`, `kind` keep their exact names and values. Justification:
   purely additive — `lifecycle` already prints on the human frontier
   lines, so JSON gains no new information class; convergence guarantees
   the two payloads never again drift on node fields.

Resulting payload field order:
`kind, run, locator, batch, cap, slots, ready`. `run`, `locator`, `batch`,
`cap`, `slots` are byte-for-byte unchanged. `ready` stays `[]` (never
null) when empty — unchanged.

### 3.3 Human output, ordinary shapes

One new line, exact format (note the two-space indent, matching existing
detail lines):

```
  generated: <generatedDir> (refresh: wip plumbing refresh <matter>)
```

Placement, frozen per shape:

- `bare-matter`: the last line of the block — after `no plan — work it
  directly` and after the `pending gates:` line when that renders.
- `positioned`: the last line of the block — after `blocked-by:` and after
  `pending gates:` when it renders.
- `choose-next` with reason `sealed` or `canceled`: immediately after the
  headline (`<addr> is sealed — choose what's next`), before any
  `N unblocked:` / `N in progress:` lists.
- `choose-next` with reason `removed`: no generated line (no target).
- `no-cursor`, `in-progress-no-cursor`, `nothing-unblocked`,
  `everything-sealed`, and every per-candidate / per-in-progress /
  waiting line: **unchanged**. The list shapes rest on the JSON surface
  alone: the taught workflow (§7) is `wip plumbing next --json`, and each
  listed entry there carries its own `matter`/`generatedDir`. No human
  fallback via `--set` is claimed — a printed Step candidate address is
  refused by `--set` in both forms (§0), and the fields needed to compose
  an accepted Step locator are deliberately not in this contract (§5).
  The no-node variants of these shapes are §3.6's recorded exclusions.

### 3.4 Human output, Run-frontier

Unchanged. Consistent with 3.3's list rule: the frontier is a list shape
and its consumers read JSON; the ready lines keep their exact format.

### 3.5 `--set` and `--clear` (decision d)

- `--set` **participates, JSON only**. The response object gains two flat
  fields computed from the node just set:
  `{"node": "<ulid>", "address": "<address>", "matter": "<matter-locator>", "generatedDir": "<abs path>"}`.
  Rationale: `--set` names a concrete node and the very next act is to work
  it; the fields save one `next` round trip. Human line stays exactly
  `cursor set to <address>`.
- `--clear` **does not participate**: there is no target. Its JSON
  (`{"cleared": true, "previous": …}`) and human line are unchanged.

### 3.6 Recorded exclusions — shapes with no node object anywhere

Three result variants carry NO owning-Matter metadata, deliberately:

1. `nothing-unblocked`, idle variant — reached when ready, blocked, and
   in-repo InProgress are all empty (readsurface/next.go's second
   NothingUnblocked return); only `waiting` fills, and `waiting` is
   human-only. This is the ordinary resting state of a repo whose work is
   Done with open gates.
2. `choose-next` with reason `removed` and `candidates[]`/`inProgress[]`
   both empty — same structure: `{"kind":"choose-next","reason":"removed"}`
   plus human-only waiting.
3. `everything-sealed` — no node objects exist at all.

**Decision (frozen): exclusion, not a `waiting` JSON projection.** These
variants name no work: there is no live target and no candidate, so the
read path — which is target-driven by construction — has no canonical
node to point at. Projecting `waiting` into JSON with per-Matter
metadata was rejected because (a) it is a new JSON field the Matter
Workplan's scope never sanctioned, (b) `status-waiting-summary` and
`status-actionable-waits` deliberately kept waiting human-only, adding no
JSON field, and this Matter must not silently reverse that sealed
decision, and (c) whether an agent needs a machine-readable
waiting/progress surface beyond next-plus-generated-files is exactly
step-07's reserved question — these exclusions are recorded input to it,
not an answer.

**Seal-condition reading (frozen):** the seal condition's "every `next`
result shape" is satisfied for these three variants by the alternative
route, which guidance and the step-06 proof must both cover: when `next`
reports no node object, run `wip plumbing status --all` and feed the chosen
Matter locator to `wip plumbing refresh <matter>`, then read the reported
files. The `--all` flag is load-bearing: plain `wip plumbing status` hides
sealed Matters older than the 14-day recent-sealed window
(readsurface/status.go's recentSealedWindow / HiddenSealedMatters),
printing only a "… N more sealed matter(s)" count line, so in an aged
repo's everything-sealed state the plain form yields no locator at all.
No parsing beyond reading a printed Matter locator is involved (a Matter
locator is an accepted locator, §0). §8's status prohibition is
untouched: the route uses the flag as it exists today and changes nothing
about status.

Step-06 must test all three variants: assert the JSON carries no
`matter`/`generatedDir` anywhere, then complete the read path via
`status --all` → refresh as above — with the everything-sealed fixture's
seal backdated beyond the 14-day window, so the proof fails if the route
ever regresses to the plain form (§9, step-06).

---

## 4. Refresh `generatedFiles` (decision e)

### 4.1 Representation

- `render.Result` gains `GeneratedFiles []string`.
- The refresh JSON payload gains `generatedFiles` **after** `rendered`:

```json
{"dispatch":"…","scratchDir":"…","opened":false,"superseded":"…","rendered":["m1","m2"],"generatedFiles":["/abs/…/m1/matter.md","…"]}
```

- **Written only.** The list holds exactly the files `writeGenerated`
  wrote in this pass. Files removed by `pruneStepFiles` are NOT reported
  anywhere (the seal condition says "reports only files written"; a
  deletion is renderer hygiene, not a read target). No `prunedFiles`
  field. Do not add one.
- **Observational, not enumerated.** The list is collected at the
  `writeGenerated` call sites (`renderMatterTree` returns the paths it
  wrote; `Refresh`/`Render` concatenate). No static file-vocabulary list
  exists in code, payload, or docs — the vocabulary grew twice since
  planning (Step Workplans; Stage/Step findings files) and an enumerated
  contract would already have been wrong twice. Tests assert against
  observed writes, never against a hard-coded "complete" vocabulary.
- **Ordering: write order, which is deterministic.** Per Matter:
  `matter.md`, then `brief.md` (if earned), `workplan.md` (if earned),
  `roadmap.md` (if the Matter has children), then for each Stage/Step in
  `MatterNodes` order: `workplan-<locator>.md` (if content),
  `findings-<locator>.md` (if content). The eager path concatenates
  Matters in `rendered`'s own (EagerScope) order. `writeGenerated`
  rewrites unconditionally, so "written" needs no content comparison.
- **Paths: absolute** (§1).
- **Empty form**: `"generatedFiles": []` — the field is always present,
  never `null`, never omitted (no `omitempty`), matching `rendered`.
- **Compatibility**: `rendered` (Matter locators) is retained byte-for-byte
  unchanged, `omitempty` on `superseded` unchanged, `dispatch`/
  `scratchDir`/`opened` unchanged. Event behavior unchanged: still exactly
  one `render.performed` per pass, same payload — the file list is verb
  output, never an event field.

### 4.2 `render.Exit` (the seal path)

**Exit stays outside the contract and keeps its `error`-only signature.**
No verb surfaces Exit's output (finish/cancel/gate-close output is
untouchable per §8), so a Result there would be dead weight and a scope
leak into the dispatch/seal surface. The sanctioned way to get a sealed
Matter's file list is `wip plumbing refresh <locator>` — which this Matter's own
workflow already teaches. Internal consequence: `renderMatterTree` changes
signature to return `([]string, error)`; `Exit` discards the list.

### 4.3 Human refresh output (decision f)

Existing first line (`opened dispatch …` / `continuing dispatch …
(scratch dir …)`) and the `rendered N matter(s)` line are unchanged. Then:

- **On-demand path** (`wip plumbing refresh <locator>`): one line per file, in
  `generatedFiles` order, two-space indent, absolute path, nothing else on
  the line:

  ```
  continuing dispatch 01ABC… (scratch dir /repo/.wip/work/01ABC…)
  rendered 1 matter(s)
    /repo/.wip/generated/my-matter/matter.md
    /repo/.wip/generated/my-matter/workplan.md
    /repo/.wip/generated/my-matter/roadmap.md
  ```

- **Eager path** (bare `wip plumbing refresh`): no per-file lines; one summary
  line appended:

  ```
  wrote 12 file(s)
  ```

  (`fmt.Sprintf("wrote %d file(s)", len(result.GeneratedFiles))` — no
  pluralization logic.) Rationale: the canonical workflow always refreshes
  one Matter by locator; the eager path is session-start hygiene where a
  100-line list would bury `status`. JSON carries the full list on BOTH
  paths.

---

## 5. Locator round-trip (part of decision c)

The fix is the `matter` field of §2 and §3.5, everywhere a node renders.
Guidance and help (§7) must state plainly: *a printed Step address is not
an accepted locator; pass the `matter` value to refresh.* Nothing about
locator resolution itself changes — `resolveLocator` /
`writesurface.ResolveNode` behavior, error identifiers
(`validation.unknown-locator`) and messages are untouched.

**Deliberately left open** (record, do not implement): a per-node
accepted-locator field (e.g. `locator: "step-NN"`) that would close the
`--set`-to-a-listed-Step round trip. That is cursor surface, not the read
path; refresh needs only the Matter. If wanted later it is additive.

---

## 6. Scope of changes, by file (steps 03–04)

Step-03 (`next` metadata):
- `internal/verbs/next/next.go` — `nodeJSON` two fields; `toNodeJSON`
  resolves owning Matter + generatedDir (needs Root and the store view);
  human generated line for the three target shapes; `renderSet` two flat
  JSON fields; `Long` help text (§7).
- `internal/verbs/next/runfrontier.go` — shared `nodeJSON`, payload
  `kind` discriminator.
- `internal/readsurface/` — only if plumbing Root/matter data through
  `View`/helpers is needed; no behavior change to Cursor, Frontier,
  CollapseReady, Address, resolveLocator, SetCursor, ClearCursor.

Step-04 (refresh files):
- `internal/render/matter.go` — `renderMatterTree` returns written paths.
- `internal/render/refresh.go` — `Result.GeneratedFiles`; `Refresh`/
  `Render` collect; `Exit` unchanged signature.
- `internal/verbs/refresh/refresh.go` — JSON field, human list/count
  lines, `Long` help text (§7).

Step-05 (docs): `assets/agent-write-guidance.md` (§7 paragraph), plus the
`Long` texts if not already landed with 03/04.

No other production file changes.

---

## 7. Help and installed guidance (decision g)

**Shorts are frozen.** The manifest projects `Short` only
(`internal/manifest/verbs.go`), so changing a Short churns the digest and
every skill table; no Short changes anywhere in this Matter.

**`Long` added to two commands** (both spellings of `next` share one
`command()`, so one text serves both; `TestPair_NextHelpDiffersOnlyInCommandPath`
must keep passing):

`wip plumbing refresh` Long (exact text):

```
With no locator: open or continue this worktree's dispatch and re-render
every not-sealed Matter. With a locator: render that node's owning Matter
alone — the only way a sealed Matter renders. Either way the result names
the files actually written: generatedFiles in JSON (absolute paths, in
write order, beside the retained rendered locators); the human locator
form lists each file, the bare form prints "wrote N file(s)". Read those
files — never wip's database — for Matter content.
```

`wip plumbing next` / `wip next` Long (exact text):

```
Every node in the result carries its owning Matter's locator (matter) and
that Matter's generated directory (generatedDir, absolute) in JSON; the
current target's human output prints the same as a "generated:" line. A
printed Step address is not an accepted locator — pass the matter value
to other verbs. To read a Matter's record: wip plumbing refresh <matter>,
then read exactly the files it reports.
```

**Installed guidance**: append ONE paragraph to
`assets/agent-write-guidance.md`, after the findings paragraph (exact
text):

```
To read a Matter's own record, follow the read path `next` teaches: run
`wip plumbing next --json` and take the target's (or your chosen candidate's)
`matter` and `generatedDir` fields; run `wip plumbing refresh <matter>` (the
locator form also renders a sealed Matter); then read exactly the files
that refresh reports — `generatedFiles` in JSON, the indented list in
human output. When `next` reports no node at all, take the Matter locator
from `wip plumbing status --all` instead and refresh it the same way —
plain status hides sealed Matters older than two weeks. A printed Step
address is not an accepted locator; the
`matter` field is. Never open wip's database or event log to answer a
content question, and never read another repo's `.wip/`: the files
refresh reports are the whole sanctioned read surface for Matter content.
```

**Database-access prohibition — exact scope**: it prohibits (a) reading
wip's SQLite store or event log at any tier, (b) reading another repo's
`.wip/`, and (c) answering content questions from wip's source. It does
NOT prohibit reading the files refresh reports under `.wip/generated/`
(that is the sanctioned surface) or the dispatch's own scratch dir. Do not
widen or narrow it beyond this sentence set.

Delivery caveat (step-05 must state in its finding, not automate): the
guidance reaches an installed agent only when the skill is regenerated by
`wip install claude-code` (and the other harnesses' installers); this
Matter does not run installers.

---

## 8. Forbidden changes

No change to, in any step of this Matter:

- `status` (all spellings) — output, JSON, flags.
- Gate presentation: `pendingGates` fields, its Done-only conditionality,
  `pending gates:` lines, gate verbs.
- Cursor behavior: `SetCursor`/`ClearCursor` semantics, `cursor.moved`
  payload, locator resolution and its error identifiers/messages.
- Dispatch lifecycle: OpenOrReuse, supersede, close, scratch dirs.
- Event taxonomy: no new event types, no payload changes
  (`render.performed` unchanged).
- Database schema (any tier).
- Existing JSON fields anywhere: names, types, order, `omitempty`
  behavior, `kind` values, `rendered` semantics.
- Existing human lines other than the exact additions in §3.3 and §4.3.
- Any `Short` help text; the manifest digest for existing entries;
  `alias-of` wiring.
- No automatic refresh from any verb; no raw-content read command; no
  `refreshRequired` (or any freshness) field; no `prunedFiles` field; no
  relative-path variants of the new fields; no JSON projection of
  `waiting` (§3.6 — it stays human-only).
- Seal path: `render.Exit` signature and callers' output.
- `.wip/generated/` file vocabulary, contents, 0444 discipline,
  `pruneStepFiles` behavior.

---

## 9. Test matrix (steps 03–06)

Conventions: e2e tests live in `internal/cli/*_e2e_test.go`; package tests
beside their package. "JSON" means `--json`; every JSON assertion checks
exact field names and values, and asserts absence where the contract says
absent.

### Step-03 — next metadata

| # | test | asserts |
|---|---|---|
| 3.1 | bare-matter JSON | `node.matter` == node's own locator; `node.generatedDir` == `<Root>/.wip/generated/<locator>` |
| 3.2 | positioned JSON, Stage-grouped Step | `node.matter`/`node.generatedDir`; `stage` unchanged; `unmet[]`/`met[]` entries carry both fields |
| 3.3 | positioned JSON, Matter-direct Step | same, no `stage` |
| 3.4 | no-cursor JSON, candidates spanning ≥2 Matters | each `candidates[]` entry carries its OWN matter/generatedDir (distinct values) |
| 3.5 | in-progress-no-cursor JSON | each `inProgress[]` entry carries both fields |
| 3.6 | nothing-unblocked JSON, blocked variant | `blocked[].node` and `blocked[].blockedBy[]` entries carry both fields |
| 3.6b | nothing-unblocked JSON, idle variant (all work Done, open gates, no blocked edges) | payload byte-identical to pre-change shape — exactly `{"kind":"nothing-unblocked"}`, no metadata fields anywhere (§3.6) |
| 3.7 | everything-sealed JSON | payload byte-identical to pre-change shape (no new fields; §3.6 exclusion) |
| 3.8 | choose-next sealed JSON | `node` carries both fields; lists carry them |
| 3.9 | choose-next canceled JSON | same as 3.8 |
| 3.10 | choose-next removed JSON, lists non-empty | no `node`; lists carry the fields |
| 3.10b | choose-next removed JSON, candidates and inProgress both empty | `{"kind":"choose-next","reason":"removed"}` with no metadata fields anywhere (§3.6) |
| 3.11 | human bare-matter / positioned | exact `  generated: <dir> (refresh: wip plumbing refresh <matter>)` as last line; with pending gates present, line comes after them |
| 3.12 | human choose-next sealed/canceled | generated line directly after headline; removed → no line |
| 3.13 | human list shapes | candidate / in-progress / waiting lines byte-unchanged |
| 3.14 | pendingGates orthogonality | Done target: `pendingGates` AND matter/generatedDir; In-Progress target: matter/generatedDir, no `pendingGates` |
| 3.15 | Run-frontier JSON | `kind == "run-frontier"` first; `ready[]` entries have id/address/kind unchanged plus lifecycle/matter/generatedDir; run/locator/batch/cap/slots unchanged; empty-slot cases unchanged |
| 3.16 | Run-frontier cross-Matter | Batch joining ≥2 Matters: distinct generatedDir per ready node |
| 3.17 | Run-frontier human | frontier lines byte-unchanged |
| 3.18 | `--set` JSON | `{node, address, matter, generatedDir}`; human `cursor set to …` unchanged |
| 3.19 | `--clear` JSON + human | byte-unchanged |
| 3.20 | alias parity (extends `pairs_e2e_test.go`) | `wip next` vs `wip plumbing next` golden-same, human and JSON, on a shape carrying the new fields. Normalization is part of this contract: `TestPair_NextGoldenSameness` seeds a separate temp repo per spelling, so the two runs print different absolute roots in `generatedDir`; extend `maskIDs` (or a sibling helper applied beside it) to substitute each run's own worktree root — as the binary resolves it, i.e. `git rev-parse --show-toplevel` run in the fixture dir, not the raw temp-dir string (they differ under symlinked temp roots) — with the literal token `<root>` before comparison, per subcase, ULID masking unchanged. This is the standing cost of the frozen absolute-path decision and applies to any future cross-root golden. Help-path test keeps passing |
| 3.21 | manifest regression | digest/drift tests pass — `Long` is manifest-invisible; `alias-of` unchanged (`TestPair_ManifestRecordsAliasOf`) |

### Step-04 — refresh file list

| # | test | asserts |
|---|---|---|
| 4.1 | render pkg: full vocabulary Matter | `Result.GeneratedFiles` exact ordered list: matter.md, brief.md, workplan.md, roadmap.md first (always this order), then per Stage/Step node in `MatterNodes` order — which sorts by sort_key, birth_event across Stages and Steps TOGETHER, so Stage and Step files interleave by creation order, not Stages-first — each node contributing workplan-<locator>.md then findings-<locator>.md when earned. The expected tail is derived from the fixture's own creation order, never hard-coded as Stages-before-Steps; absolute paths |
| 4.2 | render pkg: minimal Matter | exactly `[…/matter.md]` |
| 4.3 | render pkg: eager multi-Matter | concatenation in `rendered` order |
| 4.4 | render pkg: prune case | stale `workplan-step-NN.md` removed from disk and ABSENT from GeneratedFiles |
| 4.5 | render pkg: empty eager scope | `GeneratedFiles == []` (present, empty) |
| 4.6 | render pkg: Exit | signature/behavior unchanged; final snapshot still written (existing d33/seal tests keep passing) |
| 4.7 | e2e refresh JSON, locator form | `generatedFiles` after `rendered`; `rendered` unchanged; sealed Matter renders and reports files |
| 4.8 | e2e refresh JSON, bare form | full list; `[]` (not omitted) when nothing rendered |
| 4.9 | e2e refresh human, locator form | dispatch + `rendered 1 matter(s)` lines unchanged, then exact two-space-indented absolute file lines in order |
| 4.10 | e2e refresh human, bare form | exact `wrote N file(s)` line; no per-file lines |
| 4.11 | D33 cold start | delete `.wip/` between dispatches; refresh reports the full re-written set; 0444 restored |
| 4.12 | event regression | exactly one `render.performed`, payload unchanged |

### Step-05 — docs

| # | test | asserts |
|---|---|---|
| 5.1 | harness asset tests (claudecode + amp/codex/devin/opencode/pi) | guidance asset contains the §7 workflow paragraph verbatim (extend existing `agent-write-guidance.md` resolve tests) |
| 5.2 | help e2e | `wip plumbing refresh --help` and both `next --help` spellings contain their §7 Long texts; Shorts unchanged |
| 5.3 | skill-table projection | `TestPair_SkillTablesProjectPlumbingMembers` and manifest digest unchanged |

### Step-06 — cold-start proof

One e2e per shape that has a target or candidates (bare-matter,
positioned ×2, no-cursor, in-progress-no-cursor, nothing-unblocked
blocked variant, choose-next sealed/canceled/removed-with-lists,
run-frontier), each driven ONLY by command output:

1. run `next --json` (one case uses the `wip next` spelling),
2. select `matter`/`generatedDir` per this contract (target node, else
   first candidate / blocked node / ready node),
3. run `wip plumbing refresh <matter>`,
4. assert: every `generatedFiles` path exists, is a regular 0444 file, and
   is under the selected `generatedDir` (for that Matter's files); the set
   read is exactly the reported set — no directory listing, no globbing.

Plus one e2e per §3.6 excluded variant (nothing-unblocked idle,
choose-next removed with empty lists, everything-sealed), driven ONLY by
command output via the alternative route: run `next --json`, assert no
`matter`/`generatedDir` appears anywhere in the payload, then run
`wip plumbing status --all`, take a Matter locator from its output, run
`wip plumbing refresh <matter>`, and apply assertion 4 above to the reported
files. The everything-sealed fixture must backdate its Matter's seal
beyond the 14-day recentSealedWindow so the sealed Matter is hidden from
plain `wip plumbing status` — proving the `--all` half of the route, which
would otherwise pass vacuously.

Include: the choose-next-sealed chase (sealed Matter → locator refresh
works, bare refresh would skip it), and a `--set` case (set from a
candidate's `matter` + `/step-NN` composition is NOT required — set to the
Matter locator suffices for the test).

Full-suite regression gate for every step: `go test ./...` green,
including manifest, harness, render, guards, pairs, scheduler suites.

---

## 10. Deliberately out of scope (hand to step-07 / later Matters)

- Per-node accepted-locator field for Step-grain `--set` round trips (§5;
  §3.3's rejected human fallback strengthens the case, record it as input
  when step-07 or a later Matter weighs this).
- Any machine-readable progress surface beyond next + generated files,
  including any JSON projection of `waiting` — the §3.6 exclusions are
  recorded input to step-07's remaining question, not an answer to it.
- Reporting pruned files; reporting Exit/seal-path writes.
- Any freshness/staleness signal.
