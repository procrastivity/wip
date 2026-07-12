#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="closeout-manifest-writer"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the three `closeout` manifest writers (_wip_closeout_mark_shipped,
# _wip_closeout_clear_active_step, _wip_closeout_repoint_current_initiative),
# driven directly as units — the `closeout` verb does not exist yet (that is
# step-04 Chunk 2). Mirrors test-ship-round-writer.sh's direct-seam idiom and
# test-ship-manifest-writer.sh's MULTI-INITIATIVE fixture discipline: every
# assertion re-reads the manifest through `yq`, and every write is proven not to
# touch a DIFFERENT initiative's fields.
#
# Pure manifest read/write — no roadmap parsing at this layer. The seams take an
# explicit <manifest> path, so there is no WIP_ROOT to root: the fixture path is
# passed in directly.
# Contract: ADR-0016; workplan step-04 (initiative-level closeout writers).
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1

# shellcheck source=lib/wip/wip-plumbing-closeout-manifest-lib.bash
source lib/wip/wip-plumbing-closeout-manifest-lib.bash

COMMENT="Round 1 closed 2026-07-12 (its only round; 4 steps shipped)"

# setup_manifest [--other-status S] [--clean-status S] [--current SLUG|--no-current]
#
# Write a fresh MULTI-INITIATIVE fixture; set `manifest` (path) and `before` (a
# byte-for-byte snapshot taken before the writer runs):
#   demo  — in-flight, active_step: step-03   (the initiative under test)
#   other — in-flight, active_step: step-07   (proves OTHER initiatives are
#                                              never touched; also candidate #1)
#   clean — in-flight, NO active_step         (the already-absent case; cand #2)
# The statuses of `other`/`clean` are the knob that sets the candidate COUNT the
# repoint seam auto-resolves against: both in-flight → 2 (ambiguous), one
# in-flight → 1 (sole other), neither → 0 (clear the pointer).
setup_manifest() {
  local other_status="in-flight" clean_status="in-flight" current="demo"
  while (($#)); do
    case "$1" in
      --other-status)
        other_status="$2"
        shift 2
        ;;
      --clean-status)
        clean_status="$2"
        shift 2
        ;;
      --current)
        current="$2"
        shift 2
        ;;
      --no-current)
        current=""
        shift
        ;;
      *)
        printf 'setup_manifest: unknown opt %q\n' "$1" >&2
        return 2
        ;;
    esac
  done
  tmp="$(wip_mktemp)"
  manifest="$tmp/.wip.yaml"
  before="$tmp/.wip.yaml.before"
  {
    printf 'version: 1\n'
    printf 'features: { wip: { enabled: true, root: .wip } }\n'
    if [[ -n "$current" ]]; then
      printf 'current_initiative: %s\n' "$current"
    fi
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    printf '    active_step: step-03\n'
    printf '  - slug: other\n'
    printf '    status: %s\n' "$other_status"
    printf '    active_step: step-07\n'
    printf '  - slug: clean\n'
    printf '    status: %s\n' "$clean_status"
  } >"$manifest"
  cp "$manifest" "$before"
}

# --- yq re-readers (immune to formatting, unlike grep on a shared key name) ---
status_of() {
  SLUG="$1" yq -r '(.initiatives[] | select(.slug == strenv(SLUG)) | .status) // ""' "$manifest"
}
comment_of() {
  SLUG="$1" yq -r '
    (.initiatives[] | select(.slug == strenv(SLUG)) | .status | line_comment) // ""
  ' "$manifest"
}
active_step_of() {
  SLUG="$1" yq -r '(.initiatives[] | select(.slug == strenv(SLUG)) | .active_step) // ""' "$manifest"
}
current_of() { yq -r '.current_initiative // ""' "$manifest"; }
has_current() { yq -r 'has("current_initiative")' "$manifest"; }

# --- seam drivers: capture status word + return code without tripping `set -e` -
mark() {
  status=""
  rc=0
  status="$(_wip_closeout_mark_shipped "$manifest" "$@")" || rc=$?
}
clear_step() {
  status=""
  rc=0
  status="$(_wip_closeout_clear_active_step "$manifest" "$@")" || rc=$?
}
repoint() {
  status=""
  rc=0
  status="$(_wip_closeout_repoint_current_initiative "$manifest" "$@")" || rc=$?
}

# ===========================================================================
# _wip_closeout_mark_shipped
# ===========================================================================

# ---------------------------------------------------------------------------
# 1. Status transition: in-flight → shipped, carrying the trailing comment.
#    Neither of the other two initiatives is disturbed.
# ---------------------------------------------------------------------------
setup_manifest
mark demo "$COMMENT"
assert_eq "0" "$rc" "mark: return 0"
assert_eq "updated" "$status" "mark: status updated"
assert_eq "shipped" "$(status_of demo)" "mark: demo status is shipped"
assert_eq "$COMMENT" "$(comment_of demo)" "mark: demo carries the trailing comment"
assert_eq "in-flight" "$(status_of other)" "mark: other's status untouched"
assert_eq "in-flight" "$(status_of clean)" "mark: clean's status untouched"
assert_eq "step-07" "$(active_step_of other)" "mark: other's active_step untouched"

# ---------------------------------------------------------------------------
# 2. Idempotency no-op #1 — already `shipped` with the IDENTICAL comment is
#    `noop` AND leaves the file byte-identical. (A writer that rewrote the line
#    unchanged would still be a defect: it churns the file and defeats the
#    no-write guarantee a clean re-run depends on.)
# ---------------------------------------------------------------------------
cp "$manifest" "$before" # snapshot the post-mark state, then re-run onto it
mark demo "$COMMENT"
assert_eq "0" "$rc" "mark-noop: return 0"
assert_eq "noop" "$status" "mark-noop: identical status+comment → noop"
assert_cmp "$before" "$manifest" "mark-noop: .wip.yaml byte-identical (no write)"

# A DIFFERENT comment on an already-shipped initiative is `updated`, not `noop` —
# the comment text is part of the seam's equality check, not just the status word.
mark demo "Round 1 closed 2026-07-12 (revised comment)"
assert_eq "updated" "$status" "mark-recomment: differing comment → updated"
assert_eq "Round 1 closed 2026-07-12 (revised comment)" "$(comment_of demo)" \
  "mark-recomment: the new comment text landed"
assert_eq "shipped" "$(status_of demo)" "mark-recomment: still shipped"

# ---------------------------------------------------------------------------
# 3. --dry-run: the status word is still computed, but nothing is written.
# ---------------------------------------------------------------------------
setup_manifest
WIP_DRY_RUN=1 mark demo "$COMMENT"
assert_eq "0" "$rc" "mark-dry: return 0"
assert_eq "updated" "$status" "mark-dry: status still computed as updated"
assert_cmp "$before" "$manifest" "mark-dry: .wip.yaml byte-identical (no write)"
assert_eq "in-flight" "$(status_of demo)" "mark-dry: demo still in-flight on disk"

# ---------------------------------------------------------------------------
# 4. Backstop: an unknown slug is an internal error (1), not a silent `updated`
#    over a write that matched nothing.
# ---------------------------------------------------------------------------
mark nosuch "$COMMENT" 2>/dev/null
assert_eq "1" "$rc" "mark-unknown: unknown slug → return 1"
assert_eq "" "$status" "mark-unknown: no status word emitted"
assert_cmp "$before" "$manifest" "mark-unknown: .wip.yaml byte-identical (no write)"

# ===========================================================================
# _wip_closeout_clear_active_step
# ===========================================================================

# ---------------------------------------------------------------------------
# 5. Unconditional clear: demo's active_step goes, regardless of which step it
#    named (no step-id match, unlike ship's version). `other` keeps its pointer.
# ---------------------------------------------------------------------------
setup_manifest
clear_step demo
assert_eq "0" "$rc" "clear: return 0"
assert_eq "updated" "$status" "clear: status updated"
assert_eq "" "$(active_step_of demo)" "clear: demo active_step removed"
assert_eq "step-07" "$(active_step_of other)" "clear: other's active_step untouched"
assert_eq "in-flight" "$(status_of demo)" "clear: demo's status untouched by this seam"

# ---------------------------------------------------------------------------
# 6. Idempotency no-op #2 — active_step already absent → `noop`, byte-identical.
#    Covered twice: the just-cleared initiative, and `clean`, which never had the
#    key at all.
# ---------------------------------------------------------------------------
cp "$manifest" "$before"
clear_step demo
assert_eq "noop" "$status" "clear-noop: re-run on the cleared key → noop"
assert_cmp "$before" "$manifest" "clear-noop: .wip.yaml byte-identical (no write)"

clear_step clean
assert_eq "0" "$rc" "clear-absent: return 0"
assert_eq "noop" "$status" "clear-absent: key never present → noop"
assert_eq "" "$(active_step_of clean)" "clear-absent: clean still has no active_step"
assert_cmp "$before" "$manifest" "clear-absent: .wip.yaml byte-identical (no write)"

# ---------------------------------------------------------------------------
# 7. --dry-run: reports `updated`, writes nothing.
# ---------------------------------------------------------------------------
setup_manifest
WIP_DRY_RUN=1 clear_step demo
assert_eq "updated" "$status" "clear-dry: status still computed as updated"
assert_cmp "$before" "$manifest" "clear-dry: .wip.yaml byte-identical (no write)"
assert_eq "step-03" "$(active_step_of demo)" "clear-dry: demo active_step still on disk"

# ===========================================================================
# _wip_closeout_repoint_current_initiative — all five resolution outcomes
# ===========================================================================

# ---------------------------------------------------------------------------
# 8. Outcome A — `--next` given: repoint to it, `updated`. The flag outranks
#    auto-resolution (here BOTH `other` and `clean` are in-flight, so without the
#    flag this fixture would be `ambiguous` — proving the flag actually decides).
# ---------------------------------------------------------------------------
setup_manifest
repoint demo other
assert_eq "0" "$rc" "next-flag: return 0"
assert_eq "updated" "$status" "next-flag: status updated"
assert_eq "other" "$(current_of)" "next-flag: current_initiative repointed to other"
assert_eq "in-flight" "$(status_of demo)" "next-flag: demo's status untouched by this seam"
assert_eq "step-03" "$(active_step_of demo)" "next-flag: demo's active_step untouched by this seam"

# The degenerate `--next` naming the slug being closed: pointer already equals it.
setup_manifest
repoint demo demo
assert_eq "noop" "$status" "next-flag-degenerate: --next == the closed slug → noop"
assert_eq "demo" "$(current_of)" "next-flag-degenerate: pointer left as-is"
assert_cmp "$before" "$manifest" "next-flag-degenerate: .wip.yaml byte-identical"

# --dry-run on the repoint path: status computed, nothing written.
setup_manifest
WIP_DRY_RUN=1 repoint demo other
assert_eq "updated" "$status" "next-flag-dry: status still computed as updated"
assert_cmp "$before" "$manifest" "next-flag-dry: .wip.yaml byte-identical (no write)"
assert_eq "demo" "$(current_of)" "next-flag-dry: pointer unchanged on disk"

# ---------------------------------------------------------------------------
# 9. Outcome B — exactly ONE other in-flight initiative: auto-repoint to it,
#    `updated`. (`clean` is archived here, so `other` is the sole candidate.)
# ---------------------------------------------------------------------------
setup_manifest --clean-status archived
repoint demo
assert_eq "0" "$rc" "sole-other: return 0"
assert_eq "updated" "$status" "sole-other: status updated"
assert_eq "other" "$(current_of)" "sole-other: auto-repointed to the sole in-flight initiative"
assert_eq "archived" "$(status_of clean)" "sole-other: clean's status untouched"

# ---------------------------------------------------------------------------
# 10. Outcome C — ZERO other in-flight initiatives: the key is deleted entirely,
#     `updated`. A cleared pointer is itself meaningful "between initiatives"
#     state (absence, not an empty string / null placeholder) — so assert the KEY
#     IS GONE, not merely that it reads as empty.
# ---------------------------------------------------------------------------
setup_manifest --other-status shipped --clean-status archived
repoint demo
assert_eq "0" "$rc" "zero-others: return 0"
assert_eq "updated" "$status" "zero-others: status updated"
assert_eq "false" "$(has_current)" "zero-others: current_initiative key deleted entirely"
assert_eq "shipped" "$(status_of other)" "zero-others: other's status untouched"
assert_eq "step-07" "$(active_step_of other)" "zero-others: other's active_step untouched"

# ---------------------------------------------------------------------------
# 11. Outcome D — the pointer names a DIFFERENT initiative: `skipped`, untouched.
#     This is ship's `skipped` meaning exactly: not ours to touch. It outranks
#     `--next` (asserted below), and it also covers an ABSENT pointer.
# ---------------------------------------------------------------------------
setup_manifest --current other
repoint demo
assert_eq "0" "$rc" "pointer-elsewhere: return 0"
assert_eq "skipped" "$status" "pointer-elsewhere: status skipped"
assert_eq "other" "$(current_of)" "pointer-elsewhere: pointer left aimed at other"
assert_cmp "$before" "$manifest" "pointer-elsewhere: .wip.yaml byte-identical (no write)"

# Resolution order pin: rule 1 (pointer elsewhere) OUTRANKS rule 2 (--next).
# A seam that checked --next first would wrongly hijack another initiative's
# pointer — this is the assertion that catches it.
setup_manifest --current other
repoint demo clean
assert_eq "skipped" "$status" "pointer-elsewhere+next: --next does NOT override skipped"
assert_eq "other" "$(current_of)" "pointer-elsewhere+next: pointer still aimed at other"
assert_cmp "$before" "$manifest" "pointer-elsewhere+next: .wip.yaml byte-identical"

# An absent current_initiative is likewise not ours to touch.
setup_manifest --no-current
repoint demo
assert_eq "skipped" "$status" "pointer-absent: status skipped"
assert_eq "false" "$(has_current)" "pointer-absent: no pointer conjured into existence"
assert_cmp "$before" "$manifest" "pointer-absent: .wip.yaml byte-identical (no write)"

# ---------------------------------------------------------------------------
# 12. Outcome E — MORE THAN ONE other in-flight initiative: `ambiguous`. The
#     pointer is left UNCHANGED (still naming the closed initiative) and the
#     caller reports the candidates so a human picks. `ambiguous` is a distinct
#     word from `skipped`: conflating them would make a caller's ship-derived
#     3-word assumption silently wrong.
# ---------------------------------------------------------------------------
setup_manifest # other + clean both in-flight → 2 candidates
repoint demo
assert_eq "0" "$rc" "ambiguous: return 0"
assert_eq "ambiguous" "$status" "ambiguous: 2 candidates → ambiguous (not skipped)"
assert_eq "demo" "$(current_of)" "ambiguous: pointer left UNCHANGED"
assert_cmp "$before" "$manifest" "ambiguous: .wip.yaml byte-identical (no write)"

# The candidate list the caller's ledger reports — the same helper the seam
# counts with, so the ledger and the write decision cannot drift.
cands="$(_wip_closeout_inflight_candidates "$manifest" demo | tr '\n' ',')"
assert_eq "other,clean," "$cands" "ambiguous: candidates are the other in-flight slugs"

# The helper excludes the closed slug itself, and excludes non-in-flight statuses.
setup_manifest --other-status shipped --clean-status archived
assert_eq "" "$(_wip_closeout_inflight_candidates "$manifest" demo)" \
  "candidates: shipped/archived initiatives are not candidates"

test_summary
