# wip-plumbing-roadmap-lib.bash — parse `roadmap.md` into a JSON document.
# Sourced by bin/wip-plumbing. Pure bash + jq. Portable on BSD awk systems —
# we use bash `=~` rematching, not gawk capture groups.
#
# Grammar (per the workplan for step-08):
#   Round heading : ## Round <N> — <title> [✅ shipped <YYYY-MM-DD>]
#   Step bullet   : - **step-<NN[.5]> — <title>** [✅] [shipped <YYYY-MM-DD>] — <body>
#   Step heading  : ### step-<NN[.5]> — <title>           (amendment form)
#   Sections      : ## Deferred / ## Backlog              (Backlog parsed as entries)
#   Backlog entry : - **<title>** [— <body>] | (<body>)   (id = slugify(title))
# shellcheck shell=bash

# wip_roadmap_parse <path> — emit a single JSON doc:
#   {rounds:[{n,title,shipped,shipped_date,steps:[{id,title,shipped,shipped_date}]}],
#    backlog:[{id,title}]}
# Missing path => empty doc.
wip_roadmap_parse() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    jq -nc '{rounds:[], backlog:[]}'
    return 0
  fi

  local mode="outside" line
  local doc='{"rounds":[],"backlog":[]}'

  while IFS= read -r line || [[ -n "$line" ]]; do
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
          steps: []
        }]' <<<"$doc")"
      mode="round"
      continue
    fi
    if [[ "$line" =~ ^\#\#\ Backlog ]]; then
      mode="backlog"
      continue
    fi
    if [[ "$line" =~ ^\#\#\ Deferred ]]; then
      mode="deferred"
      continue
    fi
    if [[ "$line" =~ ^\#\#[^\#] ]]; then
      mode="outside"
      continue
    fi

    case "$mode" in
      round)
        if [[ "$line" =~ ^-\ \*\*step-([0-9]+(\.[0-9]+)?)\ —\ (.+)\*\*(.*)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local srest="${BASH_REMATCH[4]}"
          local sshipped="false" sdate="" sdummy="$srest"
          _wip_roadmap_extract_shipped "$srest" sshipped sdate sdummy
          doc="$(jq -c \
            --arg id "$sid" --arg title "$stitle" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end)
            }]' <<<"$doc")"
        elif [[ "$line" =~ ^\#\#\#\ step-([0-9]+(\.[0-9]+)?)\ —\ (.+)$ ]]; then
          local sid="step-${BASH_REMATCH[1]}"
          local stitle="${BASH_REMATCH[3]}"
          local sshipped="false" sdate="" sdummy="$stitle"
          _wip_roadmap_extract_shipped "$stitle" sshipped sdate sdummy
          doc="$(jq -c \
            --arg id "$sid" --arg title "$sdummy" \
            --argjson shipped "$sshipped" --arg sdate "$sdate" '
            .rounds[-1].steps += [{
              id: $id, title: $title, shipped: $shipped,
              shipped_date: (if $sdate == "" then null else $sdate end)
            }]' <<<"$doc")"
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
    esac
  done <"$path"

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

# wip_roadmap_first_unshipped <doc> — emit {round_n,id,title} of the very
# first unshipped step in declared order, or `null`.
wip_roadmap_first_unshipped() {
  local doc="$1"
  jq -c '
    [ .rounds[] as $r | $r.steps[] | select(.shipped == false) | {round_n: $r.n, id, title} ]
    | (.[0] // null)
  ' <<<"$doc"
}

# wip_roadmap_unshipped_after <doc> <step_id> — emit a JSON array of unshipped
# steps strictly AFTER <step_id> in declared order. If <step_id> is empty or
# not found, emit every unshipped step.
wip_roadmap_unshipped_after() {
  local doc="$1" sid="$2"
  jq -c --arg sid "$sid" '
    [ .rounds[] as $r | $r.steps[] | {round_n: $r.n, id, title, shipped} ]
    | (if ($sid == "" or ([.[].id] | index($sid)) == null)
       then map(select(.shipped == false) | del(.shipped))
       else .[([.[].id] | index($sid)) + 1 :]
            | map(select(.shipped == false) | del(.shipped))
       end)
  ' <<<"$doc"
}

# wip_roadmap_step <doc> <step_id> — emit the step record (id,title,shipped,
# shipped_date) or `null`.
wip_roadmap_step() {
  local doc="$1" sid="$2"
  jq -c --arg sid "$sid" '
    [ .rounds[] | .steps[] | select(.id == $sid) ] | (.[0] // null)
  ' <<<"$doc"
}
