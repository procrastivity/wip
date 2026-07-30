# Dogfood session 01 — reconstructed CLI execution log

**Date:** 2026-07-30. **Scope:** the first live dogfood of the Go wip on
its own repo — install, intake of the Phase 1 deferred/found backlog,
four Matters worked to sealed, one entry declined. Reconstructed from the
driving agent's transcript, not from the store: `wip session` knows the
87 events, but the interleaving with builds, test runs and git commits
exists only in the transcript (see finding 3 below). Store IDs are real.
Coverage ends at the third gate-close batch; entries added afterward
(the two structural-gap entries) are not in this log.

Conventions: `# ✗` marks a command that failed, kept because the failure
was informative. `[outside wip: ...]` marks work wip brackets but does
not contain. `$M` abbreviates the long first-Matter locator,
`wip-next-the-no-cursor-view-must-see-in-progress-work` — itself
friction that became a worked Backlog entry.

```console
## ── Setup ────────────────────────────────────────────────────────
make build
ln -s /Users/beausimensen/Code/wip-go/bin/wip ~/.local/bin/wip
wip version
wip init                                  # attached clone "wip-go"
wip install claude-code -v                # → ~/.claude/skills/wip
wip doctor                                # clean

## ── Intake: the five backlog items ───────────────────────────────
wip backlog add --title "release-engineering" --provenance deferred \
    --origin "scaffold planning (HANDOFF H7)" --detail "..."
# ✗ validation.unknown-locator — --origin must resolve to a store node
wip backlog add --title "release-engineering" --provenance deferred --detail "..."
# ✗ validation.missing-origin — deferred *requires* an origin node
wip backlog add --title "release-engineering"  --provenance intake --detail "Pushed out of scaffold planning (HANDOFF H7, pre-store)..."
wip backlog add --title "cutover-to-go-main"   --provenance intake --detail "..."
wip backlog add --title "install-target-codex" --provenance intake --detail "..."
wip backlog add --title "install-target-pi"    --provenance intake --detail "..."
wip backlog add --title "wip next reports EverythingSealed with work in progress" \
    --provenance found --detail "..."
wip backlog list

## ── Item 1: the wip-next bug (full lifecycle) ────────────────────
wip matter create --title "wip next: the no-cursor view must see in-progress work"
#   → locator wip-next-the-no-cursor-view-must-see-in-progress-work (friction)
wip gate declare reviewed-local --scale matter
wip backlog plan 01KYTKBZRB6P7Z2BGNXRJ1H881 $M
wip step create $M --title "Reproduce: failing test — ..."     # step-01
wip step create $M --title "Fix noCursorView ..."              # step-02
wip step create $M --title "make check green; align ..."       # step-03
wip start $M/step-01                      # auto-started 1 ancestor
wip next                                  # bug family live: in-progress work invisible
wip next --set $M/step-01
    # [outside wip: write failing test; go test → compile fail as expected]
wip finish $M/step-01
wip start $M/step-02 && wip next --set $M/step-02
    # [outside wip: implement InProgressNoCursor; gofumpt; go test → 9/9 pass]
wip finding add $M "Fix shape decided: a sixth view kind InProgressNoCursor ..."
wip finish $M/step-02
wip start $M/step-03 && wip next --set $M/step-03
    # [outside wip: make check → exit 0; check vocabulary register upstream]
make build                                # installed binary now has the fix
wip finding add $M "Vocabulary alignment: ... upstream recommendation ..."
wip finish $M/step-03
wip finish $M                             # → awaiting gate; stopped for user
wip gate close reviewed-local $M          # user-approved → sealed
git commit  # da668db fix(read-surface): the no-cursor view must see in-progress work

## ── Items 2–4: plan only ─────────────────────────────────────────
wip matter create --title "release-engineering"
wip backlog plan 01KYTKBR5WRSFBZKAN6CRBVXTP release-engineering
wip workplan release-engineering --file <scratchpad>/release-engineering-workplan.md
wip step create release-engineering --title "..."   # ×5, left Planned
#   (same shape for install-target-codex, install-target-pi: create, plan,
#    workplan --file, step ×4 — after reading arch doc §4 outside wip)

## ── Intake: friction found so far ────────────────────────────────
wip backlog add --title "deferred provenance unusable for pre-store origins"      --provenance found --detail "..."
wip backlog add --title "matter create needs a locator override"                  --provenance found --detail "..."
wip backlog add --title "frontier floods next with planned steps of unstarted matters" --provenance found --detail "..."

## ── Friction A: locator override ─────────────────────────────────
wip matter create --title "matter locator override" --json
# ✗ refusal.unknown-clone — shell cwd was still wip-reboot; correct refusal
cd /Users/beausimensen/Code/wip-go
wip matter create --title "matter locator override"    # → matter-locator-override
wip backlog plan 01KYTM14F54CK343NKF3DP66WD matter-locator-override
wip step create matter-locator-override --title "Add --locator ..."
wip start matter-locator-override/step-01 && wip next --set .../step-01
    # [outside wip: birth.go, matter.go, 2 test helpers, e2e test; go test green]
make build                                # ← so the next Matter can use --locator
wip finding add matter-locator-override "Validation rule chosen: fixed point of slugify ..."
wip finish matter-locator-override/step-01 && wip finish matter-locator-override

## ── Friction B: frontier grain (+ found item D mid-flight) ───────
wip matter create --title "next candidates collapse to the outermost ready node" \
    --locator frontier-grain              # ← the flag built minutes earlier
wip backlog plan 01KYTM14FRGKADDTD3WNBQRKXJ frontier-grain
wip step create frontier-grain --title "Failing test: ..."      # step-01
wip step create frontier-grain --title "Collapse rule ..."      # step-02
wip start frontier-grain/step-01 && wip next --set frontier-grain/step-01
    # [outside wip: two tests — collapse fails (4 candidates), stage-shape passes]
wip finish frontier-grain/step-01
wip start frontier-grain/step-02 && wip next --set frontier-grain/step-02
    # [outside wip: CollapseReady; apply in next+status; suite → TestWorkedExample6 FAILS]
wip doctor                                # 2× advisory.stale-harness-artifact — root cause
wip backlog add --title "e2e doctor tests read the host's real skill install" \
    --provenance found --origin frontier-grain --detail "..."   # origin is a real node

wip matter create --title "e2e runs must not read the host's real skill install" \
    --locator e2e-skills-hermetic
wip backlog plan 01KYTMR4S1KCXQMGS69BD0W371 e2e-skills-hermetic
wip step create e2e-skills-hermetic --title "runIn defaults WIP_CLAUDE_SKILLS_DIR ..."
wip start e2e-skills-hermetic/step-01 && wip next --set .../step-01
    # [outside wip: hermeticEnv in 3 test files; make check → exit 0]
make build
wip finding add frontier-grain "Collapse rule: CollapseReady drops ..."
wip finish frontier-grain/step-02   && wip finish frontier-grain
wip finish e2e-skills-hermetic/step-01 && wip finish e2e-skills-hermetic
wip install claude-code                   # refresh stale projection
wip doctor                                # clean
wip status                                # "next to start": 3 rows, was 16

## ── Friction C + gates (user-approved) ───────────────────────────
wip backlog decline 01KYTM14EHCNA819VPY46TW83J --reason "Blessed as posture: D9 stays strict ..."
wip gate close reviewed-local matter-locator-override
wip gate close reviewed-local frontier-grain
wip gate close reviewed-local e2e-skills-hermetic
git commit  # 4559d66 feat(write-surface): --locator override on matter create
git commit  # 2489cad fix(read-surface): collapse candidates to the outermost ready node
git commit  # 5a56788 test(cli): default WIP_CLAUDE_SKILLS_DIR to a temp dir per test
wip status && wip backlog list && wip session      # 87 events, one working period
```

## Findings the log surfaces

1. **The per-step cadence held**: `start` → `next --set` → outside-wip
   work → optional `finding add` → `finish`. The cursor stayed honest the
   whole session; consulted from a stale position it reported the target
   sealed and offered candidates rather than guessing.
2. **Every failed command earned something.** The two `backlog add`
   refusals produced the provenance decision (later formally declined);
   the `unknown-clone` refusal was a correct tier boundary; the failing
   e2e suite produced item D with the first store-node `--origin` chain
   (`e2e-skills-hermetic ← frontier-grain`).
3. **The store cannot reconstruct this log.** The event log is wip's own
   narrative; the interleaving with `make build`, test runs and git
   commits lives only in the driving transcript. Commit linkage is
   forge-seam territory (appendix-forge: "commit landed" events) — the
   demand signal is now a Backlog entry.
4. **Worked-vs-planned shapes diverged deliberately.** Full workplan
   prose went only to the three planned-not-started Matters; the four
   worked bugfix Matters carried step titles + findings only. That shape
   judgment was tracked as its own Backlog entry and folded into the
   skill's judgment guidance.
