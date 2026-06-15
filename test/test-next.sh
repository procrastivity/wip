#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="next"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-02
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
- **step-03 — Third** — later.

## Round 2 — Two

- **step-04 — Fourth** — round 2.

## Backlog

- **Cleanup chore** — sweep stragglers.
MD

# 1. Happy path: manifest active step is rank 1.
out="$(WIP_ROOT="$tmp" bin/wip-plumbing next)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "demo" "$(jq -r '.initiative' <<<"$out")" "initiative"
assert_eq "step-02" "$(jq -r '.candidates[0].id' <<<"$out")" "rank 1 = step-02"
assert_eq "manifest active step" "$(jq -r '.candidates[0].reason' <<<"$out")" "rank 1 reason"
assert_eq "step-03" "$(jq -r '.candidates[1].id' <<<"$out")" "rank 2 = step-03"
assert_eq "first unshipped step in active round" "$(jq -r '.candidates[1].reason' <<<"$out")" "rank 2 reason"
assert_eq "step-04" "$(jq -r '.candidates[2].id' <<<"$out")" "rank 3 = step-04 (next round)"
assert_eq "upcoming round 2" "$(jq -r '.candidates[2].reason' <<<"$out")" "rank 3 reason"
assert_eq "cleanup-chore" "$(jq -r '.candidates[3].id' <<<"$out")" "rank 4 = backlog"
assert_eq "roadmap backlog" "$(jq -r '.candidates[3].reason' <<<"$out")" "rank 4 reason"
assert_eq "4" "$(jq -r '.candidates | length' <<<"$out")" "4 candidates"

# 2. No duplicates: step-02 is rank 1 only, not also rank 2.
assert_eq "1" "$(jq '[.candidates[] | select(.id == "step-02")] | length' <<<"$out")" "step-02 not duplicated"

# 3. All shipped -> "roadmap complete" candidate.
tmp2="$(mktemp -d)"
mkdir -p "$tmp2/.wip/initiatives/demo"
cat >"$tmp2/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp2/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Done

- **step-01 — A** ✅ shipped 2026-05-01 — x.
- **step-02 — B** ✅ shipped 2026-05-02 — y.
MD
out2="$(WIP_ROOT="$tmp2" bin/wip-plumbing next)"
assert_eq "null" "$(jq -r '.candidates[0].id' <<<"$out2")" "all shipped: id null"
assert_eq "roadmap complete" "$(jq -r '.candidates[0].title' <<<"$out2")" "all shipped: title"
assert_eq "start next round / close initiative" "$(jq -r '.candidates[0].reason' <<<"$out2")" "all shipped: reason"
rm -rf "$tmp2"

# 4. Manifest active_step shipped (or empty) -> rank 1 = inferred first unshipped.
tmp3="$(mktemp -d)"
mkdir -p "$tmp3/.wip/initiatives/demo"
sed 's/active_step: step-02//' "$tmp/.wip.yaml" >"$tmp3/.wip.yaml"
cp "$tmp/.wip/initiatives/demo/roadmap.md" "$tmp3/.wip/initiatives/demo/roadmap.md"
out3="$(WIP_ROOT="$tmp3" bin/wip-plumbing next)"
assert_eq "step-02" "$(jq -r '.candidates[0].id' <<<"$out3")" "no active_step: rank 1 = inferred"
assert_eq "first unshipped step in active round" "$(jq -r '.candidates[0].reason' <<<"$out3")" "inferred reason"
rm -rf "$tmp3"

# 5. Repo backlog appears after roadmap backlog.
cat >"$tmp/.wip/backlog.md" <<'MD'
# Backlog — cross-cutting

- **Cross-cutting thing** — a wider concern.
MD
out5="$(WIP_ROOT="$tmp" bin/wip-plumbing next)"
last_idx=$(jq -r '.candidates | length - 1' <<<"$out5")
assert_eq "cross-cutting-thing" "$(jq -r --argjson i "$last_idx" '.candidates[$i].id' <<<"$out5")" "repo backlog last"
assert_eq "repo backlog" "$(jq -r --argjson i "$last_idx" '.candidates[$i].reason' <<<"$out5")" "repo backlog reason"
rm "$tmp/.wip/backlog.md"

# 6. --initiative <unknown> -> exit 3.
set +e
WIP_ROOT="$tmp" bin/wip-plumbing next --initiative bogus >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"

# ---- Lane-aware ranking (ADR-0010) ----
tmpL="$(mktemp -d)"
mkdir -p "$tmpL/.wip/initiatives/demo"
cat >"$tmpL/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-13
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmpL/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 4 — Track expansion

- **step-12 — F1 prereq** ✅ shipped 2026-06-01 — done.

### Lane A
- **step-13 — Track A part 1** — spine.
- **step-15 — Track A part 2** — provider.

### Lane D
- **step-14 — Track D** — SPA.
MD

# Active step in Lane A: rank 1 = manifest active, rank 2 = next-in-lane (A),
# Lane D step surfaced as concurrent.
outL="$(WIP_ROOT="$tmpL" bin/wip-plumbing next)"
assert_eq "step-13" "$(jq -r '.candidates[0].id' <<<"$outL")" "lane: rank 1 = active step-13"
assert_eq "manifest active step" "$(jq -r '.candidates[0].reason' <<<"$outL")" "lane: rank 1 reason"
assert_eq "step-15" "$(jq -r '.candidates[1].id' <<<"$outL")" "lane: rank 2 = step-15 (next in Lane A)"
assert_eq "next-in-lane" "$(jq -r '.candidates[1].reason' <<<"$outL")" "lane: rank 2 reason next-in-lane"
assert_eq "null" "$(jq -r '.candidates[1].concurrent // null' <<<"$outL")" "lane: next-in-lane not concurrent"
assert_eq "step-14" "$(jq -r '.candidates[2].id' <<<"$outL")" "lane: rank 3 = step-14 (Lane D)"
assert_eq "concurrent lane D" "$(jq -r '.candidates[2].reason' <<<"$outL")" "lane: rank 3 reason concurrent"
assert_eq "true" "$(jq -r '.candidates[2].concurrent' <<<"$outL")" "lane: sibling lane flagged concurrent"

# No active_step + main-lane prereq unshipped: the prereq ranks first (no concurrency).
sed 's/active_step: step-13//' "$tmpL/.wip.yaml" >"$tmpL/.wip.yaml.noactive"
mv "$tmpL/.wip.yaml.noactive" "$tmpL/.wip.yaml"
sed 's/✅ shipped 2026-06-01 //' "$tmpL/.wip/initiatives/demo/roadmap.md" >"$tmpL/rm.tmp"
mv "$tmpL/rm.tmp" "$tmpL/.wip/initiatives/demo/roadmap.md"
outL2="$(WIP_ROOT="$tmpL" bin/wip-plumbing next)"
assert_eq "step-12" "$(jq -r '.candidates[0].id' <<<"$outL2")" "lane: main prereq ranks first"
assert_eq "first unshipped step in active round" "$(jq -r '.candidates[0].reason' <<<"$outL2")" "lane: prereq reason"
assert_eq "0" "$(jq '[.candidates[] | select(.concurrent == true)] | length' <<<"$outL2")" "lane: no concurrency while in main lane"
rm -rf "$tmpL"

test_summary
