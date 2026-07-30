# `tiers` — decisions this Matter made

The design of record is the `tiers` Brief (`workplans/tiers.md`, external). This
file records what building `tier-verbs` *forced* — the calls the Brief left
implicit, and the places the implementation diverged from the Brief text.
Step-10 folded the divergences back into the Brief; this is the working record
it read, and it stays as the rationale the Brief's amended sentences are short
for.

Status: **`identity-rules`, `tier-verbs`, and `worked-examples` complete.**
The `tiers` Matter's seal condition — all seven worked examples produced
(rows, keys, `status` output), example 7 scoped to tiers/keys/`status` only;
tier verbs implemented and tested — is discharged. See
`worked-examples.md` for the captured transcripts. The `reviewed-local` gate
is the user's to close.

## What `tier-verbs` builds on

`schema` had already provisioned exactly the tier storage contract `tiers`
needs before this Matter ever opened its own Stage: `repos`/`clones`/`worktrees`
tables (ULID + one natural key + label column on Clone, `identity_remote`
column on Repo), the six tier event types
(`repo.attached`·`clone.attached`·`worktree.attached`·`repo.key-adopted`·
`clone.relinked`·`clone.labeled`) with their payload structs, and the projection
rules that fold them. Step-01's "confirm the contract" was genuinely a
confirmation, not new schema work — nothing in `internal/store`'s tables or
event taxonomy changed.

Two small, additive read methods were added to `store.View`
(`Repos`, `ClonesOfRepo`, `WorktreesOfClone`, `WorktreeByName`) and one helper
to `internal/store` (`IsIdentityShaped`, `paths.go`'s `WIP_DB_PATH` test
override) — pure reads and a shape-check already implicit in `ulid.go`, plus
the same escape-hatch-env-var pattern `manifest-install`'s
`WIP_CLAUDE_SKILLS_DIR` established. No table, event type, or payload shape
changed; `schema`'s own seal condition is untouched.

## Divergences folded into the Brief (step-10)

1. **`wip init` also births the Worktree row for the location it ran from.**
   Not stated in the original "Move detection" section. A Clone row alone
   cannot make the cursor or `status` work — D38 keys the cursor at Clone
   *and* Worktree — so every `init` births exactly one Worktree row alongside
   whatever else it births: empty name (main) when `git rev-parse --git-dir`
   and `--git-common-dir` agree, or git's own worktree name (the linked
   worktree's target directory basename by default — confirmed empirically;
   it is *not* the branch checked out into it) when they don't. This is
   exactly what worked example 2 needs: a second `wip init`, run inside a
   linked worktree of an already-known Clone, creates only the Worktree row.

2. **`validation.already-initialized`.** A guard the Brief never named:
   re-running `init` against a (Clone, Worktree) pair it already knows would
   otherwise surface the store's own `UNIQUE constraint failed` as a raw Go
   error. Added so a duplicate `init` fails cleanly instead.

3. **Zero remotes vs. remotes-but-no-match.** The Brief's "no origin, no
   override → refuse" sentence read ambiguously as to whether a clone with
   *no remotes at all* was included. Read (and now stated) as: the refusal is
   specifically "declining to guess *among the remaining remotes*" — which
   presupposes remotes exist. Zero remotes is unambiguously the local-only
   case the very next Brief section covers, and never reaches this refusal.

4. **`doctor`'s remote match is broader than `init`'s identity-remote
   convention.** `doctor` checks every remote configured at the current
   location (sorted by name, for determinism) against known Repos, not just
   `origin`/`--identity-remote`. It is asking "is this place known under any
   name" — a different question from `init`'s "which remote is this Repo's
   identity" — and there is no committed Repo yet here to have an
   `identity_remote` recorded against in the first place.

5. **`refusal.unknown-clone`'s actual emitters.** `vocabulary`'s ratified
   draft (step-10) illustrates the message with `status:` in the verb slot,
   but that slot is filled by `chassis`'s renderer with whichever verb
   actually raised it — not baked into the ratified text. In this Matter the
   emitters are `doctor` (remote-also-unknown fallthrough), `label`, and
   `clone list` (no `--repo` given, current Clone unresolved). `status` never
   raises it, per the Brief's own Read-scope exception — no conflict, just
   worth stating plainly since the illustrative verb name could otherwise
   read as a commitment.

6. **`--repo` label addressing is written generally but resolves against an
   always-empty column in P1.** `schema` provisioned `repos.label` ahead of
   this Brief, apparently for exactly this addressing convention, but no
   `tiers` Step ever populates it — only Clone labels are addressable in
   `tier-verbs`. `ResolveRepo`'s label-dispatch branch is implemented anyway
   (against the real column), so a later Matter that starts writing
   `repos.label` needs no resolver change. Stated honestly in the Brief now,
   rather than left to be discovered as a silent no-op.

7. **Port-omission generalized beyond ssh.** The Brief states the port rule
   for ssh only (22, the one case the worked table exercises); implemented
   generally against each scheme's own default (80 http, 443 https, 9418 git)
   since the stated principle — "omitted when it equals the scheme's
   default" — was already scheme-general in its own wording.

None of these are reinterpretations of a rule the Brief stated — each is
either a gap the Brief's prose left open (folded in with a reading), or a
consequence forced by making the identity rules actually work end to end
(the Worktree-birth fact, the already-initialized guard). Every worked
example this Matter's next Stage produces is expected to pass against the
Brief as amended here without a second reconciliation.

## Where this stands

`internal/tiers` implements the whole of `tier-verbs`: remote-URL
normalization (`remote.go`), the git shell wrapper (`git.go`), the
collision-suggestion label algorithm (`label.go`), `wip init`'s birth/attach
logic (`init.go`), remote adoption and current-clone resolution (`adopt.go`),
the addressing resolver (`resolve.go`), move detection and relink
(`move.go`), `wip label` (`relabel.go`), and the `status` tier-scoping stub
(`status.go`). Five verb packages
(`internal/verbs/{init,clone,label,doctor,status}`) wire these into Cobra per
the `chassis` Brief's conventions and are registered in `internal/cli.
NewRootCommand`.

Tests: `internal/tiers/*_test.go` covers every identity-rules behavior
step-09 names, against a real store and real git repos in temp directories
(no mocks — the same posture `schema`'s own tests took). `internal/cli/
tiers_e2e_test.go` covers the CLI wiring — exit codes and `--json` envelopes
— through the actual built binary, the same pattern `chassis`'s own
`e2e_test.go` established. `go build ./...`, `go vet ./...`, and `go test
./...` are clean.
