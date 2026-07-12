#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="closeout-end-to-end"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the COMPOSED `closeout` verb driven END-TO-END through
# `bin/wip-plumbing closeout` — the initiative-level rung of the closeout ladder.
#
# The load-bearing half of this file is the REFUSE-UNLESS-ALL-SHIPPED guard.
# `closeout` is the only write verb in the family that is GATED, and a guard that
# never fires looks exactly like a guard that always passes — from the outside,
# on the positive fixture, they are indistinguishable. So the negative cases here
# are deliberately NON-DEGENERATE: case C's fixture has every step's BULLET ✅
# shipped and only the round-2 HEADING marker missing (precisely the drift §2j's
# doctor check exists to catch), and case E's fixture has every round heading
# marked with no steps under them (the vacuous `all([]) == true` trap). A guard
# that read the bullets instead of `rounds[].shipped`, or that dropped the
# `length > 0` clause, or that swapped `all` for `any`, would sail through the
# positive cases AND wrongly accept C or E.
#
# Every refusal case also asserts `.wip.yaml` is BYTE-IDENTICAL across the
# refused run: the guard must fire before ANY writer seam, so a partial write can
# never leak out of a refusal.
#
# Seam-internal mechanics (each writer's own status vocabulary, other-initiative
# isolation, dry-run no-write at the seam level) are owned by
# test-closeout-manifest-writer.sh and NOT re-tested here.
# Contract: ADR-0016; workplan step-04.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
export WIP_NOW=2026-07-12

# The comment `closeout` synthesizes for the 2-round / 3-step `allshipped`
# fixture at $WIP_NOW. Spelled out once, asserted against BOTH the JSON ledger
# and the on-disk YAML line comment — if the verb ever computed those two
# independently, this is what catches the drift.
COMMENT_EXPECTED='Round 2 closed 2026-07-12 (2 rounds shipped; 3 steps shipped)'

# setup_co [roadmap] [others] [pointer] [active] — fresh fixture in a new tmp
# root; sets the globals `tmp`, `manifest`, `roadmap`.
#
#   roadmap : allshipped   (default) every round heading ✅ marked, every step ✅.
#             unmarked-r2  every step's BULLET ✅ shipped, but round 2's HEADING
#                          marker was never written — the §2j drift. MUST refuse.
#             unshipped-r2 round 2's heading unmarked AND its step unshipped.
#             emptyrounds  both round headings ✅ marked but NO steps under them —
#                          the vacuous-pass trap. MUST refuse.
#   others  : one  (default) `other` in-flight — the sole repoint candidate.
#             none          no other in-flight initiative — pointer gets cleared.
#             two           `other` + `second` in-flight — the ambiguous case.
#   pointer : `current_initiative` value (default "demo"); "" omits the key.
#   active  : `active_step` value on demo (default "step-02"); "" omits the key.
#
# EVERY fixture also carries a `done` initiative with `status: shipped`. It is
# load-bearing twice over: it proves a shipped initiative is never miscounted as
# a repoint candidate (the `none` fixture would otherwise be vacuously "no other
# initiatives at all"), and it is the target for the `next-not-in-flight` refusal.
setup_co() {
  local rm="${1:-allshipped}" others="${2:-one}" pointer="${3-demo}" active="${4-step-02}"
  tmp="$(wip_mktemp)"
  mkdir -p "$tmp/.wip/initiatives/demo"

  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    [[ -n "$pointer" ]] && printf 'current_initiative: %s\n' "$pointer"
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    [[ -n "$active" ]] && printf '    active_step: %s\n' "$active"
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
    if [[ "$others" == "one" || "$others" == "two" ]]; then
      printf '  - slug: other\n'
      printf '    status: in-flight\n'
      printf '    roadmap: .wip/initiatives/other/roadmap.md\n'
    fi
    if [[ "$others" == "two" ]]; then
      printf '  - slug: second\n'
      printf '    status: in-flight\n'
      printf '    roadmap: .wip/initiatives/second/roadmap.md\n'
    fi
    printf '  - slug: done\n'
    printf '    status: shipped\n'
    printf '    roadmap: .wip/initiatives/done/roadmap.md\n'
  } >"$tmp/.wip.yaml"

  case "$rm" in
    allshipped)
      {
        printf '# Roadmap — demo\n\n'
        printf '## Round 1 — Build ✅ shipped 2026-05-02\n\n'
        printf -- '- **step-01 — Auth** ✅ shipped 2026-05-01 — done.\n'
        printf -- '- **step-02 — Tokens** ✅ shipped 2026-05-02 — done.\n\n'
        printf '## Round 2 — Polish ✅ shipped 2026-06-01\n\n'
        printf -- '- **step-03 — Rotation** ✅ shipped 2026-06-01 — done.\n'
      } >"$tmp/.wip/initiatives/demo/roadmap.md"
      ;;
    unmarked-r2)
      # Every STEP bullet is shipped; only the round-2 HEADING marker is missing.
      {
        printf '# Roadmap — demo\n\n'
        printf '## Round 1 — Build ✅ shipped 2026-05-02\n\n'
        printf -- '- **step-01 — Auth** ✅ shipped 2026-05-01 — done.\n'
        printf -- '- **step-02 — Tokens** ✅ shipped 2026-05-02 — done.\n\n'
        printf '## Round 2 — Polish\n\n'
        printf -- '- **step-03 — Rotation** ✅ shipped 2026-06-01 — done.\n'
      } >"$tmp/.wip/initiatives/demo/roadmap.md"
      ;;
    unshipped-r2)
      {
        printf '# Roadmap — demo\n\n'
        printf '## Round 1 — Build ✅ shipped 2026-05-02\n\n'
        printf -- '- **step-01 — Auth** ✅ shipped 2026-05-01 — done.\n'
        printf -- '- **step-02 — Tokens** ✅ shipped 2026-05-02 — done.\n\n'
        printf '## Round 2 — Polish\n\n'
        printf -- '- **step-03 — Rotation** — later.\n'
      } >"$tmp/.wip/initiatives/demo/roadmap.md"
      ;;
    emptyrounds)
      # Both headings ✅ marked, zero steps: `all()` over an empty array is TRUE.
      {
        printf '# Roadmap — demo\n\n'
        printf '## Round 1 — Build ✅ shipped 2026-05-02\n\n'
        printf '## Round 2 — Polish ✅ shipped 2026-06-01\n'
      } >"$tmp/.wip/initiatives/demo/roadmap.md"
      ;;
    *)
      printf 'setup_co: unknown roadmap variant %q\n' "$rm" >&2
      return 2
      ;;
  esac

  # Only `.wip.yaml` is a global here: `closeout` never writes the roadmap, so
  # every assertion in this file reads the manifest.
  manifest="$tmp/.wip.yaml"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing closeout "$@"; }

# snapshot — copy the current .wip.yaml aside; echo the snapshot path.
snapshot() {
  local s
  s="$(wip_mktemp)/wip.yaml"
  cp "$manifest" "$s"
  printf '%s\n' "$s"
}

status_of() {
  SLUG="$1" yq -r '(.initiatives[] | select(.slug == strenv(SLUG)) | .status) // ""' "$manifest"
}
comment_of() {
  SLUG="$1" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status | line_comment) // ""
  ' "$manifest"
}
active_of() {
  SLUG="$1" yq -r '(.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""' "$manifest"
}
pointer() { yq -r '.current_initiative // ""' "$manifest"; }

# ---------------------------------------------------------------------------
# Case A — positive: one invocation flips status (with the synthesized comment),
#   clears active_step, and repoints current_initiative at the sole other
#   in-flight initiative. Asserted BOTH in the ledger and by re-reading the
#   ACTUAL on-disk .wip.yaml — a right ledger over a wrong write (or the reverse)
#   is exactly what a ledger-only assertion would miss.
# ---------------------------------------------------------------------------
setup_co allshipped one
out="$(run demo)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "demo" "$(jq -r '.slug' <<<"$out")" "A: slug echo"
assert_eq "2026-07-12" "$(jq -r '.closed_date' <<<"$out")" "A: closed_date from WIP_NOW"
assert_eq "updated" "$(jq -r '.status_set' <<<"$out")" "A: status_set updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "A: active_step_cleared updated"
assert_eq "updated" "$(jq -r '.current_initiative.action' <<<"$out")" "A: current_initiative updated"
assert_eq "other" "$(jq -r '.current_initiative.value' <<<"$out")" "A: repointed at sole in-flight other"
assert_eq "null" "$(jq -r '.current_initiative.candidates' <<<"$out")" "A: no candidates key when unambiguous"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"
assert_eq "$COMMENT_EXPECTED" "$(jq -r '.comment' <<<"$out")" "A: ledger carries synthesized comment"
assert_eq "null" "$(jq -r '.dry_run' <<<"$out")" "A: dry_run absent without flag"
# On-disk proof — the writes actually landed.
assert_eq "shipped" "$(status_of demo)" "A: on-disk status shipped"
assert_eq "$COMMENT_EXPECTED" "$(comment_of demo)" "A: on-disk trailing comment"
assert_eq "" "$(active_of demo)" "A: on-disk active_step cleared"
assert_eq "other" "$(pointer)" "A: on-disk current_initiative repointed"
assert_eq "in-flight" "$(status_of other)" "A: other initiative untouched"

# ---------------------------------------------------------------------------
# Case B — idempotency: a clean re-run is changed:false AND writes nothing. The
#   BYTE-IDENTICAL check is the real assertion — "exit 0 twice" would pass even
#   against a verb that rewrote the file with a churned comment every run.
#   (Run 2's pointer now names `other`, not `demo`, so the repoint seam correctly
#   reports `skipped`: not ours to touch.)
# ---------------------------------------------------------------------------
setup_co allshipped one
run demo >/dev/null # run 1 — mutates
snap="$(snapshot)"
out2="$(run demo)" # run 2 — steady state
assert_eq "noop" "$(jq -r '.status_set' <<<"$out2")" "B: re-run status_set noop"
assert_eq "noop" "$(jq -r '.active_step_cleared' <<<"$out2")" "B: re-run active_step_cleared noop"
assert_eq "skipped" "$(jq -r '.current_initiative.action' <<<"$out2")" "B: re-run pointer not ours to touch"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "B: re-run changed false"
assert_cmp "$snap" "$manifest" "B: .wip.yaml BYTE-IDENTICAL across re-run"
out3="$(run demo)"
assert_eq "$out2" "$out3" "B: steady-state ledger stable (run2 == run3)"

# ---------------------------------------------------------------------------
# Case C — THE LOAD-BEARING NEGATIVE PIN. Round 1 is fully shipped and marked;
#   round 2's every STEP BULLET reads ✅ shipped but its HEADING marker was never
#   written. `closeout` reads `rounds[].shipped` — the round-level marker — so it
#   MUST refuse. A guard that re-derived "all shipped" from the bullets would
#   wrongly accept this fixture; that is the entire point of the case.
# ---------------------------------------------------------------------------
setup_co unmarked-r2 one
before="$(snapshot)"
set +e
out="$(run demo 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "C: unmarked round-2 heading refuses with exit 4"
assert_eq "not-all-shipped" "$(jq -r '.error.kind' <<<"$out")" "C: kind not-all-shipped"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "C: ok false"
assert_eq "closeout: 1 round(s) not yet shipped: 2" \
  "$(jq -r '.error.message' <<<"$out")" "C: message names the SPECIFIC unshipped round"
assert_cmp "$before" "$manifest" "C: .wip.yaml BYTE-IDENTICAL across the refused run"
assert_eq "in-flight" "$(status_of demo)" "C: status not flipped by a refused run"

# C2 — the refusal fires identically under --dry-run (a gate, not a write path).
setup_co unmarked-r2 one
before="$(snapshot)"
set +e
out="$(run demo --dry-run 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "C2: --dry-run refuses identically (exit 4)"
assert_eq "not-all-shipped" "$(jq -r '.error.kind' <<<"$out")" "C2: --dry-run kind not-all-shipped"
assert_cmp "$before" "$manifest" "C2: .wip.yaml untouched under refused --dry-run"

# ---------------------------------------------------------------------------
# Case D — the other half of the non-degenerate refusal: round 2's step is
#   genuinely unshipped. Same refusal, same byte-identical guarantee.
# ---------------------------------------------------------------------------
setup_co unshipped-r2 one
before="$(snapshot)"
set +e
out="$(run demo 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "D: genuinely-unshipped round 2 refuses with exit 4"
assert_eq "not-all-shipped" "$(jq -r '.error.kind' <<<"$out")" "D: kind not-all-shipped"
assert_cmp "$before" "$manifest" "D: .wip.yaml BYTE-IDENTICAL across the refused run"

# ---------------------------------------------------------------------------
# Case E — the vacuous-pass trap. BOTH round headings are ✅ marked, but neither
#   has any steps. `all()` over an empty array is TRUE in jq, so a guard missing
#   its `length > 0` clause would happily close an initiative with no work in it.
# ---------------------------------------------------------------------------
setup_co emptyrounds one
before="$(snapshot)"
set +e
out="$(run demo 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "E: empty-rounds roadmap refuses (no vacuous pass)"
assert_eq "not-all-shipped" "$(jq -r '.error.kind' <<<"$out")" "E: kind not-all-shipped"
assert_eq "closeout: roadmap has no rounds with steps — nothing to close" \
  "$(jq -r '.error.message' <<<"$out")" "E: message names the real reason"
assert_cmp "$before" "$manifest" "E: .wip.yaml BYTE-IDENTICAL across the refused run"

# ---------------------------------------------------------------------------
# Case F — positive under --dry-run: the FULL ledger is still computed (every
#   status word, the comment, the resolved pointer value) but nothing is written.
# ---------------------------------------------------------------------------
setup_co allshipped one
before="$(snapshot)"
out="$(run demo --dry-run)"
assert_eq "updated" "$(jq -r '.status_set' <<<"$out")" "F: dry-run status_set updated"
assert_eq "updated" "$(jq -r '.active_step_cleared' <<<"$out")" "F: dry-run active_step_cleared updated"
assert_eq "updated" "$(jq -r '.current_initiative.action' <<<"$out")" "F: dry-run pointer updated"
assert_eq "other" "$(jq -r '.current_initiative.value' <<<"$out")" "F: dry-run pointer value resolved"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "F: dry-run changed true"
assert_eq "$COMMENT_EXPECTED" "$(jq -r '.comment' <<<"$out")" "F: dry-run comment synthesized"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "F: dry_run true"
assert_cmp "$before" "$manifest" "F: .wip.yaml unwritten under --dry-run"

# ---------------------------------------------------------------------------
# Case G — zero other in-flight initiatives: the pointer is CLEARED entirely
#   (absence is the between-initiatives state). The fixture's `done` initiative
#   exists but is shipped — proving a shipped initiative is not a candidate.
# ---------------------------------------------------------------------------
setup_co allshipped none
out="$(run demo)"
assert_eq "updated" "$(jq -r '.current_initiative.action' <<<"$out")" "G: pointer updated (cleared)"
assert_eq "null" "$(jq -r '.current_initiative.value' <<<"$out")" "G: ledger value null on clear"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "G: changed true"
assert_eq "" "$(pointer)" "G: on-disk current_initiative key removed"
assert_not_grep "current_initiative" "$manifest" "G: the key itself is gone, not just emptied"

# ---------------------------------------------------------------------------
# Case H — more than one other in-flight initiative: AMBIGUOUS. The pointer is
#   left UNCHANGED (never guessed) and the ledger carries the candidates so a
#   human picks. The other two writers still ran — `changed` stays true.
# ---------------------------------------------------------------------------
setup_co allshipped two
out="$(run demo)"
assert_eq "ambiguous" "$(jq -r '.current_initiative.action' <<<"$out")" "H: pointer ambiguous"
assert_eq "other,second" \
  "$(jq -r '.current_initiative.candidates | join(",")' <<<"$out")" "H: candidates listed in manifest order"
assert_eq "demo" "$(jq -r '.current_initiative.value' <<<"$out")" "H: ledger reports the UNCHANGED pointer"
assert_eq "demo" "$(pointer)" "H: on-disk current_initiative left UNTOUCHED"
assert_eq "shipped" "$(status_of demo)" "H: the other two writers still ran"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "H: changed true (status + active_step)"

# ---------------------------------------------------------------------------
# Case I — `--next` overrides auto-resolution: the same 2-candidate fixture that
#   is ambiguous in case H resolves cleanly when the human names the successor.
# ---------------------------------------------------------------------------
setup_co allshipped two
out="$(run demo --next second)"
assert_eq "updated" "$(jq -r '.current_initiative.action' <<<"$out")" "I: --next resolves the ambiguity"
assert_eq "second" "$(jq -r '.current_initiative.value' <<<"$out")" "I: ledger value is the --next slug"
assert_eq "null" "$(jq -r '.current_initiative.candidates' <<<"$out")" "I: no candidates key when resolved"
assert_eq "second" "$(pointer)" "I: on-disk current_initiative points at --next"

# ---------------------------------------------------------------------------
# Case J — `--next` refusals (both locked decisions). Writing the pointer at a
#   dangling or already-shipped slug is the exact drift this verb exists to
#   eliminate, so the verb must not be able to introduce it through its own flag.
#   Both refuse BEFORE any writer runs — byte-identical .wip.yaml.
# ---------------------------------------------------------------------------

# J1 — --next names no initiative at all.
setup_co allshipped one
before="$(snapshot)"
set +e
out="$(run demo --next nope 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "J1: unknown --next exits 4"
assert_eq "unknown-initiative" "$(jq -r '.error.kind' <<<"$out")" "J1: kind unknown-initiative"
assert_cmp "$before" "$manifest" "J1: .wip.yaml untouched — refused before any writer"

# J2 — --next names an initiative that is itself already shipped.
setup_co allshipped one
before="$(snapshot)"
set +e
out="$(run demo --next 'done' 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "J2: already-shipped --next exits 4"
assert_eq "next-not-in-flight" "$(jq -r '.error.kind' <<<"$out")" "J2: kind next-not-in-flight (distinct word)"
assert_cmp "$before" "$manifest" "J2: .wip.yaml untouched — refused before any writer"

# J3 — --next names the initiative being closed (which this very run ships).
setup_co allshipped one
before="$(snapshot)"
set +e
out="$(run demo --next demo 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "J3: --next naming the closing initiative exits 4"
assert_eq "next-not-in-flight" "$(jq -r '.error.kind' <<<"$out")" "J3: kind next-not-in-flight"
assert_cmp "$before" "$manifest" "J3: .wip.yaml untouched"

# ---------------------------------------------------------------------------
# Case K — `--pr <ref>`: the one clause of the comment that is NOT derivable from
#   disk. Interpolated when given, and the clause is simply absent when not
#   (cases A/F above). Free-form string, never parsed back.
# ---------------------------------------------------------------------------
setup_co allshipped one
out="$(run demo --pr '#30 merged')"
assert_eq "$COMMENT_EXPECTED; PR #30 merged" \
  "$(jq -r '.comment' <<<"$out")" "K: --pr clause interpolated into the ledger comment"
assert_eq "$COMMENT_EXPECTED; PR #30 merged" \
  "$(comment_of demo)" "K: --pr clause landed in the on-disk YAML comment"

# ---------------------------------------------------------------------------
# Case L — the pointer names a DIFFERENT initiative: never touched (`skipped`),
#   even though this run legitimately ships `demo`.
# ---------------------------------------------------------------------------
setup_co allshipped one other
out="$(run demo)"
assert_eq "skipped" "$(jq -r '.current_initiative.action' <<<"$out")" "L: pointer elsewhere -> skipped"
assert_eq "other" "$(jq -r '.current_initiative.value' <<<"$out")" "L: ledger reports the untouched value"
assert_eq "other" "$(pointer)" "L: on-disk pointer left in place"
assert_eq "shipped" "$(status_of demo)" "L: the status writer still ran"

# ---------------------------------------------------------------------------
# Case M — arg-surface errors (mirrors ship's skeleton pins).
# ---------------------------------------------------------------------------
setup_co allshipped one
set +e
out="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "M: missing <slug> exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "M: missing slug kind usage"

set +e
out="$(run demo --bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "M: unknown flag exits 2"

set +e
out="$(run bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "M: unknown initiative exits 3 (ship parity)"
assert_eq "unknown-initiative" "$(jq -r '.error.kind' <<<"$out")" "M: unknown initiative kind"

test_summary
