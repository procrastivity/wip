#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-closeout"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Closeout-drift checks (kind:"closeout"): per roadmap step the `✅ shipped`
# marker must agree with whether the workplan is archived (pure-disk, no git).
#   archived ∧ ¬marked → "half-done-closeout"  (+ fix hint)
#   marked ∧ ¬archived → "shipped-not-archived"
# Healthy steps (marked == archived) add NO entry. The check runs only for
# in-flight/proposed initiatives (status: shipped/archived are skipped).
# Fixtures mirror test-doctor.sh's tmpdir idiom: a minimal in-flight `demo`
# initiative with a roadmap + an archive/ tree, driven through WIP_ROOT.

# mkfix <tmp> — scaffold an in-flight `demo` initiative: manifest + initiative
# dir + empty archive/. Caller writes roadmap.md and archive files. Only the
# `wip` feature is enabled, so the fixture has no non-closeout drift of its own.
mkfix() {
  local tmp="$1"
  mkdir -p "$tmp/.wip/initiatives/demo/archive"
  cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  wip: { enabled: true, root: .wip }
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
}

# run_doctor <tmp> — run doctor under WIP_ROOT; set globals OUT (json) and RC.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" bin/wip-plumbing doctor)"
  RC=$?
  set -e
}

# n_closeout <selector?> — count closeout entries in OUT (optional jq filter).
n_closeout() {
  local sel="${1:-true}"
  jq "[.checks[] | select(.kind==\"closeout\") | select($sel)] | length" <<<"$OUT"
}

# ── Case A: healthy — shipped+archived AND unshipped+unarchived → clean ──────
tmpA="$(mktemp -d)"
trap 'rm -rf "$tmpA"' EXIT
mkfix "$tmpA"
cat >"$tmpA/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
MD
: >"$tmpA/.wip/initiatives/demo/archive/step-01-first-workplan.md"
: >"$tmpA/.wip/initiatives/demo/archive/step-01-rolling-context.md"
run_doctor "$tmpA"
assert_eq "0" "$RC" "healthy: doctor exits 0"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "healthy: zero drift"
assert_eq "0" "$(n_closeout)" "healthy: no closeout entries"

# ── Case B: half-done-closeout — archived ∧ ¬marked → entry + exit 4 ─────────
tmpB="$(mktemp -d)"
mkfix "$tmpB"
cat >"$tmpB/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — current.
MD
: >"$tmpB/.wip/initiatives/demo/archive/step-01-first-workplan.md"
run_doctor "$tmpB"
assert_eq "4" "$RC" "half-done-closeout: exit 4"
assert_eq "1" "$(n_closeout)" "half-done-closeout: one closeout entry"
assert_eq "half-done-closeout" \
  "$(jq -r '.checks[]|select(.kind=="closeout").status' <<<"$OUT")" \
  "half-done-closeout: status"
assert_eq "step-01" \
  "$(jq -r '.checks[]|select(.kind=="closeout").step' <<<"$OUT")" \
  "half-done-closeout: offending step"
assert_eq "demo" \
  "$(jq -r '.checks[]|select(.kind=="closeout").slug' <<<"$OUT")" \
  "half-done-closeout: offending slug"
assert_eq "run wip ship demo step-01" \
  "$(jq -r '.checks[]|select(.kind=="closeout").fix' <<<"$OUT")" \
  "half-done-closeout: fix hint"
rm -rf "$tmpB"

# ── Case C: shipped-not-archived — marked ∧ ¬archived → entry + exit 4 ───────
tmpC="$(mktemp -d)"
mkfix "$tmpC"
cat >"$tmpC/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
MD
# archive/ is empty: shipped but never archived.
run_doctor "$tmpC"
assert_eq "4" "$RC" "shipped-not-archived: exit 4"
assert_eq "1" "$(n_closeout)" "shipped-not-archived: one closeout entry"
assert_eq "shipped-not-archived" \
  "$(jq -r '.checks[]|select(.kind=="closeout").status' <<<"$OUT")" \
  "shipped-not-archived: status"
assert_eq "step-01" \
  "$(jq -r '.checks[]|select(.kind=="closeout").step' <<<"$OUT")" \
  "shipped-not-archived: offending step"
rm -rf "$tmpC"

# ── Case D: rolling-context sidecar is NOT a workplan → no entry ─────────────
tmpD="$(mktemp -d)"
mkfix "$tmpD"
cat >"$tmpD/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — current.
MD
# Only the sidecar is archived; the workplan itself is not.
: >"$tmpD/.wip/initiatives/demo/archive/step-01-rolling-context.md"
run_doctor "$tmpD"
assert_eq "0" "$RC" "rolling-context-only: exit 0 (sidecar excluded)"
assert_eq "0" "$(n_closeout)" "rolling-context-only: no closeout entry"
rm -rf "$tmpD"

# ── Case E: prefix-collision guard — step-01 archived must not satisfy step-12 ─
tmpE="$(mktemp -d)"
mkfix "$tmpE"
cat >"$tmpE/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-12 — Twelfth** — current.
MD
# Only step-01 is archived; step-12 has no archived workplan.
: >"$tmpE/.wip/initiatives/demo/archive/step-01-first-workplan.md"
run_doctor "$tmpE"
assert_eq "0" "$RC" "prefix-collision: exit 0 (step-01 archive does not satisfy step-12)"
assert_eq "0" "$(n_closeout '.step=="step-12"')" \
  "prefix-collision: step-12 gets no closeout entry"
assert_eq "0" "$(n_closeout)" "prefix-collision: no closeout entries at all"
rm -rf "$tmpE"

# ── Case F: Option-C scope — status:shipped initiatives are skipped ──────────
# A legacy shipped initiative with a shipped-but-unarchived step must NOT trip
# closeout drift; an in-flight initiative in the same manifest still does.
tmpF="$(mktemp -d)"
mkdir -p "$tmpF/.wip/initiatives/demo/archive" "$tmpF/.wip/initiatives/legacy/archive"
cat >"$tmpF/.wip.yaml" <<'YAML'
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
YAML
cat >"$tmpF/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
MD
cat >"$tmpF/.wip/initiatives/legacy/roadmap.md" <<'MD'
# Roadmap — legacy

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-04-01 — done.
MD
run_doctor "$tmpF"
assert_eq "4" "$RC" "scope: in-flight initiative still trips drift"
assert_eq "1" "$(n_closeout)" "scope: exactly one closeout entry (demo only)"
assert_eq "1" "$(n_closeout '.slug=="demo"')" "scope: demo (in-flight) is checked"
assert_eq "0" "$(n_closeout '.slug=="legacy"')" "scope: legacy (shipped) is skipped"
rm -rf "$tmpF"

test_summary
