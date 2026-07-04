#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="sync"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-08 (Lane B, ADR-0019 §6): wip sync — a PUSH-FORWARD-ONLY reconciler.
# Forwards wip→tracker transitions that advance the lifecycle; never backward;
# a tracker ahead is observed (visibility), never mutated; no write transport ⇒
# the forward plan is emitted as `pending` for the agent/MCP path. WIP_LINEAR_
# {READ,WRITE}_CMD are the seams.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
WIP=bin/wip-plumbing

tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_map: { step-01: BDS-90, step-02: BDS-91, step-03: BDS-92, step-04: BDS-93 }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — x. [tracker: BDS-90]
- **step-02 — Second** — x. [tracker: BDS-91]
- **step-03 — Third** — x. [tracker: BDS-92]
- **step-04 — Fourth** — x. [tracker: BDS-93]
MD
# wip cache: step-01 in-review (ahead), step-02 in-progress (==), step-03 todo
# (tracker ahead), step-04 mapped but NO cache entry.
_wip_tracker_cache_set "$tmp" "demo/step-01" "in-review" "ship" "2026-06-28" >/dev/null
_wip_tracker_cache_set "$tmp" "demo/step-02" "in-progress" "start" "2026-06-28" >/dev/null
_wip_tracker_cache_set "$tmp" "demo/step-03" "todo" "x" "2026-06-28" >/dev/null
# tracker side: BDS-90 Todo (behind), BDS-91 In Progress (==), BDS-92 In Review (ahead).
cat >"$tmp/tracker.json" <<'J'
{"BDS-90":"Todo","BDS-91":"In Progress","BDS-92":"In Review"}
J
cat >"$tmp/read.sh" <<SH
#!/bin/sh
jq -r --arg i "\$1" '.[\$i] // ""' "$tmp/tracker.json"
SH
chmod +x "$tmp/read.sh"
cat >"$tmp/write.sh" <<SH
#!/bin/sh
echo "\$1=\$2" >> "$tmp/writes.log"
SH
chmod +x "$tmp/write.sh"

# --- no write transport -> everything forward is pending (agent/MCP path) ----
p="$(WIP_ROOT="$tmp" $WIP sync)"
assert_eq "mcp" "$(jq -r '.transport' <<<"$p")" "no write transport -> transport mcp"
assert_eq "0" "$(jq -r '.applied | length' <<<"$p")" "mcp path applies nothing"
# step-01/02/03 are forward-or-unknown (no read either) -> pending; step-04 no state.
assert_eq "3" "$(jq -r '.pending | length' <<<"$p")" "mcp path: 3 forward transitions pending"
assert_eq "no wip state" "$(jq -r '.skipped[] | select(.node=="demo/step-04") | .reason' <<<"$p")" \
  "unmapped-in-cache node -> skipped no wip state"

# (a) FLOOR PRESENT (BDS-29 chunk 1 landed in b35c495): on the empty-read_cmd
# (default MCP) path plumbing has no live tracker read, so it can no longer guard
# a backward move here — instead EVERY emitted pending row carries `min_rank`, the
# semantic rank of its target state (todo=0 < in-progress=1 < in-review=2 < done=3;
# _wip_tracker_semantic_rank), which the MCP applier enforces as a floor. Assert no
# pending row is bare (all carry a floor) and the floor matches each row's `to`.
assert_eq "0" "$(jq -r '[.pending[] | select(.min_rank == null)] | length' <<<"$p")" \
  "every mcp pending row carries a min_rank floor (none bare/unguarded)"
assert_eq "2" "$(jq -r '.pending[] | select(.node=="demo/step-01") | .min_rank' <<<"$p")" \
  "step-01 pending floor = rank of In Review (2)"
assert_eq "0" "$(jq -r '.pending[] | select(.node=="demo/step-03") | .min_rank' <<<"$p")" \
  "step-03 pending floor = rank of Todo (0)"

# (b) BDS-14 REPRO: step-02's wip cache floor is `in-progress` (rank 1). On this
# empty-read_cmd path plumbing is BLIND to the tracker's live state — in the BDS-14
# regression that state was actually `Done` (ahead of in-progress), and the old
# bare `{node,issue,to}` row let the applier stamp In Progress back over Done.
# Plumbing now emits `min_rank:1` on the pending row: the applier's refusal hook,
# which applies only a strictly-forward move against the issue's live rank, so a
# live-Done issue is never moved backward. Assert the guarded forward plan + floor.
assert_eq "In Progress" "$(jq -r '.pending[] | select(.node=="demo/step-02") | .to' <<<"$p")" \
  "BDS-14 repro: in-progress cache floor plans a forward move to In Progress"
assert_eq "1" "$(jq -r '.pending[] | select(.node=="demo/step-02") | .min_rank' <<<"$p")" \
  "BDS-14 repro: pending row carries min_rank 1 (applier's refusal floor vs live Done)"

# --- read+write transport: apply / skip / observe ---------------------------
rm -f "$tmp/writes.log"
s="$(WIP_ROOT="$tmp" WIP_LINEAR_READ_CMD="$tmp/read.sh" WIP_LINEAR_WRITE_CMD="$tmp/write.sh" $WIP sync)"
assert_eq "cli" "$(jq -r '.transport' <<<"$s")" "wired write -> transport cli"
# step-01: wip in-review, tracker Todo -> forward applied.
assert_eq "demo/step-01" "$(jq -r '.applied[0].node' <<<"$s")" "step-01 applied (forward)"
assert_eq "In Review" "$(jq -r '.applied[0].to' <<<"$s")" "step-01 applied to In Review"
assert_eq "BDS-90=In Review" "$(cat "$tmp/writes.log")" "the transition was actually written"
# step-02: wip == tracker -> skipped in sync.
assert_eq "in sync" "$(jq -r '.skipped[] | select(.node=="demo/step-02") | .reason' <<<"$s")" \
  "step-02 in sync -> skipped"
# step-03: tracker In Review ahead of wip todo -> observed, NOT moved backward.
assert_eq "demo/step-03" "$(jq -r '.observed[0].node' <<<"$s")" "step-03 tracker-ahead -> observed"
assert_eq "in-review" "$(jq -r '.observed[0].tracker_state' <<<"$s")" "observed records the tracker state"
assert_eq "0" "$(jq -r '[.applied[] | select(.node=="demo/step-03")] | length' <<<"$s")" \
  "tracker-ahead node is never written (push-forward only)"

# --- dry-run: forward plan is pending, nothing written ----------------------
rm -f "$tmp/writes.log"
d="$(WIP_ROOT="$tmp" WIP_LINEAR_READ_CMD="$tmp/read.sh" WIP_LINEAR_WRITE_CMD="$tmp/write.sh" $WIP sync --dry-run)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$d")" "dry-run flagged"
assert_eq "0" "$(jq -r '.applied | length' <<<"$d")" "dry-run applies nothing"
assert_eq "demo/step-01" "$(jq -r '.pending[0].node' <<<"$d")" "dry-run: forward move is pending"
assert_eq "false" "$([[ -f "$tmp/writes.log" ]] && echo true || echo false)" "dry-run wrote nothing"

# --- service selection ------------------------------------------------------
assert_eq "none" "$(jq -r '.transport' <<<"$(WIP_ROOT="$tmp" $WIP sync solo)")" \
  "sync solo (no tracker match) -> transport none"
assert_eq "mcp" "$(jq -r '.transport' <<<"$(WIP_ROOT="$tmp" $WIP sync linear)")" \
  "sync linear -> reconciles the tracker"

tmpG="$(wip_mktemp)"
mkdir -p "$tmpG/.wip/initiatives/demo"
cat >"$tmpG/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: github } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x.\n' \
  >"$tmpG/.wip/initiatives/demo/roadmap.md"
assert_eq "none" "$(jq -r '.transport' <<<"$(WIP_ROOT="$tmpG" $WIP sync linear)")" \
  "sync linear does not match a non-linear backend"

# --- stale mirror guard ------------------------------------------------------
WIP_ROOT="$tmp" yq -i '.initiatives[0].tracker_map["step-01"] = "BDS-999"' "$tmp/.wip.yaml"
set +e
drift="$(WIP_ROOT="$tmp" WIP_LINEAR_READ_CMD="$tmp/read.sh" WIP_LINEAR_WRITE_CMD="$tmp/write.sh" $WIP sync 2>/dev/null)"
drc=$?
set -e
assert_eq "4" "$drc" "sync refuses stale tracker mirror"
assert_eq "tracker-mirror-drift" "$(jq -r '.error.kind' <<<"$drift")" "sync drift error kind"

# --- node granularity: initiative (anchor) + round nodes flow through sync ---
# (ADR-0024) The intake `tracker_anchor` surfaces as an `initiative` node and a
# `## Round N [tracker: ID]` heading as a `round-N` node, alongside steps — all
# push-forward-only, in one `pending` set. The anchor is a sibling of tracker_map,
# so it must NOT trip the mirror-drift gate (rmap == mmap compares steps+rounds).
g="$(wip_mktemp)"
mkdir -p "$g/.wip/initiatives/demo"
cat >"$g/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: linear } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_anchor: BDS-56
    tracker_map: { step-01: BDS-90, round-1: BDS-100 }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$g/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One [tracker: BDS-100]

- **step-01 — First** — x. [tracker: BDS-90]
MD
_wip_tracker_cache_set "$g" "demo/initiative" "in-progress" "start" "2026-07-04" >/dev/null
_wip_tracker_cache_set "$g" "demo/round-1" "in-progress" "start" "2026-07-04" >/dev/null
_wip_tracker_cache_set "$g" "demo/step-01" "in-progress" "start" "2026-07-04" >/dev/null

# No transport -> all three forward/unknown transitions are pending; the anchor
# (BDS-56, absent from tracker_map) does NOT trip the mirror-drift gate.
gp="$(WIP_ROOT="$g" $WIP sync)"
assert_eq "true" "$(jq -r '.ok' <<<"$gp")" "granularity sync ok (anchor doesn't trip drift gate)"
assert_eq "3" "$(jq -r '.pending | length' <<<"$gp")" "initiative + round-1 + step-01 all pending"
assert_eq "In Progress" "$(jq -r '.pending[] | select(.node=="demo/initiative") | .to' <<<"$gp")" \
  "initiative node pending (from the anchor)"
assert_eq "In Progress" "$(jq -r '.pending[] | select(.node=="demo/round-1") | .to' <<<"$gp")" \
  "round-1 node pending"

# push-forward-only: tracker reports the initiative Done (AHEAD of wip in-progress)
# -> observed, never moved backward, never pending.
cat >"$g/tracker.json" <<'J'
{"BDS-56":"Done"}
J
cat >"$g/gread.sh" <<SH
#!/bin/sh
jq -r --arg i "\$1" '.[\$i] // ""' "$g/tracker.json"
SH
chmod +x "$g/gread.sh"
go="$(WIP_ROOT="$g" WIP_LINEAR_READ_CMD="$g/gread.sh" $WIP sync)"
assert_eq "demo/initiative" "$(jq -r '.observed[] | select(.node=="demo/initiative") | .node' <<<"$go")" \
  "tracker-ahead initiative -> observed (push-forward only)"
assert_eq "done" "$(jq -r '.observed[] | select(.node=="demo/initiative") | .tracker_state' <<<"$go")" \
  "observed records the ahead tracker state"
assert_eq "0" "$(jq -r '[.pending[] | select(.node=="demo/initiative")] | length' <<<"$go")" \
  "tracker-ahead initiative never pending"

# The mirror-drift gate STILL fires on a genuine round/step mismatch (the round IS
# part of the mirror) — proving the anchor exclusion did not disable the gate.
WIP_ROOT="$g" yq -i '.initiatives[0].tracker_map["round-1"] = "BDS-999"' "$g/.wip.yaml"
set +e
gdrift="$(WIP_ROOT="$g" $WIP sync 2>/dev/null)"
gdrc=$?
set -e
assert_eq "4" "$gdrc" "mirror drift on a round node still fails sync"
assert_eq "tracker-mirror-drift" "$(jq -r '.error.kind' <<<"$gdrift")" "round drift -> drift error kind"

# --- error envelopes --------------------------------------------------------
set +e
WIP_ROOT="$tmp" $WIP sync --initiative nope >/dev/null 2>&1
assert_eq "3" "$?" "unknown initiative -> exit 3"
WIP_ROOT="$tmp" $WIP sync --initiative >/dev/null 2>&1
assert_eq "2" "$?" "--initiative without arg -> exit 2"
set -e

test_summary
