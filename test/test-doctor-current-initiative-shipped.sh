#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-current-initiative-shipped"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Stale `current_initiative` pointer (kind:"current-initiative"): the manifest's
# single top-level pointer names an initiative whose status is `shipped` or
# `archived` — state `closeout` (ADR-0016, step-04) resolves on its own runs, and
# that a manual edit or a pre-closeout-era run can leave behind.
#
# Testing posture is the OPPOSITE of §2b/§2j's (test-doctor-closeout.sh /
# test-doctor-closeout-round.sh), which pin that a shipped/archived initiative is
# SKIPPED. Here, being pointed at while shipped IS the drift — so these cases pin
# that the shipped initiative is FLAGGED, not skipped. The check is also one-shot
# (one scalar, no `.initiatives[]` loop), so there is no per-initiative fan-out to
# cover; what varies is the pointed-at initiative's status, and the null pointer.
#
# Every fixture keeps a healthy in-flight `demo` (one unshipped step) so the other
# closeout checks (§2b step-level, §2j round-level, §2l initiative-level) stay
# quiet and the pointer check under test is the only drift in play.

# run_doctor <tmp> — run doctor under WIP_ROOT; set globals OUT (json) and RC.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" bin/wip-plumbing doctor)"
  RC=$?
  set -e
}

# n_ci <selector?> — count current-initiative entries in OUT (optional jq filter).
n_ci() {
  local sel="${1:-true}"
  jq "[.checks[] | select(.kind==\"current-initiative\") | select($sel)] | length" <<<"$OUT"
}

# mkfix <tmp> <pointer> <other-status> — a two-initiative manifest: an in-flight
# `demo` (mid-flight roadmap: nothing to close out) and an `other` carrying
# <other-status>. `current_initiative` is set to <pointer>, or omitted entirely
# when <pointer> is empty.
mkfix() {
  local tmp="$1" pointer="$2" other_status="$3" s
  mkdir -p "$tmp/.wip/initiatives/demo" "$tmp/.wip/initiatives/other"
  {
    printf 'version: 1\n'
    printf 'features:\n'
    printf '  wip: { enabled: true, root: .wip }\n'
    [[ -n "$pointer" ]] && printf 'current_initiative: %s\n' "$pointer"
    printf 'initiatives:\n'
    printf '  - slug: demo\n'
    printf '    status: in-flight\n'
    printf '    roadmap: .wip/initiatives/demo/roadmap.md\n'
    printf '  - slug: other\n'
    printf '    status: %s\n' "$other_status"
    printf '    roadmap: .wip/initiatives/other/roadmap.md\n'
  } >"$tmp/.wip.yaml"
  for s in demo other; do
    cat >"$tmp/.wip/initiatives/$s/roadmap.md" <<'MD'
# Roadmap

## Round 1 — One

- **step-01 — First** — current.
MD
  done
}

# ── Case A: pointer names an in-flight initiative → quiet ────────────────────
# The healthy steady state: the pointer tracks the initiative actually in flight.
tmpA="$(wip_mktemp)"
mkfix "$tmpA" demo shipped
run_doctor "$tmpA"
assert_eq "0" "$RC" "pointer at in-flight: exit 0"
assert_eq "0" "$(n_ci)" "pointer at in-flight: no current-initiative entry"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "pointer at in-flight: zero drift"

# ── Case B: pointer names a SHIPPED initiative → flagged ─────────────────────
# The pin: unlike §2b/§2j, a shipped initiative is not skipped here — being
# pointed at while shipped is the drift itself.
tmpB="$(wip_mktemp)"
mkfix "$tmpB" other shipped
run_doctor "$tmpB"
assert_eq "4" "$RC" "pointer at shipped: exit 4"
assert_eq "1" "$(n_ci)" "pointer at shipped: one current-initiative entry"
assert_eq "1" "$(jq -r '.drift_count' <<<"$OUT")" \
  "pointer at shipped: the stale pointer is the ONLY drift"
assert_eq "current-initiative-shipped" \
  "$(jq -r '.checks[]|select(.kind=="current-initiative").status' <<<"$OUT")" \
  "pointer at shipped: status"
assert_eq "other" \
  "$(jq -r '.checks[]|select(.kind=="current-initiative").slug' <<<"$OUT")" \
  "pointer at shipped: slug names the pointed-at initiative"
assert_eq "shipped" \
  "$(jq -r '.checks[]|select(.kind=="current-initiative").initiative_status' <<<"$OUT")" \
  "pointer at shipped: reports the offending initiative's status"
assert_eq "true" \
  "$(jq -r '.checks[]|select(.kind=="current-initiative").fix
            | contains("point current_initiative at an in-flight initiative")' <<<"$OUT")" \
  "pointer at shipped: fix steers to a repoint (closeout cannot fix this pointer itself)"

# ── Case C: pointer names an ARCHIVED initiative → flagged the same way ──────
tmpC="$(wip_mktemp)"
mkfix "$tmpC" other archived
run_doctor "$tmpC"
assert_eq "4" "$RC" "pointer at archived: exit 4"
assert_eq "1" "$(n_ci '.slug=="other"')" "pointer at archived: flagged like shipped"
assert_eq "archived" \
  "$(jq -r '.checks[]|select(.kind=="current-initiative").initiative_status' <<<"$OUT")" \
  "pointer at archived: reports the offending initiative's status"

# ── Case D: no pointer at all → quiet ────────────────────────────────────────
# `current_initiative` absent (the between-initiatives state `closeout` writes
# when nothing else is in flight) is not drift.
tmpD="$(wip_mktemp)"
mkfix "$tmpD" "" shipped
run_doctor "$tmpD"
assert_eq "0" "$RC" "no pointer: exit 0"
assert_eq "0" "$(n_ci)" "no pointer: no current-initiative entry"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "no pointer: zero drift"

# ── Case E: pointer at a proposed/paused initiative → quiet ──────────────────
# Only shipped/archived are drift: a pointer at an initiative that has not been
# closed out is a live pointer, whatever its pre-flight status.
for st in proposed paused; do
  tmpE="$(wip_mktemp)"
  mkfix "$tmpE" other "$st"
  run_doctor "$tmpE"
  assert_eq "0" "$RC" "pointer at $st: exit 0"
  assert_eq "0" "$(n_ci)" "pointer at $st: no current-initiative entry"
done

test_summary
