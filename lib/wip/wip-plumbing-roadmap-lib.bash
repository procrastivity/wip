# wip-plumbing-roadmap-lib.bash — parse `roadmap.md` into a JSON document.
# Sourced by bin/wip-plumbing. Pure bash + jq. Portable on BSD awk systems —
# we use bash `=~` rematching, not gawk capture groups.
#
# Grammar (per the workplan for step-08; lanes per ADR-0010):
#   Round heading : ## Round <N> — <title> [✅ shipped <YYYY-MM-DD>]
#   Step bullet   : - **step-<NN[.5]> — <title>** [✅] [shipped <YYYY-MM-DD>] — <body>
#   Step heading  : ### step-<NN[.5]> — <title>           (amendment form)
#   Lane heading  : ### Lane <name>                       (parallel lane in a round)
#   Sections      : ## Deferred / ## Backlog              (both parsed as entries)
#   Backlog entry : - **<title>** [— <body>] | (<body>)   (id = slugify(title))
#   Deferred entry: - **<title>** [— <body>]              (id = slugify(title))
#
# Lanes parallelize across; steps within a lane stay sequential. Every step parses
# with a `lane` field (the `### Lane <name>` it lives under, or null for main lane);
# every round with a `lanes` array (declared lane names, in order); the document with
# a `lane_errors` array, empty when well-formed. The round grammar is
# `main* (lane+)? main*`; the malformed cases recorded in lane_errors are:
#   lane-outside-round / nested-lane / duplicate-lane / main-step-between-lanes.
# shellcheck shell=bash

# wip_roadmap_parse <path> — emit a single JSON doc:
#   {rounds:[{n, title, shipped, shipped_date, lanes:[<name>],
#             steps:[{id, title, shipped, shipped_date, lane}]}],
#    backlog:[{id, title}],
#    deferred:[{id, title}],
#    lane_errors:[{kind, round?, lane?, step?}],
#    step_errors:[{kind, round?, line, raw}]}
# `lane` is null for a main-lane step; `lanes` lists a round's declared lanes in
# order (incl. empty lanes); `lane_errors` is empty when the lane structure is
# well-formed (ADR-0010). `step_errors` is empty when every step bullet parses:
# a line inside a round that opens like a step bullet but fails the bullet
# grammar lands there (kind:"malformed-step-bullet") instead of being silently
# skipped — a dropped step is invisible to status/next/ship, so the parse
# surface has to say so out loud.
# Missing path => empty doc (same shape, all arrays empty).
wip_roadmap_parse() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    jq -nc '{rounds:[], backlog:[], deferred:[], lane_errors:[], step_errors:[]}'
    return 0
  fi

  local mode="outside" line current_lane="" lane_saw_step=0 in_comment=0 lineno=0
  local doc='{"rounds":[],"backlog":[],"deferred":[],"lane_errors":[],"step_errors":[]}'

  while IFS= read -r line || [[ -n "$line" ]]; do
    lineno=$((lineno + 1))
    # Skip HTML comment blocks (`<!-- … -->`) so commented examples (e.g. the
    # `### Lane` block in templates/roadmap.md.tmpl) and the wip-amend markers
    # never parse as content.
    if [[ "$in_comment" == "1" ]]; then
      [[ "$line" == *"-->"* ]] && in_comment=0
      continue
    fi
    if [[ "$line" =~ ^[[:space:]]*\<!-- ]]; then
      # Open a multi-line skip only when the comment does not also close here.
      [[ "$line" == *"-->"* ]] || in_comment=1
      continue
    fi

    # Section heading switches.
    if [[ "$line" =~ ^\#\#\ Round\ ([0-9]+)\ —\ (.+)$ ]]; then
      local n="${BASH_REMATCH[1]}"
      local rest="${BASH_REMATCH[2]}"
      # A round heading may carry a `[tracker: ID]` key (ADR-0024 / D3): the round
      # is an addressable lifecycle node (`round-N`). Extract the id, then strip the
      # bracket so it never leaks into the round title — a step carries the key
      # outside its bold `**…**` title, but a round title has no such delimiter, so
      # it must be stripped here. Order-independent vs the ✅ shipped marker.
      local rtrk
      rtrk="$(_wip_roadmap_extract_tracker "$rest")"
      if [[ "$rest" =~ (\[tracker:[[:space:]]*([A-Z][A-Z0-9]*-[0-9]+|([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+)?#[0-9]+)[[:space:]]*\]) ]]; then
        rest="${rest/"${BASH_REMATCH[1]}"/}"
      fi
      local shipped="false" shipped_date="" title="$rest"
      _wip_roadmap_extract_shipped "$rest" shipped shipped_date title
      doc="$(jq -c \
        --argjson n "$n" --arg title "$title" --arg trk "$rtrk" \
        --argjson shipped "$shipped" --arg shipped_date "$shipped_date" '
        .rounds += [{
          n: $n, title: $title, shipped: $shipped,
          shipped_date: (if $shipped_date == "" then null else $shipped_date end),
          tracker: (if $trk == "" then null else $trk end),
          lanes: [], steps: []
        }]' <<<"$doc")"
      mode="round"
      current_lane=""
      lane_saw_step=0
      continue
    fi
    if [[ "$line" =~ ^\#\#\ Backlog ]]; then
      mode="backlog"
      current_lane=""
      lane_saw_step=0
      continue
    fi
    if [[ "$line" =~ ^\#\#\ Deferred ]]; then
      mode="deferred"
      current_lane=""
      lane_saw_step=0
      continue
    fi
    if [[ "$line" =~ ^\#\#[^\#] ]]; then
      mode="outside"
      current_lane=""
      lane_saw_step=0
      continue
    fi

    # A `### Lane` heading outside a round is malformed (ADR-0010 §5).
    if [[ "$mode" != "round" ]] && [[ "$line" =~ ^\#\#\#+\ Lane(\ |$) ]]; then
      doc="$(jq -c '.lane_errors += [{kind:"lane-outside-round"}]' <<<"$doc")"
      continue
    fi

    case "$mode" in
      round)
        if [[ "$line" =~ ^[[:space:]]*$ ]]; then
          # A blank line terminates a lane block once it has a step (ADR-0010 §5):
          # subsequent bullets return to the main lane (post-lane sync steps).
          # A blank line right after the `### Lane` heading (no step yet) does not.
          if [[ -n "$current_lane" && "$lane_saw_step" == "1" ]]; then
            current_lane=""
            lane_saw_step=0
          fi
        elif [[ "$line" =~ ^\#\#\#\#+\ Lane ]]; then
          # H4+ lane heading -> attempted nesting (ADR-0010 §3).
          doc="$(jq -c '.lane_errors += [{kind:"nested-lane", round:.rounds[-1].n}]' <<<"$doc")"
        elif [[ "$line" =~ ^\#\#\#\ Lane\ (.+)$ ]]; then
          local lane_name="${BASH_REMATCH[1]}"
          # Lane exclusion (ADR-0024 / D1, ADR-0010): a lane is a *grouping*, not a
          # lifecycle-emitting node. A `[tracker: ID]` on a `### Lane` heading is
          # deliberately IGNORED — strip it from the lane name (so it never pollutes
          # the name) and never harvest a mapping for it, so an author can't wire a
          # lane auto-transition by mistake. (A lane-level epic maps to its enclosing
          # round or an out-of-band parent link, not an auto-transitioned wip node.)
          if [[ "$lane_name" =~ (\[tracker:[[:space:]]*([A-Z][A-Z0-9]*-[0-9]+|([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+)?#[0-9]+)[[:space:]]*\]) ]]; then
            lane_name="${lane_name/"${BASH_REMATCH[1]}"/}"
          fi
          # Trim trailing whitespace from the lane name.
          lane_name="${lane_name%"${lane_name##*[![:space:]]}"}"
          doc="$(jq -c --arg lane "$lane_name" '
            if (.rounds[-1].lanes | index($lane)) != null
            then .lane_errors += [{kind:"duplicate-lane", round:.rounds[-1].n, lane:$lane}]
            else .rounds[-1].lanes += [$lane]
            end' <<<"$doc")"
          current_lane="$lane_name"
          lane_saw_step=0
        # The title runs up to its CLOSING `**`, not up to the first `*`: a title
        # may legitimately contain a literal `*` or an inline code span such as
        # `/wip:*`. `(([^*]|\*[^*])+)` is the bash-ERE way to say "any run that
        # does not cross a `**`" — a lone `*` is consumed only when the next char
        # is not another `*` — which keeps the load-bearing title/`srest` split
        # intact (shipped-state and tracker are read from `srest` ONLY). The ship
        # writer's first-line split mirrors this exactly; the two are one grammar
        # surface and must be changed together.
        elif [[ "$line" =~ ^-\ \*\*step-([0-9]+(\.[0-9]+)?)\ —\ (([^*]|\*[^*])+)\*\*(.*)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local srest="${BASH_REMATCH[5]}"
          local sshipped="false" sdate="" sdummy="$srest"
          # `head` anchor: in a bullet the marker sits immediately after the
          # title's closing `**`. A marker merely QUOTED in the body leaves the
          # step unshipped.
          _wip_roadmap_extract_shipped "$srest" sshipped sdate sdummy head
          local strk
          strk="$(_wip_roadmap_extract_tracker "$srest")"
          doc="$(jq -c \
            --arg id "$sid" --arg title "$stitle" --arg lane "$current_lane" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" --arg trk "$strk" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end),
              lane: (if $lane == "" then null else $lane end),
              tracker: (if $trk == "" then null else $trk end)
            }]' <<<"$doc")"
          [[ -n "$current_lane" ]] && lane_saw_step=1
        elif [[ "$line" =~ ^\#\#\#\ step-([0-9]+(\.[0-9]+)?)\ —\ (.+)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local sshipped="false" sdate="" sdummy="$stitle"
          _wip_roadmap_extract_shipped "$stitle" sshipped sdate sdummy
          local strk
          strk="$(_wip_roadmap_extract_tracker "$stitle")"
          doc="$(jq -c \
            --arg id "$sid" --arg title "$sdummy" --arg lane "$current_lane" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" --arg trk "$strk" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end),
              lane: (if $lane == "" then null else $lane end),
              tracker: (if $trk == "" then null else $trk end)
            }]' <<<"$doc")"
          [[ -n "$current_lane" ]] && lane_saw_step=1
        elif [[ "$line" =~ ^-[[:space:]]+\*\*step- ]]; then
          # Opens like a step bullet but matched neither arm above: report it
          # rather than dropping it. Scoped to `round` mode on purpose — a
          # backlog/deferred bullet that merely starts similarly is not a
          # malformed step. `round` is null for a bullet before any `## Round`.
          doc="$(jq -c --argjson ln "$lineno" --arg raw "$line" '
            .step_errors += [{
              kind: "malformed-step-bullet",
              round: (.rounds[-1].n),
              line: $ln,
              raw: $raw
            }]' <<<"$doc")"
        fi
        ;;
      backlog)
        if [[ "$line" =~ ^-\ \*\*([^*]+)\*\*(.*)$ ]]; then
          local btitle="${BASH_REMATCH[1]}"
          local brest="${BASH_REMATCH[2]}"
          local bid btrk
          bid="$(_wip_roadmap_slugify "$btitle")"
          btrk="$(_wip_roadmap_extract_tracker "$brest")"
          doc="$(jq -c --arg id "$bid" --arg title "$btitle" --arg trk "$btrk" '
            .backlog += [{id:$id, title:$title, tracker:(if $trk == "" then null else $trk end)}]' <<<"$doc")"
        fi
        ;;
      deferred)
        # Mirror the backlog arm: a `- **<title>**` bullet becomes a
        # {id, title} deferred entry (id = slugify(title)). Plain (non-bold)
        # bullets under `## Deferred` are not collected, same as backlog.
        if [[ "$line" =~ ^-\ \*\*([^*]+)\*\*(.*)$ ]]; then
          local dtitle="${BASH_REMATCH[1]}"
          local drest="${BASH_REMATCH[2]}"
          local did dtrk
          did="$(_wip_roadmap_slugify "$dtitle")"
          dtrk="$(_wip_roadmap_extract_tracker "$drest")"
          doc="$(jq -c --arg id "$did" --arg title "$dtitle" --arg trk "$dtrk" '
            .deferred += [{id:$id, title:$title, tracker:(if $trk == "" then null else $trk end)}]' <<<"$doc")"
        fi
        ;;
    esac
  done <"$path"

  # Post-pass: a main-lane (lane==null) step sandwiched between lane steps in the
  # same round is malformed (ADR-0010 §5) — pre/post-lane main steps are fine.
  doc="$(jq -c '
    .lane_errors += [
      .rounds[] as $r
      | ($r.steps | to_entries) as $es
      | $es[]
      | select(.value.lane == null)
      | . as $e
      | select(
          ([$es[] | select(.key < $e.key and .value.lane != null)] | length) > 0 and
          ([$es[] | select(.key > $e.key and .value.lane != null)] | length) > 0
        )
      | {kind:"main-step-between-lanes", round:$r.n, step:$e.value.id}
    ]
  ' <<<"$doc")"

  printf '%s' "$doc"
}

# ---------------------------------------------------------------------------
# SHIPPED-MARKER SPELLING — the single extension point.
#
# `_WIP_ROADMAP_SHIPPED_MARKERS` is the ONE place that decides WHICH tokens
# spell a shipped marker, and `_WIP_ROADMAP_SHIPPED_KEYWORD` the optional word
# that may follow one before the date. Everything downstream — this file's
# parser and the ship writer in wip-plumbing-ship-roadmap-lib.bash — asks
# `_wip_roadmap_extract_shipped` and never re-spells the marker itself, so the
# canonical spelling can be re-decided by editing this block alone, without
# reopening the structural grammar (which is position, and is settled here).
# Add an accepted spelling by appending a token; first match wins.
# ---------------------------------------------------------------------------
_WIP_ROADMAP_SHIPPED_MARKERS=("✅")
_WIP_ROADMAP_SHIPPED_KEYWORD="shipped"

# _wip_roadmap_shipped_run <text> <date_var> <rest_var>
# Parse a marker RUN anchored at the HEAD of <text>: an accepted marker token,
# then an optional `shipped` keyword, then an optional ISO date. On a match write
# the date (or "") to <date_var>, the text following the run to <rest_var>, and
# return 0; return 1 when <text> does not open with a marker token. Glob-matched,
# not regex-matched: the marker glyph is multibyte and `==` globbing stays
# locale-robust where `=~` need not.
_wip_roadmap_shipped_run() {
  local _run_text="$1"
  # shellcheck disable=SC2178
  local -n _run_date="$2"
  # shellcheck disable=SC2178
  local -n _run_rest="$3"

  local _run_token="" _run_m
  for _run_m in "${_WIP_ROADMAP_SHIPPED_MARKERS[@]}"; do
    if [[ "$_run_text" == "$_run_m"* ]]; then
      _run_token="$_run_m"
      break
    fi
  done
  [[ -n "$_run_token" ]] || return 1

  local _run_tail="${_run_text#"$_run_token"}"
  _run_tail="${_run_tail#"${_run_tail%%[![:space:]]*}"}" # ltrim
  if [[ "$_run_tail" == "$_WIP_ROADMAP_SHIPPED_KEYWORD"* ]]; then
    _run_tail="${_run_tail#"$_WIP_ROADMAP_SHIPPED_KEYWORD"}"
    _run_tail="${_run_tail#"${_run_tail%%[![:space:]]*}"}" # ltrim
  fi
  _run_date=""
  if [[ "$_run_tail" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2})(.*)$ ]]; then
    _run_date="${BASH_REMATCH[1]}"
    _run_tail="${BASH_REMATCH[2]}"
  fi
  _run_rest="$_run_tail"
  return 0
}

# _wip_roadmap_extract_shipped <text> <shipped_var> <date_var> <rest_var> [anchor]
#
# POSITIONAL shipped-marker parser. A marker counts only where the grammar puts
# it — never "the glyph appears somewhere in the text". The old substring test
# marked a step shipped merely because its body QUOTED the marker (discussing
# marker grammar, or naming it in a code span) and then scavenged `shipped
# <date>` from that prose, which is how an unstarted step got reported shipped
# and raised phantom `shipped-not-archived` drift in `doctor`.
#
#   anchor=head — bullet form. <text> is the post-`**` remainder (`srest`) of a
#     `- **step-NN — Title**` bullet, so a real marker is the FIRST thing in it,
#     sitting immediately after the title's closing `**`:
#       - **step-02 — B** ✅ shipped 2026-06-27 (small) — body mentioning ✅.
#     <rest_var> receives the clean descriptive tail with the marker run removed
#     (ltrimmed) — the ship writer reuses it verbatim when rebuilding the bullet.
#
#   anchor=tail (default) — heading form. <text> is a `## Round N — …` /
#     `### step-NN — …` title, whose marker terminates the line. The trailing run
#     must be a PURE marker run (marker, optional keyword, optional date, then
#     nothing) — a glyph mid-prose is not a marker. <rest_var> receives the title
#     with that run stripped (rtrimmed).
#
# This function owns WHERE a marker may appear; WHICH spellings count is decided
# only in the SHIPPED-MARKER SPELLING block above.
_wip_roadmap_extract_shipped() {
  local _es_text="$1" _es_anchor="${5:-tail}"
  # shellcheck disable=SC2178
  local -n _shipped="$2"
  # shellcheck disable=SC2178
  local -n _date="$3"
  # shellcheck disable=SC2178
  local -n _rest="$4"

  local _es_date="" _es_rest=""
  _shipped="false"
  _date=""

  if [[ "$_es_anchor" == "head" ]]; then
    local _es_head="${_es_text#"${_es_text%%[![:space:]]*}"}" # ltrim
    if _wip_roadmap_shipped_run "$_es_head" _es_date _es_rest; then
      _shipped="true"
      _date="$_es_date"
      _rest="${_es_rest#"${_es_rest%%[![:space:]]*}"}" # ltrim the clean tail
    else
      _rest="$_es_head"
    fi
    return 0
  fi

  # tail: a marker run, when present, must END the text. Scan from the LAST
  # occurrence of each accepted token so a marker quoted earlier in the title
  # does not shadow the real trailing one.
  local _es_m _es_prefix _es_suffix
  for _es_m in "${_WIP_ROADMAP_SHIPPED_MARKERS[@]}"; do
    [[ "$_es_text" == *"$_es_m"* ]] || continue
    _es_prefix="${_es_text%"$_es_m"*}"
    _es_suffix="${_es_m}${_es_text##*"$_es_m"}"
    if _wip_roadmap_shipped_run "$_es_suffix" _es_date _es_rest &&
      [[ -z "${_es_rest//[[:space:]]/}" ]]; then
      _shipped="true"
      _date="$_es_date"
      _rest="${_es_prefix%"${_es_prefix##*[![:space:]]}"}" # rtrim
      return 0
    fi
  done
  _rest="${_es_text%"${_es_text##*[![:space:]]}"}" # rtrim
  return 0
}

# _wip_roadmap_extract_tracker <text> — echo the issue id from a
# `[tracker: <ID>]` marker in <text>, or empty. <ID> is the ADR-0026 union: a
# Linear key (letters, then `-`, then digits — e.g. BDS-22) OR a github/gitlab
# ref (`#123`, `owner/repo#123`, nested `grp/sub/proj#45`). First match wins.
# The union is the outermost group, so `BASH_REMATCH[1]` is the whole id
# regardless of which branch matched (inner groups capture the optional
# owner/repo prefix and are unused). MIRRORS `_wip_tracker_id_valid`
# (wip-plumbing-tracker-lib.bash); both must stay in step. The bracketed form
# keeps it unambiguous against prose and survives the shipped-marker strip
# (ADR-0019 §C: the roadmap node body authors the key).
_wip_roadmap_extract_tracker() {
  local text="$1"
  if [[ "$text" =~ \[tracker:\ *([A-Z][A-Z0-9]*-[0-9]+|([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+)?#[0-9]+)\ *\] ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  fi
}

# slugify: lowercase + non-alphanumeric -> `-` + trim.
_wip_roadmap_slugify() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' |
    sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//'
}

# wip_roadmap_active_round <doc> <step_id> — emit {n,title,shipped} of the
# round containing <step_id>, or `null`.
wip_roadmap_active_round() {
  local doc="$1" sid="$2"
  jq -c --arg sid "$sid" '
    .rounds[]
    | select(.steps | map(.id) | index($sid) != null)
    | {n, title, shipped}
    | . // null
  ' <<<"$doc" | head -1
}

# wip_roadmap_first_unshipped <doc> — emit {round_n,id,title,lane} of the very
# first unshipped step in declared order, or `null`.
wip_roadmap_first_unshipped() {
  local doc="$1"
  jq -c '
    [ .rounds[] as $r | $r.steps[] | select(.shipped == false) | {round_n: $r.n, id, title, lane} ]
    | (.[0] // null)
  ' <<<"$doc"
}

# wip_roadmap_unshipped_after <doc> <step_id> — emit a JSON array of unshipped
# steps strictly AFTER <step_id> in declared order. If <step_id> is empty or
# not found, emit every unshipped step. Each entry carries its `lane`.
wip_roadmap_unshipped_after() {
  local doc="$1" sid="$2"
  jq -c --arg sid "$sid" '
    [ .rounds[] as $r | $r.steps[] | {round_n: $r.n, id, title, shipped, lane} ]
    | (if ($sid == "" or ([.[].id] | index($sid)) == null)
       then map(select(.shipped == false) | del(.shipped))
       else .[([.[].id] | index($sid)) + 1 :]
            | map(select(.shipped == false) | del(.shipped))
       end)
  ' <<<"$doc"
}

# wip_roadmap_step <doc> <step_id> — emit the step record (id,title,shipped,
# shipped_date,lane) or `null`.
wip_roadmap_step() {
  local doc="$1" sid="$2"
  jq -c --arg sid "$sid" '
    [ .rounds[] | .steps[] | select(.id == $sid) ] | (.[0] // null)
  ' <<<"$doc"
}

# wip_roadmap_lanes_in_round <doc> <round_n> — emit a JSON array of the lane
# names declared in round <round_n> (in declared order), or `[]`. Includes lanes
# declared with `### Lane <name>` even when they carry no steps yet.
wip_roadmap_lanes_in_round() {
  local doc="$1" n="$2"
  jq -c --argjson n "$n" '
    [ .rounds[] | select(.n == $n) | .lanes[] ]
  ' <<<"$doc"
}

# _wip_archived_workplan_exists <archive_dir> <step_id> — return 0 if an
# archived workplan for <step_id> lives in <archive_dir>, else 1. "Archived"
# means a non-sidecar file `step-<token>-*.md` exists, where token = <step_id>
# minus the leading `step-`. The trailing `-` in the glob guards `step-1` from
# matching `step-12`. Basenames ending `-rolling-context.md` are excluded — the
# rolling-context sidecar is not the workplan. Runs in a subshell with
# `nullglob` so a missing dir or no match is a clean negative. Pure bash, no jq.
# Single-sources "archived" for both `doctor` and `status`.
_wip_archived_workplan_exists() {
  local archive_dir="$1" step_id="$2"
  local token="${step_id#step-}"
  (
    shopt -s nullglob
    local f base
    for f in "$archive_dir"/step-"$token"-*.md; do
      base="${f##*/}"
      [[ "$base" == *-rolling-context.md ]] && continue
      exit 0
    done
    exit 1
  )
}
