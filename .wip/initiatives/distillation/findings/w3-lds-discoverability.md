# w3 — LDS discoverability, composability & the extract pipeline

## TL;DR

- **One pointer beats `find`.** Add a single top-level `wip.yaml` whose `features.lds` block names the install root(s) (`engineering/`, `docs/`, custom). Keep `.lds-manifest.yaml` as the authoritative per-install record, but make `wip doctor` read the pointer first; fall back to `find -maxdepth 3 -name .lds-manifest.yaml` only when the pointer is absent (legacy/unconfigured repos).
- **Composability is a matrix, not a flag.** LDS and Diátaxis are two **independent** features that can coexist (xcind: both), be solo (hypomnema: only LDS in `docs/`; bizapps: LDS in `engineering/` only), or be absent. Model them as two separate `wip.yaml` feature entries, each with their own root path.
- **The "old-style vs new-style" split is not a schema problem — it's a path problem.** `hypomnema/docs/.lds-manifest.yaml` and `playbook/engineering/.lds-manifest.yaml` use the **same** schema (`schema_version: "1.0"`); they differ only in install root. The pointer in `wip.yaml` resolves the ambiguity deterministically; nothing in the manifest schema needs to change.
- **Don't invent an extraction pipeline.** `.wip/<initiative>/` already maps cleanly onto the existing LDS verbs: `brief.md` + `proposal.md` → `analyze.md` (produces a manifest scoped to one initiative) → `review.md` → `extract.md` writes into `ENG_DOCS_DIR/{decisions,specs}/`. `workplans/` and `archive/` graduate via `create.md` (single ADR/spec) or `maintenance/update.md` (after merge). Bizapps `COMMON.md §11` is the proof this works — it's just an *informal* cross-reference today; we make it the *output* of graduation.
- **The general detection contract is one rule, applied to every feature.** Each composable feature (LDS, Diátaxis, changelog, prtend, Solo, direnv, .wip) advertises its presence by (a) a stanza in `wip.yaml` and (b) a feature-specific sentinel file at a declared path. `wip doctor` answers "is X installed and where?" by reading `wip.yaml` and verifying each sentinel exists. No probing, no guessing, no `find` walks at steady state.

## Recommendations

### 1. The detection contract (general; applies to every feature)

A feature is **active** iff both hold:

1. `wip.yaml` has a `features.<name>` stanza with `enabled: true`.
2. The sentinel file declared in that stanza exists on disk.

A feature is **installed-but-undeclared** iff its sentinel exists but no stanza names it. `wip doctor` reports these as drift and offers `wip detect --write` to materialize the stanza.

A feature is **declared-but-broken** iff a stanza names a sentinel that does not exist. `wip doctor` reports this as a hard error.

Each feature contributes a small **descriptor** registered with `wip`:

```yaml
# conceptual — lives in the wip CLI, not on disk
name: lds
sentinel_glob: "{root}/.lds-manifest.yaml"   # {root} substituted from stanza
schema_check: "schema_version"               # field that must be present
optional_fields: [lds_version, install_type, layer_selections]
plural: true                                  # multiple install roots allowed
```

The descriptor is the contract; `wip doctor` is a generic loop over descriptors. New features (changelog, prtend, solo) just register a descriptor.

### 2. `wip.yaml` schema (LDS-relevant slice; coordinate with w1)

```yaml
# wip.yaml — top-level pointer for all composable features
schema_version: 1

features:
  lds:
    enabled: true
    # Plural: a repo may host multiple LDS installs (rare but supported).
    # Each entry's `root` is the directory containing .lds-manifest.yaml.
    installs:
      - root: engineering    # xcind, playbook, bizapps style
        track: engineering   # explicit: this is the engineering canon
      - root: docs           # only if Diátaxis is NOT in docs/
        track: engineering   # hypomnema style (single-track in docs/)

  diataxis:
    enabled: true
    root: docs               # xcind style (LDS in engineering/, Diátaxis in docs/)
    # When LDS is also in docs/ (hypomnema), diataxis.enabled MUST be false.
    # wip doctor enforces this mutual exclusion at the path level.

  wip:
    root: .wip
    commit_policy: gitignored   # or: committed (w1 owns this)

  # ... changelog, prtend, solo, direnv stanzas follow the same shape
```

Mutual-exclusion rule (enforced by `wip doctor`): **no two features may declare the same `root`** except where their descriptors explicitly mark them as cohabitable. LDS and Diátaxis are not cohabitable in the same root — that's the entire reason `migrate-to-two-track.md` exists.

### 3. LDS detection algorithm (deterministic)

```
1. Read wip.yaml. If features.lds.enabled, for each install:
     - Verify {root}/.lds-manifest.yaml exists. Read schema_version.
     - Report: lds@{root} (track={engineering|user}, version={lds_version|unknown})
2. If wip.yaml is absent OR features.lds is absent:
     - Run: find . -maxdepth 3 -name .lds-manifest.yaml
     - For each hit, report as DRIFT and propose a wip.yaml stanza.
3. If neither yields a manifest: report "LDS not installed".
```

Steady-state cost: one file read. Bootstrap/legacy cost: one shallow `find`.

### 4. The four LDS topologies `wip doctor` must distinguish

| Topology | Example | `wip.yaml` shape |
|---|---|---|
| **Two-track** (LDS + Diátaxis) | `xcind/` | `lds.installs=[{root: engineering}]`, `diataxis.root: docs` |
| **Single-track engineering-only** | `bizapps`, `playbook` | `lds.installs=[{root: engineering}]`, no `diataxis` |
| **Single-track in `docs/` (old-style)** | `hypomnema` | `lds.installs=[{root: docs, track: engineering}]`, no `diataxis` |
| **None** | bare repo | `features.lds.enabled: false` |

The "old-style vs new-style" distinction (BRIEF item 1) collapses to **which root holds the manifest** — `docs/` or `engineering/`. The manifest schema is identical (`hypomnema/docs/.lds-manifest.yaml:5` vs `playbook/engineering/.lds-manifest.yaml:5` both at `schema_version: "1.0"`). Migration to two-track remains the existing LDS workflow (`migrate-to-two-track.md`); `wip` just detects which side of that migration you're on.

### 5. The `.wip/` → LDS extract pipeline (no new system)

`.wip/` planning content **graduates** by being fed to existing LDS verbs. Map:

| `.wip/<initiative>/` artifact | Graduates via | LDS destination |
|---|---|---|
| `brief.md` (≡ bizapps `COMMON.md`) | `analyze.md` (scoped to one initiative) → `review.md` → `extract.md` | `engineering/decisions/NNNN-*.md` + `engineering/specs/<feature>.md` |
| `proposal.md` (pre-commitment design) | `create.md` (one ADR at a time) | `engineering/decisions/NNNN-*.md` |
| `workplans/step-NN.md` chunks (durable design discovered mid-execution) | `create.md` (spec) or `maintenance/update.md` (after merge) | `engineering/specs/<feature>.md` |
| `roadmap.md` | Stays in `.wip/`; *summary* graduates as a product/vision note via `create.md` | `engineering/product/roadmap.md` (optional) |
| `archive/` | Source for `analyze.md` if pattern-mining a closed initiative | wherever the manifest places it |

The new `wip` verb is a **thin shim**, not a parallel pipeline:

```
wip graduate <initiative> --as decision   # → create.md (single ADR)
wip graduate <initiative> --as spec       # → create.md (single spec)
wip graduate <initiative> --bulk          # → analyze.md → review.md → extract.md
```

Each form invokes the existing LDS verb with the right `LEGACY_DOCS_DIR=.wip/<initiative>/` and `ENG_DOCS_DIR={features.lds.installs[*].root}`. We do not write a new extraction engine — we feed `.wip/` into `analyze.md`'s manifest generator and let `extract.md` do the deterministic copy (`layered-documentation-system/extract.md:68-88`).

**Bizapps `COMMON.md §11`** (`bizapps-symfony-bot/.wip/conversations/COMMON.md:252-265`) is the **template for the output of graduation**. Every initiative's `brief.md` should grow an "LDS cross-references" section that names the ADRs/specs that have been extracted from it. This makes graduation visible and audit-able: an initiative is "fully graduated" when every durable claim in `brief.md` has a citation in §11.

### 6. Composability rules (LDS × Diátaxis × neither)

- **LDS without Diátaxis** is the default. Most projects only need the engineering track. `wip.yaml` declares `lds` only.
- **Diátaxis without LDS** is supported but rare. Declare `diataxis.root: docs` and omit `lds`. `wip doctor` does not require LDS to validate Diátaxis.
- **Both present** (xcind): two stanzas, two roots, two sentinels. `wip doctor` validates each independently. The Diátaxis sentinel is `{root}/README.md` containing the four canonical sections (Tutorials/How-to/Reference/Explanation) — cheap, deterministic, no schema (see `xcind/docs/README.md:1-44` for the shape). w4 owns Diátaxis detection details; w3 only requires that the two never claim the same `root`.
- **Coexistence inside one root is forbidden.** If a repo has `hypomnema`-style `docs/.lds-manifest.yaml` and wants to add Diátaxis, the path is `migrate-to-two-track.md` — not a `wip.yaml` flag. `wip doctor` surfaces this with a specific remediation: *"Run `migrate-to-two-track.md` to split this single-track install."*

### 7. `wip doctor` / `wip detect` output

`wip doctor` (verification): exit 0 if every declared feature's sentinel exists and conflicts are absent; non-zero with a punch list otherwise.

```
$ wip doctor
✓ lds@engineering   (schema 1.0, version 3.0.0, install_type=migration)
✓ diataxis@docs     (4/4 canonical sections present)
✓ wip@.wip          (committed=false)
✗ changelog          declared but CHANGELOG.md missing
```

`wip detect` (bootstrap): runs the fallback `find` and prints a proposed `wip.yaml`. `--write` materializes it.

```
$ wip detect
Found: docs/.lds-manifest.yaml  → proposing features.lds.installs=[{root: docs, track: engineering}]
Found: CHANGELOG.md             → proposing features.changelog.enabled=true
No diataxis sentinel found.
```

## Evidence

- **Two manifest locations, same schema**: `hypomnema/docs/.lds-manifest.yaml:5` (`schema_version: "1.0"`, `install_type: "fresh"`, root `docs/`) vs `playbook/engineering/.lds-manifest.yaml:5` (`schema_version: "1.0"`, `install_type: "migration"`, root `engineering/`). Confirms BRIEF item 6: the schema is shared; only the install root differs. A pointer resolves this.
- **Install.md generates the manifest**: `layered-documentation-system/install.md:1355-1395` shows the manifest template the installer writes. We do not need to change this; we wrap it.
- **Two-track is already an LDS-native concept**: `layered-documentation-system/LAYERED-DOCUMENTATION-SYSTEM.md:80-93` defines `ENG_DOCS_DIR` (default `engineering/`) and `USER_DOCS_DIR` (default `docs/`) as **distinct roots**. `wip.yaml` reflects this distinction; it does not invent it.
- **Migrate-to-two-track is the bridge for old-style**: `layered-documentation-system/migrate-to-two-track.md:22-39` is the canonical path from "LDS in `docs/`" to "LDS in `engineering/` + Diátaxis in `docs/`". `wip doctor` surfaces this workflow as the remediation when it detects the single-root case.
- **xcind = positive coexistence**: `xcind/engineering/README.md:1-33` (LDS) and `xcind/docs/README.md:1-44` (Diátaxis) live side-by-side without conflict. The two READMEs are sufficient sentinels; no extra metadata needed.
- **Bizapps already does LDS cross-references manually**: `bizapps-symfony-bot/.wip/conversations/COMMON.md:252-265` lists ADRs and specs the initiative depends on/graduates to. This is the human-written prototype of what `wip graduate` should automate (and what `analyze.md`'s manifest captures formally — see `layered-documentation-system/extract.md:14-23`).
- **Extract is deterministic and mechanical**: `layered-documentation-system/extract.md:68-88` ("Extraction is DETERMINISTIC … Extraction is MECHANICAL"). Routing `.wip/` content through it preserves that guarantee — no LLM interpretation at graduation time.
- **Create.md handles single-item additions**: `layered-documentation-system/create.md:20-56` walks the classification decision tree for adding one ADR/spec. This is the right verb for `wip graduate --as decision`.

## Open questions / escalations for the human

1. **Plural LDS installs**: do you actually want to support more than one LDS root per repo (e.g. a monorepo with per-package engineering docs), or is the simpler "exactly one LDS root" assumption fine? The schema above allows plural; collapsing to scalar simplifies `wip doctor` materially.
2. **Where does `wip.yaml` live?** Top of repo is obvious. But if `.wip/` itself is gitignored by default (BRIEF "confirmed decisions" §1), `wip.yaml` must NOT live under `.wip/` — it must be committed at the repo root. Confirm.
3. **Sentinel for Diátaxis**: I proposed "README.md with the four canonical sections." That's heuristic. A stricter alternative: require a `.diataxis-manifest.yaml` mirror of LDS's manifest. Heavier but uniform. w4's call; flagging the choice here.
4. **`wip graduate --bulk`**: `analyze.md` was designed to consume an entire `legacy-docs/` tree. Pointing it at `.wip/<initiative>/` (which is small, structured, and already partly LDS-shaped) may produce a low-value manifest with mostly 1:1 mappings. May be worth a **`wip graduate --quick`** mode that skips `analyze.md`/`review.md` and asks the human to pick a target layer per file directly. Decide after first real use.

## Dependencies on other workstreams

- **w1** owns `wip.yaml`'s overall schema, the `.wip/` layout, and the `features.wip` stanza. The `features.lds` and `features.diataxis` slices proposed here must slot into w1's broader schema. **Action**: w1 to confirm `features.<name>` is the right top-level key; w3 has assumed it.
- **w2** owns baseline-tooling features (changelog, direnv, prtend). Those features adopt the **same detection contract** (§1 above): a stanza in `wip.yaml` + a sentinel file. w3 has defined the contract; w2 picks the sentinels for its features.
- **w4** owns Diátaxis specifically. w3 assumes Diátaxis is a peer feature with its own root and sentinel, and that LDS+Diátaxis coexist as two separate stanzas. w4 confirms the Diátaxis sentinel format.
- **w5** consumes everything: `wip doctor` is w5's tool. w3 provides the descriptor-driven detection algorithm; w5 implements it. The output format in §7 is a starting point — w5 owns final UX.
