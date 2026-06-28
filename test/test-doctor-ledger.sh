#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-ledger"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Ledger-drift check (kind:"ledger", opt-in `doctor --probe-solo`): a shipped
# step must leave no OPEN `<slug>/step-NN` ledger entry — the invariant in
# roles/shared.md §Ledger Ownership & Completion (BDS-14). The ledger lives in
# the Solo control plane, so this is a LIVE probe; WIP_SOLO_TODOS_CMD overrides
# the `solo todos list` command (test seam). Fixtures use a file-based seam
# (`cat <file>`) so the canned JSON survives without shell brace-expansion.
#
# Fixtures: an in-flight `demo` with orchestration.backend: solo and a
# shipped+archived step-01 (so the 2b closeout check stays clean and only the
# ledger check can fire).

# mkfix <tmp> — scaffold the fixture; caller writes the seam JSON file.
mkfix() {
  local tmp="$1"
  mkdir -p "$tmp/.wip/initiatives/demo/archive"
  cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
current_initiative: demo
features:
  wip: { enabled: true, root: .wip }
  orchestration: { backend: solo }
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
  cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
MD
  : >"$tmp/.wip/initiatives/demo/archive/step-01-first-workplan.md"
}

# run_doctor <tmp> <seam-cmd|""> <flag...> — set globals OUT/RC.
run_doctor() {
  local tmp="$1" seam="$2"
  shift 2
  set +e
  OUT="$(WIP_ROOT="$tmp" WIP_SOLO_TODOS_CMD="$seam" bin/wip-plumbing doctor "$@")"
  RC=$?
  set -e
}

n_ledger() {
  local sel="${1:-true}"
  jq "[.checks[] | select(.kind==\"ledger\") | select($sel)] | length" <<<"$OUT"
}

# ── Case A: open entry tagged demo/step-01 (a shipped step) → drift + exit 4 ──
tmpA="$(mktemp -d)"
trap 'rm -rf "$tmpA"' EXIT
mkfix "$tmpA"
printf '%s' '{"data":{"todos":[{"completed":false,"tags":["demo/step-01","task"]}]}}' >"$tmpA/todos.json"
run_doctor "$tmpA" "cat $tmpA/todos.json" --probe-solo
assert_eq "4" "$RC" "open entry for shipped step: exit 4"
assert_eq "1" "$(n_ledger)" "one ledger entry"
assert_eq "shipped-step-open-ledger" "$(jq -r '.checks[]|select(.kind=="ledger").status' <<<"$OUT")" "status"
assert_eq "step-01" "$(jq -r '.checks[]|select(.kind=="ledger").step' <<<"$OUT")" "offending step"
assert_eq "demo" "$(jq -r '.checks[]|select(.kind=="ledger").slug' <<<"$OUT")" "offending slug"
assert_eq "1" "$(jq -r '.checks[]|select(.kind=="ledger").count' <<<"$OUT")" "open-entry count"

# ── Case B: same data, but WITHOUT --probe-solo → never probed, exit 0 ───────
run_doctor "$tmpA" "cat $tmpA/todos.json"
assert_eq "0" "$RC" "no --probe-solo: exit 0"
assert_eq "0" "$(n_ledger)" "no --probe-solo: no ledger check"

# ── Case C: open entry only for the UNSHIPPED step-02 → no drift, exit 0 ──────
tmpC="$(mktemp -d)"
mkfix "$tmpC"
printf '%s' '{"data":{"todos":[{"completed":false,"tags":["demo/step-02"]}]}}' >"$tmpC/todos.json"
run_doctor "$tmpC" "cat $tmpC/todos.json" --probe-solo
assert_eq "0" "$RC" "open entry for unshipped step only: exit 0"
assert_eq "0" "$(n_ledger '.status!="ok"')" "no ledger drift"
rm -rf "$tmpC"

# ── Case D: tag for a different initiative → not attributed to demo ───────────
tmpD="$(mktemp -d)"
mkfix "$tmpD"
printf '%s' '{"data":{"todos":[{"completed":false,"tags":["other/step-01"]}]}}' >"$tmpD/todos.json"
run_doctor "$tmpD" "cat $tmpD/todos.json" --probe-solo
assert_eq "0" "$RC" "foreign-initiative tag: exit 0 (slug-scoped match)"
assert_eq "0" "$(n_ledger '.status!="ok"')" "foreign tag: no drift"
rm -rf "$tmpD"

# ── Case E: --probe-solo but backend != solo → probe skipped entirely ────────
tmpE="$(mktemp -d)"
mkfix "$tmpE"
sed 's/backend: solo/backend: task/' "$tmpE/.wip.yaml" >"$tmpE/.wip.yaml.t"
mv "$tmpE/.wip.yaml.t" "$tmpE/.wip.yaml"
printf '%s' '{"data":{"todos":[{"completed":false,"tags":["demo/step-01"]}]}}' >"$tmpE/todos.json"
run_doctor "$tmpE" "cat $tmpE/todos.json" --probe-solo
assert_eq "0" "$RC" "backend=task: exit 0 (probe skipped)"
assert_eq "0" "$(n_ledger)" "backend=task: no ledger check at all"
rm -rf "$tmpE"

# ── Case F: probe requested but unresolvable (no seam, mktemp path matches no
#    Solo project) → informational status:ok note, never drift ──────────────
tmpF="$(mktemp -d)"
mkfix "$tmpF"
run_doctor "$tmpF" "" --probe-solo
assert_eq "0" "$RC" "unresolvable probe: exit 0 (never fails doctor)"
assert_eq "0" "$(n_ledger '.status!="ok"')" "unresolvable probe: no drift"

test_summary
