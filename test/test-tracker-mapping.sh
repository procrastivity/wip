#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-mapping"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-02 (ADR-0019 §B/§C): the `issue-tracker` feature detail, the roadmap
# `[tracker: ID]` parse, the writer-generated `.wip.yaml` mirror (`tracker map
# [--write]`), and the `doctor` agreement check. Fixture is built so doctor's
# ONLY possible drift is the tracker mirror — steps stay unshipped (no closeout
# drift), features carry no sentinel, the initiative dir exists.

export WIP_NO_REGISTRY=1
WIP=bin/wip-plumbing

mkfixture() {
  local dir="$1"
  mkdir -p "$dir/.wip/initiatives/demo"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  issue-tracker: { enabled: true, backend: linear }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
  cat >"$dir/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — scoped. [tracker: BDS-90]
- **step-02 — Second** — scoped. [tracker: BDS-91]
- **step-03 — Third** — no mapping here.
MD
}

# --- issue-tracker feature detail (backend echo) ----------------------------
tmp="$(wip_mktemp)"
mkfixture "$tmp"
det="$(WIP_ROOT="$tmp" $WIP detect)"
assert_eq "true" "$(jq -r '.features[] | select(.name=="issue-tracker") | .active' <<<"$det")" \
  "issue-tracker active"
assert_eq "linear" "$(jq -r '.features[] | select(.name=="issue-tracker") | .detail.backend' <<<"$det")" \
  "issue-tracker backend detail echoed"

# --- roadmap parse: [tracker: ID] extraction --------------------------------
doc="$(WIP_ROOT="$tmp" $WIP roadmap parse "$tmp/.wip/initiatives/demo/roadmap.md")"
assert_eq "BDS-90" "$(jq -r '.rounds[0].steps[0].tracker' <<<"$doc")" "step-01 tracker parsed"
assert_eq "BDS-91" "$(jq -r '.rounds[0].steps[1].tracker' <<<"$doc")" "step-02 tracker parsed"
assert_eq "null" "$(jq -r '.rounds[0].steps[2].tracker' <<<"$doc")" "step-03 has no tracker"

# A shipped marker AND a tracker key coexist (tracker survives the ✅ strip).
shipd="$(wip_mktemp)"
mkdir -p "$shipd"
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** ✅ shipped 2026-05-01 — done. [tracker: BDS-77]\n' \
  >"$shipd/roadmap.md"
sdoc="$(WIP_ROOT="$tmp" $WIP roadmap parse "$shipd/roadmap.md")"
assert_eq "true" "$(jq -r '.rounds[0].steps[0].shipped' <<<"$sdoc")" "shipped+tracker: shipped true"
assert_eq "BDS-77" "$(jq -r '.rounds[0].steps[0].tracker' <<<"$sdoc")" "shipped+tracker: tracker survives ✅ strip"

# --- tracker map (read) -----------------------------------------------------
m1="$(WIP_ROOT="$tmp" $WIP tracker map)"
assert_eq "BDS-90" "$(jq -r '.tracker_map["step-01"]' <<<"$m1")" "map derives step-01"
assert_eq "BDS-91" "$(jq -r '.tracker_map["step-02"]' <<<"$m1")" "map derives step-02"
assert_eq "0" "$(jq -r '.mirror | length' <<<"$m1")" "mirror empty before write"
assert_eq "false" "$(jq -r '.agrees' <<<"$m1")" "read: agrees false (mirror empty)"
assert_eq "false" "$(jq -r '.wrote' <<<"$m1")" "read: wrote false"

# --- doctor before write: tracker-mirror-drift, exit 4 ----------------------
set +e
dout="$(WIP_ROOT="$tmp" $WIP doctor 2>/dev/null)"
drc=$?
set -e
assert_eq "4" "$drc" "doctor exits 4 on mirror drift"
assert_eq "tracker-mirror-drift" \
  "$(jq -r '.checks[] | select(.kind=="tracker") | .status' <<<"$dout")" "doctor flags tracker-mirror-drift"

# --- tracker map --write: regenerate mirror ---------------------------------
m2="$(WIP_ROOT="$tmp" $WIP tracker map --write)"
assert_eq "true" "$(jq -r '.agrees' <<<"$m2")" "write: agrees true after"
assert_eq "true" "$(jq -r '.wrote' <<<"$m2")" "write: wrote true"
assert_eq "BDS-91" "$(jq -r '.mirror["step-02"]' <<<"$m2")" "write: mirror now carries step-02"
assert_eq "BDS-90" "$(SLUG=demo yq -r '.initiatives[0].tracker_map["step-01"]' "$tmp/.wip.yaml")" \
  "write: .wip.yaml mirror persisted"

# --- re-read after write: agrees, no re-write -------------------------------
m3="$(WIP_ROOT="$tmp" $WIP tracker map)"
assert_eq "true" "$(jq -r '.agrees' <<<"$m3")" "re-read: agrees true"
assert_eq "false" "$(jq -r '.wrote' <<<"$m3")" "re-read: wrote false (no change)"

# --- doctor after write: clean (exit 0, no tracker check) -------------------
set +e
WIP_ROOT="$tmp" $WIP doctor >/dev/null 2>&1
drc2=$?
set -e
assert_eq "0" "$drc2" "doctor clean after mirror written"

# --- no tracker keys at all -> doctor stays quiet ---------------------------
tmpN="$(wip_mktemp)"
mkdir -p "$tmpN/.wip/initiatives/demo"
cat >"$tmpN/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — no tracker.\n' \
  >"$tmpN/.wip/initiatives/demo/roadmap.md"
set +e
WIP_ROOT="$tmpN" $WIP doctor >/dev/null 2>&1
drcN=$?
set -e
assert_eq "0" "$drcN" "no tracker keys -> doctor clean (quiet)"

# --- round-level tracker nodes + anchor stays out of the mirror (ADR-0024) --
# A `## Round N — title [tracker: ID]` heading is an addressable node: the map
# harvests a `round-N` entry alongside its steps; --write mirrors it; the
# `tracker_anchor` (intake-anchored, D3) is NEVER folded into tracker_map.
rnd="$(wip_mktemp)"
mkdir -p "$rnd/.wip/initiatives/demo"
cat >"$rnd/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  issue-tracker: { enabled: true, backend: linear }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_anchor: BDS-56
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$rnd/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One [tracker: BDS-100]

- **step-01 — First** — scoped. [tracker: BDS-90]
- **step-02 — Second** — no mapping.

## Round 2 — Two

- **step-03 — Third** — scoped. [tracker: BDS-91]
MD

rmap="$(WIP_ROOT="$rnd" $WIP tracker map)"
assert_eq "BDS-100" "$(jq -r '.tracker_map["round-1"]' <<<"$rmap")" "map harvests round-1 node"
assert_eq "BDS-90" "$(jq -r '.tracker_map["step-01"]' <<<"$rmap")" "map still harvests step-01"
assert_eq "BDS-91" "$(jq -r '.tracker_map["step-03"]' <<<"$rmap")" "map still harvests step-03"
assert_eq "null" "$(jq -r '.tracker_map["round-2"]' <<<"$rmap")" "round-2 (no key) absent from map"
# The intake anchor is NOT part of the roadmap-derived map.
assert_eq "null" "$(jq -r '.tracker_map["initiative"]' <<<"$rmap")" "anchor not a tracker_map node"

# --write mirrors the round node into .wip.yaml; anchor stays a sibling field.
wmap="$(WIP_ROOT="$rnd" $WIP tracker map --write)"
assert_eq "true" "$(jq -r '.agrees' <<<"$wmap")" "round-map write agrees"
assert_eq "BDS-100" "$(SLUG=demo yq -r '.initiatives[0].tracker_map["round-1"]' "$rnd/.wip.yaml")" \
  "write: round-1 persisted to mirror"
assert_eq "BDS-56" "$(yq -r '.initiatives[0].tracker_anchor' "$rnd/.wip.yaml")" "anchor untouched by map --write"
assert_eq "false" "$(yq -o=json '.initiatives[0].tracker_map | has("initiative")' "$rnd/.wip.yaml")" \
  "anchor NOT written into tracker_map"

# doctor agrees after mirroring the round node (no drift from the round entry).
set +e
WIP_ROOT="$rnd" $WIP doctor >/dev/null 2>&1
rdrc=$?
set -e
assert_eq "0" "$rdrc" "doctor clean after round node mirrored"

# --- error envelopes --------------------------------------------------------
set +e
WIP_ROOT="$tmp" $WIP tracker >/dev/null 2>&1
assert_eq "2" "$?" "missing subcommand -> exit 2"
WIP_ROOT="$tmp" $WIP tracker bogus >/dev/null 2>&1
assert_eq "2" "$?" "unknown subcommand -> exit 2"
WIP_ROOT="$tmp" $WIP tracker map --initiative nope >/dev/null 2>&1
assert_eq "3" "$?" "unknown initiative -> exit 3"
set -e

test_summary
