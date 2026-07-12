#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="doctor-backlog-tracker-closed"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# §2o (kind:"backlog", status:"backlog-tracker-closed"): a backlog entry whose
# tracker issue the cache already knows to be closed (done/canceled/cancelled) —
# the entry nobody pruned when its work shipped, and which `next` would otherwise
# keep nominating. Actionable drift, so exit 4.
#
# The check is a PURE DISK read of `.wip/tracker-cache.json` under the
# `issue:<TRACKER-ID>` keyspace (workplan D3) — no network, no `--probe-*` flag —
# gated only on `_wip_tracker_enabled`. Two sweeps, one kind: the repo-level
# `.wip/backlog.md` (source:"repo", multi-paragraph grammar, parsed by
# `_wip_repo_backlog_parse`) and each in-flight initiative's own `## Backlog`
# section (source:"roadmap", one-line grammar, parsed by `wip_roadmap_parse`).
#
# The two pins the fixtures below exist to kill, per the workplan:
#   (a) a stub that flags any entry merely because it CARRIES a tracker key, never
#       consulting the cache — dies on the open-state and no-entry cases, which
#       must stay silent;
#   (b) a stub that never reads the cache and reports everything "ok" — dies on the
#       closed-state case, which must both emit the check AND exit 4.
# The no-entry case is the fail-open rule (D4) standing on its own: absent data is
# never "closed". It is also the LIVE repo's state today (nothing populates
# `issue:*` keys yet), so a check that got this wrong would demand the pruning of
# every tracked backlog item in this very repo.
#
# HARD BOUNDARY: every fixture is a disposable tmp dir. Nothing here reads, runs
# against, or modifies the live repo's `.wip/backlog.md`, `.wip.yaml`,
# `.wip/tracker-cache.json`, or any roadmap.
#
# Contract: workplan step-06 (.wip/initiatives/closeout-write-ladder/workplans/
# step-06-retire-shipped-backlog-items-instead-of-re-nominating-them.md), Chunk 5.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
WIP=bin/wip-plumbing

# mkfx <dir> — a fixture whose ONLY possible drift is §2o. The step is unshipped
# and carries no tracker (so §2b/§2j/§2l/§2d all stay quiet), issue-tracker is
# enabled (so §2o is armed at all), and BOTH backlog sources carry one tracked
# entry — BDS-99 in the repo backlog (spelled the live file's way, a markdown link
# on the entry's trailing line), BDS-98 in the roadmap's `## Backlog` (spelled the
# roadmap's way, a `[tracker: ID]` marker). Each source also carries one UNTRACKED
# entry, which must never be flagged whatever the cache says: an entry with no
# tracker has nothing to look up.
mkfx() {
  local dir="$1"
  mkdir -p "$dir/.wip/initiatives/demo"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
  cat >"$dir/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — current.

## Backlog (cross-cutting, not yet scheduled)

- **Roadmap chore** — sweep the stragglers. [tracker: BDS-98]
- **Unfiled roadmap chore** — never filed anywhere.
MD
  cat >"$dir/.wip/backlog.md" <<'MD'
# Backlog

## Nice-to-have

- **Repo chore** — the multi-paragraph shape the real file uses.

  A second paragraph, because real entries have them.

  ([BDS-99](https://linear.app/beausimensen/issue/BDS-99))

- **Unfiled repo chore** — no tracker of either spelling, so nothing to look up.
MD
}

# seed <dir> <tracker-id> <state> — write an `issue:<ID>` cache entry, the same
# way status.bash's and review.bash's cache-consuming tests seed theirs. Populating
# this keyspace from a live tracker is out of scope for step-06 (there is no writer
# for it yet), so seeding IS the only way in.
seed() { _wip_tracker_cache_set "$1" "issue:$2" "$3" "seeded by test" "2026-07-12" >/dev/null; }

# run_doctor <dir> — set globals OUT (json) and RC. Plain `doctor`, no probe flag:
# §2o is default-on by construction and must never need one.
run_doctor() {
  set +e
  OUT="$(WIP_ROOT="$1" $WIP doctor)"
  RC=$?
  set -e
}

# n_backlog [selector] — count §2o checks (optionally filtered).
n_backlog() { jq "[.checks[] | select(.kind==\"backlog\") | select(${1:-true})] | length" <<<"$OUT"; }

# ── Case 1: cache says done → flagged, exit 4 ────────────────────────────────
# Both sources, both trackers closed. This is pin (b): a check that never reads the
# cache and reports "ok" produces zero entries here and exits 0.
tmp1="$(wip_mktemp)"
mkfx "$tmp1"
seed "$tmp1" BDS-99 "done"
seed "$tmp1" BDS-98 "done"
run_doctor "$tmp1"
assert_eq "4" "$RC" "cache done: exit 4 — closed-tracker backlog entries are drift, not a note"
assert_eq "2" "$(n_backlog)" "cache done: EXACTLY 2 §2o entries — one per source, none for the untracked entries"
assert_eq "2" "$(jq -r '.drift_count' <<<"$OUT")" "cache done: the 2 §2o entries are the ONLY drift"
assert_eq "false" "$(jq -r '.ok' <<<"$OUT")" "cache done: ok:false"
assert_eq "2" "$(n_backlog '.status=="backlog-tracker-closed"')" "cache done: status is backlog-tracker-closed"

repo="$(jq -c '.checks[] | select(.kind=="backlog" and .source=="repo")' <<<"$OUT")"
assert_eq "BDS-99" "$(jq -r '.tracker' <<<"$repo")" "repo sweep: names the tracker"
assert_eq "repo-chore" "$(jq -r '.id' <<<"$repo")" "repo sweep: id is the slugified title"
assert_eq "Repo chore" "$(jq -r '.title' <<<"$repo")" "repo sweep: carries the title"
assert_eq "prune repo-chore (tracker BDS-99)" "$(jq -r '.fix' <<<"$repo")" "repo sweep: fix names the entry and its tracker"
assert_eq "null" "$(jq -r '.slug // "null"' <<<"$repo")" "repo sweep: no slug — the repo backlog belongs to no initiative"

road="$(jq -c '.checks[] | select(.kind=="backlog" and .source=="roadmap")' <<<"$OUT")"
assert_eq "BDS-98" "$(jq -r '.tracker' <<<"$road")" "roadmap sweep: names the tracker"
assert_eq "roadmap-chore" "$(jq -r '.id' <<<"$road")" "roadmap sweep: id is the slugified title"
assert_eq "demo" "$(jq -r '.slug' <<<"$road")" "roadmap sweep: slug names the initiative it came from"
assert_eq "prune roadmap-chore (tracker BDS-98)" "$(jq -r '.fix' <<<"$road")" "roadmap sweep: fix names the entry and its tracker"

# ...and the untracked entries in BOTH files stayed out of it, on the very run where
# their tracked neighbours were flagged.
assert_eq "0" "$(n_backlog '.id=="unfiled-repo-chore" or .id=="unfiled-roadmap-chore"')" \
  "cache done: an entry with no tracker is never flagged — there is nothing to look up"

# ── Case 2: cache says in-progress → silent, exit 0 ──────────────────────────
# THE pin for a stub that flags any entry carrying a tracker key: the trackers are
# present, spelled identically to case 1, and the cache is populated for both. Only
# the STATE differs, and the state is the whole check.
tmp2="$(wip_mktemp)"
mkfx "$tmp2"
seed "$tmp2" BDS-99 in-progress
seed "$tmp2" BDS-98 in-progress
run_doctor "$tmp2"
assert_eq "0" "$RC" "cache in-progress: exit 0"
assert_eq "0" "$(n_backlog)" "cache in-progress: no §2o entry — an open issue is not a prunable backlog item"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "cache in-progress: zero drift"
assert_eq "true" "$(jq -r '.ok' <<<"$OUT")" "cache in-progress: ok:true"

# ── Case 3: no cache entry at all → silent, exit 0 (D4, fail open) ───────────
# The live repo's state today. Absent data is UNKNOWN, never closed: the entries
# carry trackers, the cache file does not exist, and doctor must stay quiet rather
# than demand their pruning. Emits NOTHING for them — not even an "ok" placeholder
# (§2f's per-item shape, not `tracker-probe`'s per-sweep "unavailable" note).
tmp3="$(wip_mktemp)"
mkfx "$tmp3"
assert_absent "$tmp3/.wip/tracker-cache.json" "no cache: the fixture really has no cache file"
run_doctor "$tmp3"
assert_eq "0" "$RC" "no cache entry: exit 0 — never guess closed"
assert_eq "0" "$(n_backlog)" "no cache entry: no §2o entry of any status, not even ok"
assert_eq "0" "$(jq -r '.drift_count' <<<"$OUT")" "no cache entry: zero drift"

# A cache that exists but has no `issue:*` key is the same answer — this is the
# shape the live repo is actually in (its cache holds only `<slug>/<node>` keys,
# whose values are wip's OWN lifecycle labels, `done` among them). Reading a wip
# node key as if it were an issue key would flag half the repo.
tmp3b="$(wip_mktemp)"
mkfx "$tmp3b"
_wip_tracker_cache_set "$tmp3b" "demo/step-01" "done" "ship" "2026-07-12" >/dev/null
run_doctor "$tmp3b"
assert_eq "0" "$RC" 'cache with only node keys: exit 0 — <slug>/<node> is a different keyspace than issue:<ID>'
assert_eq "0" "$(n_backlog)" "cache with only node keys: no §2o entry"

# ── Case 4: closed vocabulary + case-insensitivity ──────────────────────────
# `canceled`/`cancelled` are closed too, and a tracker reporting `Done` in its own
# capitalisation must read the same as `done` — the cache stores whatever a future
# writer puts there, not a normalised token.
tmp4="$(wip_mktemp)"
mkfx "$tmp4"
seed "$tmp4" BDS-99 "Done"
seed "$tmp4" BDS-98 "Canceled"
run_doctor "$tmp4"
assert_eq "4" "$RC" "Done/Canceled: exit 4"
assert_eq "2" "$(n_backlog)" "Done/Canceled: both flagged — the state match is case-insensitive"

tmp4b="$(wip_mktemp)"
mkfx "$tmp4b"
seed "$tmp4b" BDS-99 "cancelled" # the other spelling
seed "$tmp4b" BDS-98 "Backlog"   # a tracker's OWN 'Backlog' column: open, not closed
run_doctor "$tmp4b"
assert_eq "4" "$RC" "cancelled/Backlog: exit 4 (the cancelled one)"
assert_eq "1" "$(n_backlog)" "cancelled/Backlog: EXACTLY one flagged"
assert_eq "1" "$(n_backlog '.source=="repo"')" "cancelled: the British spelling is closed too"
assert_eq "0" "$(n_backlog '.source=="roadmap"')" \
  "a tracker state literally named 'Backlog' is an OPEN column, not a closed one"

# ── Case 5: per-entry, not per-file ─────────────────────────────────────────
# One closed, one absent. A sweep that flagged a whole file once it found any
# closed tracker in it — or that fell back to "the file has tracked entries" — is
# caught here: exactly one of the two is drift.
tmp5="$(wip_mktemp)"
mkfx "$tmp5"
seed "$tmp5" BDS-99 "done"
run_doctor "$tmp5"
assert_eq "4" "$RC" "mixed: exit 4"
assert_eq "1" "$(n_backlog)" "mixed: EXACTLY the closed one is flagged"
assert_eq "BDS-99" "$(jq -r '.checks[] | select(.kind=="backlog") | .tracker' <<<"$OUT")" \
  "mixed: and it is the closed tracker, not the uncached one"

# ── Case 6: gated on _wip_tracker_enabled ───────────────────────────────────
# Same closed cache, issue-tracker feature removed. §2o must go inert — same gate
# as §2f/§2h: a repo not using a tracker has no tracker states to reason about.
tmp6="$(wip_mktemp)"
mkfx "$tmp6"
seed "$tmp6" BDS-99 "done"
seed "$tmp6" BDS-98 "done"
yq -i 'del(.features."issue-tracker")' "$tmp6/.wip.yaml"
run_doctor "$tmp6"
assert_eq "0" "$RC" "tracker disabled: exit 0 — §2o is gated, not unconditional"
assert_eq "0" "$(n_backlog)" "tracker disabled: no §2o entry despite a done cache"

# ── Case 7: shipped/archived initiatives are out of scope for the roadmap sweep ─
# Same scope guard every other per-initiative check uses. The REPO backlog is not
# initiative-scoped, so it keeps being swept — that asymmetry is the point of the
# assertion pair.
tmp7="$(wip_mktemp)"
mkfx "$tmp7"
seed "$tmp7" BDS-99 "done"
seed "$tmp7" BDS-98 "done"
yq -i '.initiatives[0].status = "shipped"' "$tmp7/.wip.yaml"
run_doctor "$tmp7"
assert_eq "0" "$(n_backlog '.source=="roadmap"')" \
  "shipped initiative: its roadmap backlog is not swept (same guard as §2b/§2f)"
assert_eq "1" "$(n_backlog '.source=="repo"')" \
  "shipped initiative: the repo backlog is still swept — it belongs to no initiative"

# ── Case 8: no repo backlog file at all ─────────────────────────────────────
# A repo need not have a `.wip/backlog.md`. The parser answers `[]` for a missing
# path; doctor must not crash or invent an entry.
tmp8="$(wip_mktemp)"
mkfx "$tmp8"
rm "$tmp8/.wip/backlog.md"
seed "$tmp8" BDS-98 "done"
run_doctor "$tmp8"
assert_eq "4" "$RC" "no backlog.md: exit 4 (from the roadmap sweep alone)"
assert_eq "0" "$(n_backlog '.source=="repo"')" "no backlog.md: repo sweep is empty, not a crash"
assert_eq "1" "$(n_backlog '.source=="roadmap"')" "no backlog.md: the roadmap sweep still runs"

test_summary
