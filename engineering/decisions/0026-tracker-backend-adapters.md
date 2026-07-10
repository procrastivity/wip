# 0026 — Tracker backend adapters (github/gitlab over a per-backend adapter surface)

- Status: accepted
- Date: 2026-07-10
- Source: `tracker-backend-seam` initiative, Round 1 (step-01); Linear BDS-74
  (substrate), BDS-75 (github lane), BDS-76 (gitlab lane); ADR-0019, ADR-0024,
  ADR-0018, ADR-0022, ADR-0006, ADR-0010
- Amends: **ADR-0019** (the "Linear first / CLI transport deferred" framing gains
  github/gitlab CLI transports now; Linear's own CLI wrapper stays deferred to
  BDS-23), **ADR-0024** (the `[tracker: ID]` marker id sub-pattern widens beyond
  the Linear-key shape)

## Context

ADR-0019 built the issue-tracker seam "provider-agnostic by design, Linear
first": plumbing resolves a lifecycle intent into a bind plan and never calls the
provider; the config key is `issue-tracker`; the marker is `tracker:`; no
provider state names leak into plumbing. But only Linear was implemented, and two
gaps now force the generalization:

1. **A latent bug.** `setup issue-tracker github` already succeeds — the allow-list
   at `setup.bash:1864` is `linear | github` — writing `backend: github` into
   `.wip.yaml`. Yet no `github` arm exists in the transport layer, so every state
   map falls through its `*)` passthrough and both command seams
   (`_wip_tracker_transport_{read,write}_cmd`) return empty. A user can configure a
   backend that silently does nothing. `gitlab` is not accepted at all.
2. **github/gitlab can carry a real CLI transport on day one.** Linear had none —
   hence its agent/MCP path, with a CLI transport deferred to BDS-23. But `gh`/`glab`
   are already this repo's forge CLIs (ADR-0018/0022), and `gh issue view --json
   state,stateReason,labels` / `gh issue edit --add-label` are verified. These
   backends light up `sync`'s `transport: cli` path headlessly — `applied` /
   `skipped` / `observed` buckets and `doctor --probe-tracker` all work — with no
   bespoke API client (ADR-0006 stays clean).

The seam is already provider-agnostic down to the transport lib; what is missing is
a way to *add a backend without every backend contending on the same shared files*.
This ADR records the design; the substrate lands in this initiative, the two
backends as follow-on lanes.

## Decision

Generalize the issue-tracker seam to a per-backend adapter surface. Five
sub-decisions:

1. **Semantic-name token vocabulary; passthrough mapping.** The read seam contract
   is unchanged: `<read_cmd> <issue>` prints **one opaque token** on stdout, which
   `sync` maps via `_wip_tracker_provider_to_semantic` and `doctor` compares
   literally. Fix the token vocabulary to be **wip's own semantic names**
   (`todo|in-progress|in-review|done|canceled`) and have each backend's *read
   command* own the provider-JSON → token reduction. Then for non-linear backends
   both provider-state maps are **pure passthrough** — the existing `*)` arms in
   `wip-plumbing-tracker-transport-lib.bash` already do this. **Consequence:
   github/gitlab need zero arms in `_wip_tracker_provider_state` /
   `_wip_tracker_provider_to_semantic`.** No contract change; a label-based state
   fits the existing seam.

2. **Per-backend adapter files + a dispatcher.** A new `lib/wip/tracker-backends/`
   directory holds one file per backend (`github.bash`, `gitlab.bash`), each
   defining `_wip_tracker_<backend>_{read,write}_cmd`. The transport lib turns its
   two command-seam functions into dispatchers and glob-sources the directory.
   Resolution precedence, highest to lowest: generic **`WIP_TRACKER_{READ,WRITE}_CMD`**
   (any backend, top test seam) → the **backend adapter fn** (which honors its own
   `WIP_GITHUB_*` / `WIP_GITLAB_*` before emitting its default CLI string) → the
   inline **`linear)`** arm (`WIP_LINEAR_*`, else empty — the agent/MCP path). This
   is a deliberate **departure from `forge-lib`'s inline-`case` style**, justified
   *solely* by ADR-0010: github and gitlab must land as concurrent lanes on
   disjoint files, which an inline `case` in one shared function forbids. Linear
   **stays inline** (minimal churn; preserves its empty-by-default MCP behavior and
   its green tests).

3. **`transport: cli` bypasses the MCP apply path.** When a backend resolves a
   write command, `sync` applies transitions in-process and reports `applied` /
   `observed` / `skipped`. `commands/sync.md` dispatches on the envelope's
   `transport`: `cli` renders the already-applied buckets with no agent-side apply
   and no re-read guard; `mcp` (Linear default) keeps the live re-read
   forward-guard. The inline rank map in `sync.md` is **scoped to the `mcp` branch**
   and relabeled *Linear provider-state → rank* — it exists only because the pure
   MCP path has no plumbing ranking verb; a future `wip tracker rank` verb would
   remove it.

4. **The `[tracker: ID]` id syntax widens.** ADR-0024's marker id sub-pattern
   (`[A-Z][A-Z0-9]*-[0-9]+`, Linear-key-shaped) widens to the union
   `([A-Z][A-Z0-9]*-[0-9]+|([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+)?#[0-9]+)`, parsing
   `BDS-22`, `#123`, `octocat/hello#123`, and nested `grp/sub/proj#45`. Greediness
   is safe (`[A-Za-z0-9._-]` excludes `]`, `#`, space). The regex is **mirrored, not
   factored**, across `wip-plumbing-tracker-lib.bash` (`_wip_tracker_id_valid`) and
   `wip-plumbing-roadmap-lib.bash` (`_wip_roadmap_extract_tracker` + the round/lane
   heading strips), because `test-roadmap-parse.sh` sources roadmap-lib in
   isolation and a shared constant would be undefined there; the two copies must
   stay in step.

5. **Backend selection is config-only, never probed.** Unlike the forge selector
   (ADR-0022, where a binary probe is a zero-config fallback), the tracker backend
   is **only** ever `features.issue-tracker.backend` (a required config value written
   by `setup issue-tracker <backend>`), with `WIP_TRACKER_*` env as the process/test
   override. There is no binary probe and no auto-detection — so ADR-0022's
   "mixed-environment mis-selection" failure mode cannot arise here by construction.

## Consequences

- **Two follow-on lanes, disjoint by construction.** With the dispatcher in place,
  BDS-75 touches only `lib/wip/tracker-backends/github.bash` + its test, and BDS-76
  only `lib/wip/tracker-backends/gitlab.bash` + its test. Every shared-file edit
  (id regex, dispatcher, `sync`/`doctor`/`setup`, `commands/sync.md`, Makefile,
  de-pinning `test-tracker-transport.sh:36`) is quarantined in this substrate.
- **Build-system fan-out.** The new `lib/wip/tracker-backends/` subdir must be added
  to the Makefile `$(SRC)` glob or the adapters escape `shfmt -d` + `shellcheck -x`;
  the glob-source (no prior precedent in this repo) needs a `# shellcheck
  disable=SC1090` and an `[[ -e "$f" ]]` no-match guard.
- **`doctor --probe-linear` → `--probe-tracker`.** The probe flag is renamed to a
  backend-neutral name; `--probe-linear` is retained as a **deprecated alias**, and
  the `kind: "tracker-probe"` envelope was already neutral.
- **Labels must pre-exist (operational, recorded not solved).** The label-carried
  middle states mean `gh issue edit --add-label` (and the glab equivalent) require
  `wip:in-progress` / `wip:in-review` / `wip:canceled` to already exist in the
  target repo/project. Candidate follow-up: a `doctor` check or provisioning in
  `setup issue-tracker`.
- **No `features.issue-tracker.repo` field (deferred).** Qualified ids
  (`owner/repo#N`) are self-contained; bare `#N` resolves via gh/glab's own
  cwd-git-remote inference. A default-repo config field would force threading the
  manifest into the seam functions (which take only `<backend>`) — deferred as sugar,
  with the extension point recorded here.
- **BDS-23 unaffected.** The github/gitlab CLI transports are distinct from a Linear
  CLI wrapper; ADR-0019's deferral of a *Linear* CLI transport (BDS-23) still stands.
- Cost at step-01 is docs + decision only; no code moves here.

## Supersedes / Amends

This ADR **surgically amends** two prior ADRs. Only the clauses named below change;
everything else in those ADRs stands.

- **ADR-0019** — "Scope honesty: the CLI transport … is **deferred to BDS-23**; this
  initiative ships the agent/MCP transport first" is narrowed to **Linear**: a
  github/gitlab CLI transport ships now (§Decision 2–3). The provider-agnostic intent
  contract, the `tracker:` mapping key, the `todo/in-progress/in-review/done`
  vocabulary, and "no provider state names in plumbing" are **preserved** (§Decision 1
  keeps provider state out of plumbing — the token *is* the semantic name).
- **ADR-0024** — the `[tracker: ID]` marker id sub-pattern widens from
  `[A-Z][A-Z0-9]*-[0-9]+` to the union in §Decision 4. Node addressing
  (`<slug>/{step-NN,round-N,initiative}`), lane exclusion, the roadmap-authored
  step/round keys, and the intake-anchored `tracker_anchor` are **preserved**.

**Preserved explicitly** (so the amendment stays surgical):

- **ADR-0006** — no bespoke API client; wip wraps the tool's own CLI (`gh`/`glab`)
  and never reimplements provider actions.
- **ADR-0010** — a lane is a grouping, not a lifecycle node; the widened id regex
  still strips (never harvests) a `### Lane` heading's tracker key.
- **ADR-0022** — the forge selector's probe-as-fallback contract is untouched; the
  tracker backend is config-only by construction (§Decision 5).
