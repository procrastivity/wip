#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-initiative-shipped-not-closed"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Initiative-level closeout drift (kind:"closeout-initiative"): an initiative whose
# every non-empty round carries the round-level `✅ shipped` heading marker, yet
# whose manifest `status` is still `in-flight` — i.e. `closeout <slug>` (ADR-0016,
# step-04) would succeed right now and has not been run. The check predicts exactly
# that, using the SAME `length > 0 and all(.shipped)` predicate as the verb's own
# refuse-unless-all-shipped guard.
#
# Scope INVERTS §2b/§2j's skip-guard: only `status: in-flight` is considered.
# `shipped`/`archived` are already closed out; `proposed` (a roadmap written out in
# advance) and `paused` (intentionally not progressing) are not closeout omissions.
#
# Shipped steps get an archived workplan so the step-level check (§2b) stays quiet,
# and every fully-shipped round's heading carries its marker so the round-level
# check (§2j) stays quiet too — the initiative-level check under test is then the
# only drift in play.

# run_doctor <tmp> — run doctor under WIP_ROOT; set globals OUT (json) and RC.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" bin/wip-plumbing doctor)"
  RC=$?
  set -e
}

# n_init <selector?> — count closeout-initiative entries (optional jq filter).
n_init() {
  local sel="${1:-true}"
  jq "[.checks[] | select(.kind==\"closeout-initiative\") | select($sel)] | length" <<<"$OUT"
}

# mkfix <tmp> — in-flight `demo`: manifest + initiative dir + archive/.
mkfix() {
  local tmp="$1"
  wip_fixture_init "$tmp" --no-active-step
  mkdir -p "$tmp/.wip/initiatives/demo/archive"
}

# archive <tmp> <step-id> — mark a step archived (satisfies the §2b step check).
archive() {
  : >"$1/.wip/initiatives/demo/archive/$2-workplan.md"
}

# ── Case A: in-flight with an unshipped round → quiet ────────────────────────
# The ordinary mid-flight state: round 1 is closed out, round 2 is still open, so
# `closeout demo` would refuse and doctor must not suggest it.
tmpA="$(wip_mktemp)"
mkfix "$tmpA"
cat >"$tmpA/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two

- **step-02 — Second** — current.
MD
archive "$tmpA" step-01
run_doctor "$tmpA"
assert_eq "0" "$RC" "unshipped round: exit 0"
assert_eq "0" "$(n_init)" "unshipped round: no closeout-initiative entry"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "unshipped round: zero drift"

# ── Case B: the transition — round 2 lands too, status still in-flight → drift ─
# Mutates Case A's fixture in place: the pin is the transition (the moment closeout
# becomes possible), not two unrelated static states.
cat >"$tmpA/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two ✅ shipped 2026-05-02

- **step-02 — Second** ✅ shipped 2026-05-02 — done.
MD
archive "$tmpA" step-02
run_doctor "$tmpA"
assert_eq "4" "$RC" "every round shipped, still in-flight: exit 4"
assert_eq "1" "$(n_init)" "every round shipped, still in-flight: one entry"
assert_eq "1" "$(jq -r '.drift_count' <<<"$OUT")" \
  "every round shipped, still in-flight: the unclosed initiative is the ONLY drift"
assert_eq "initiative-shipped-not-closed" \
  "$(jq -r '.checks[]|select(.kind=="closeout-initiative").status' <<<"$OUT")" \
  "every round shipped, still in-flight: status"
assert_eq "demo" \
  "$(jq -r '.checks[]|select(.kind=="closeout-initiative").slug' <<<"$OUT")" \
  "every round shipped, still in-flight: offending slug"
assert_eq "run wip-plumbing closeout demo" \
  "$(jq -r '.checks[]|select(.kind=="closeout-initiative").fix' <<<"$OUT")" \
  "every round shipped, still in-flight: fix names the verb that closes it"

# ── Case C: an unmarked round heading is NOT enough → quiet ──────────────────
# Round 2's steps all read ✅ shipped but its HEADING marker was never written, so
# `closeout` would refuse (it reads the round marker, never re-derives it from the
# bullets). The check must agree — the round-level check (§2j) owns this drift.
tmpC="$(wip_mktemp)"
mkfix "$tmpC"
cat >"$tmpC/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two

- **step-02 — Second** ✅ shipped 2026-05-02 — done.
MD
archive "$tmpC" step-01
archive "$tmpC" step-02
run_doctor "$tmpC"
assert_eq "0" "$(n_init)" "round marker missing: no closeout-initiative entry"
assert_eq "1" \
  "$(jq '[.checks[] | select(.kind=="closeout-round")] | length' <<<"$OUT")" \
  "round marker missing: §2j owns this drift instead"

# ── Case D: only empty rounds never false-positive ───────────────────────────
# `all(.shipped)` over an EMPTY array is vacuously true in jq, so without the
# `length > 0` guard an initiative whose roadmap is nothing but placeholder rounds
# would read as "fully shipped" and be flagged as closeable. Mirrors case E of
# test-doctor-closeout-round.sh, and the same trap the verb's own guard covers.
tmpD="$(wip_mktemp)"
mkfix "$tmpD"
cat >"$tmpD/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

## Round 2 — Two
MD
run_doctor "$tmpD"
assert_eq "0" "$RC" "only empty rounds: exit 0"
assert_eq "0" "$(n_init)" "only empty rounds: no closeout-initiative entry"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "only empty rounds: zero drift"

# ...while an EMPTY round alongside a fully-shipped one does not SUPPRESS the flag
# either: the predicate ignores empty rounds, it is not defeated by them.
cat >"$tmpD/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.

## Round 2 — Two
MD
archive "$tmpD" step-01
run_doctor "$tmpD"
assert_eq "4" "$RC" "empty round beside a shipped one: exit 4"
assert_eq "1" "$(n_init)" "empty round beside a shipped one: still flagged"

# ── Case E: scope — only in-flight initiatives are considered ────────────────
# The inverted skip-guard: every initiative below has an identical all-shipped
# roadmap; only the in-flight one is closeout drift. shipped/archived are already
# closed out; proposed/paused are not closeout omissions.
tmpE="$(wip_mktemp)"
for s in demo legacy attic draft onhold; do
  mkdir -p "$tmpE/.wip/initiatives/$s/archive"
done
cat >"$tmpE/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  wip: { enabled: true, root: .wip }
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
  - slug: legacy
    status: shipped
    roadmap: .wip/initiatives/legacy/roadmap.md
  - slug: attic
    status: archived
    roadmap: .wip/initiatives/attic/roadmap.md
  - slug: draft
    status: proposed
    roadmap: .wip/initiatives/draft/roadmap.md
  - slug: onhold
    status: paused
    roadmap: .wip/initiatives/onhold/roadmap.md
YAML
for s in demo legacy attic draft onhold; do
  cat >"$tmpE/.wip/initiatives/$s/roadmap.md" <<'MD'
# Roadmap

## Round 1 — One ✅ shipped 2026-05-01

- **step-01 — First** ✅ shipped 2026-05-01 — done.
MD
  : >"$tmpE/.wip/initiatives/$s/archive/step-01-workplan.md"
done
run_doctor "$tmpE"
assert_eq "4" "$RC" "scope: the in-flight initiative still trips closeout drift"
assert_eq "1" "$(n_init)" "scope: exactly one closeout-initiative entry (demo only)"
assert_eq "1" "$(n_init '.slug=="demo"')" "scope: demo (in-flight) is checked"
assert_eq "0" "$(n_init '.slug=="legacy"')" "scope: legacy (shipped) is skipped"
assert_eq "0" "$(n_init '.slug=="attic"')" "scope: attic (archived) is skipped"
assert_eq "0" "$(n_init '.slug=="draft"')" "scope: draft (proposed) is skipped"
assert_eq "0" "$(n_init '.slug=="onhold"')" "scope: onhold (paused) is skipped"

test_summary
