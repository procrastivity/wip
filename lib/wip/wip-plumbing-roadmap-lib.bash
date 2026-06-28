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
#    lane_errors:[{kind, round?, lane?, step?}]}
# `lane` is null for a main-lane step; `lanes` lists a round's declared lanes in
# order (incl. empty lanes); `lane_errors` is empty when the lane structure is
# well-formed (ADR-0010). Missing path => empty doc (same shape, all arrays empty).
wip_roadmap_parse() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    jq -nc '{rounds:[], backlog:[], deferred:[], lane_errors:[]}'
    return 0
  fi

  local mode="outside" line current_lane="" lane_saw_step=0 in_comment=0
  local doc='{"rounds":[],"backlog":[],"deferred":[],"lane_errors":[]}'

  while IFS= read -r line || [[ -n "$line" ]]; do
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
      local shipped="false" shipped_date="" title="$rest"
      _wip_roadmap_extract_shipped "$rest" shipped shipped_date title
      doc="$(jq -c \
        --argjson n "$n" --arg title "$title" \
        --argjson shipped "$shipped" --arg shipped_date "$shipped_date" '
        .rounds += [{
          n: $n, title: $title, shipped: $shipped,
          shipped_date: (if $shipped_date == "" then null else $shipped_date end),
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
          # Trim trailing whitespace from the lane name.
          lane_name="${lane_name%"${lane_name##*[![:space:]]}"}"
          doc="$(jq -c --arg lane "$lane_name" '
            if (.rounds[-1].lanes | index($lane)) != null
            then .lane_errors += [{kind:"duplicate-lane", round:.rounds[-1].n, lane:$lane}]
            else .rounds[-1].lanes += [$lane]
            end' <<<"$doc")"
          current_lane="$lane_name"
          lane_saw_step=0
        elif [[ "$line" =~ ^-\ \*\*step-([0-9]+(\.[0-9]+)?)\ —\ ([^*]+)\*\*(.*)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local srest="${BASH_REMATCH[4]}"
          local sshipped="false" sdate="" sdummy="$srest"
          _wip_roadmap_extract_shipped "$srest" sshipped sdate sdummy
          doc="$(jq -c \
            --arg id "$sid" --arg title "$stitle" --arg lane "$current_lane" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end),
              lane: (if $lane == "" then null else $lane end)
            }]' <<<"$doc")"
          [[ -n "$current_lane" ]] && lane_saw_step=1
        elif [[ "$line" =~ ^\#\#\#\ step-([0-9]+(\.[0-9]+)?)\ —\ (.+)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local sshipped="false" sdate="" sdummy="$stitle"
          _wip_roadmap_extract_shipped "$stitle" sshipped sdate sdummy
          doc="$(jq -c \
            --arg id "$sid" --arg title "$sdummy" --arg lane "$current_lane" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end),
              lane: (if $lane == "" then null else $lane end)
            }]' <<<"$doc")"
          [[ -n "$current_lane" ]] && lane_saw_step=1
        fi
        ;;
      backlog)
        if [[ "$line" =~ ^-\ \*\*([^*]+)\*\*(.*)$ ]]; then
          local btitle="${BASH_REMATCH[1]}"
          local bid
          bid="$(_wip_roadmap_slugify "$btitle")"
          doc="$(jq -c --arg id "$bid" --arg title "$btitle" '
            .backlog += [{id:$id, title:$title}]' <<<"$doc")"
        fi
        ;;
      deferred)
        # Mirror the backlog arm: a `- **<title>**` bullet becomes a
        # {id, title} deferred entry (id = slugify(title)). Plain (non-bold)
        # bullets under `## Deferred` are not collected, same as backlog.
        if [[ "$line" =~ ^-\ \*\*([^*]+)\*\*(.*)$ ]]; then
          local dtitle="${BASH_REMATCH[1]}"
          local did
          did="$(_wip_roadmap_slugify "$dtitle")"
          doc="$(jq -c --arg id "$did" --arg title "$dtitle" '
            .deferred += [{id:$id, title:$title}]' <<<"$doc")"
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

# _wip_roadmap_extract_shipped <rest> <shipped_var> <date_var> <title_var>
# Inspects <rest> for "✅" + optional " shipped YYYY-MM-DD"; writes results
# into the named variables. <title_var> is trimmed of the trailing ✅… run.
_wip_roadmap_extract_shipped() {
  local rest="$1"
  # shellcheck disable=SC2178
  local -n _shipped="$2"
  # shellcheck disable=SC2178
  local -n _date="$3"
  # shellcheck disable=SC2178
  local -n _title="$4"
  if [[ "$rest" == *"✅"* ]]; then
    _shipped="true"
    if [[ "$rest" =~ shipped\ ([0-9]{4}-[0-9]{2}-[0-9]{2}) ]]; then
      _date="${BASH_REMATCH[1]}"
    fi
    # Strip the trailing "✅..." run from the title.
    _title="${rest%%✅*}"
    _title="${_title%"${_title##*[![:space:]]}"}"
  else
    _shipped="false"
    _date=""
    _title="$rest"
    _title="${_title%"${_title##*[![:space:]]}"}"
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
