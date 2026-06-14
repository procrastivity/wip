# wip-intake-shaper-lib.bash — LLM glue for the `wip intake` porcelain.
#
# Builds the system + user messages sent to the provider; parses responses
# (shape body vs ---ASK--- fenced clarifying question). Pure bash + jq.
# I/O lives in wip-subcommands/intake.bash; this lib is pure data transforms
# so the shape rules and ASK protocol are auditable in one place.
# shellcheck shell=bash

# wip_shaper_system_prompt <kind> — emit the system prompt for <kind>.
# The shaper preamble + per-kind shape rules live at
# templates/prompts/intake/{preamble,<kind>}.md and are SHARED with the
# /wip:* Claude Code plugin (step-11). Resolution order for the templates
# dir:
#   1. $WIP_TEMPLATES_DIR (explicit override; test seam + install seam)
#   2. $WIP_LIB/../templates (i.e. repo's templates/ next to lib/wip/)
# An unknown kind emits "Target kind: <kind> — unknown.\n" to match the
# legacy heredoc fallback exactly.
wip_shaper_system_prompt() {
  local kind="$1"
  local preamble
  preamble="$(_wip_shaper_preamble)"
  local rules
  rules="$(_wip_shaper_rules "$kind")"
  printf '%s\n\n%s\n' "$preamble" "$rules"
}

_wip_shaper_templates_dir() {
  if [[ -n "${WIP_TEMPLATES_DIR:-}" ]]; then
    printf '%s' "$WIP_TEMPLATES_DIR"
    return 0
  fi
  local lib
  # shellcheck disable=SC1007  # CDPATH= prefixes the cd command (neutralize CDPATH), not an assignment
  lib="${WIP_LIB:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)}"
  # lib/wip/ sits next to templates/ under the repo root.
  # shellcheck disable=SC1007
  CDPATH= cd -- "$lib/../../templates" 2>/dev/null && pwd
}

_wip_shaper_read_template() {
  local name="$1"
  local dir
  dir="$(_wip_shaper_templates_dir)"
  local path="$dir/prompts/intake/$name.md"
  if [[ ! -f "$path" ]]; then
    return 1
  fi
  cat -- "$path"
}

_wip_shaper_preamble() {
  _wip_shaper_read_template preamble
}

_wip_shaper_rules() {
  local kind="$1"
  case "$kind" in
    brief | amendment | workplan-seed | spec | handoff)
      _wip_shaper_read_template "$kind"
      ;;
    *)
      printf 'Target kind: %s — unknown.\n' "$kind"
      ;;
  esac
}

# wip_shaper_extract_response <raw> — classify a shaper response.
# Emits JSON: {mode, question, why, body}. `mode` is one of:
#   - "ask"      — `---ASK---` fence present; question + why extracted
#   - "shape"    — no fence; body is the shaped artifact (outer ```markdown
#                  fence stripped if present)
#   - "invalid"  — body is empty after trimming / ASK fence malformed
wip_shaper_extract_response() {
  local raw="$1"

  # Trim leading/trailing blank lines but preserve interior whitespace.
  local trimmed
  trimmed="$(printf '%s' "$raw" | awk '
    BEGIN { started = 0; last = 0 }
    { lines[NR] = $0; if ($0 !~ /^[[:space:]]*$/) { if (!started) first = NR; started = 1; last = NR } }
    END { for (i = first; i <= last; i++) print lines[i] }
  ')"

  if [[ -z "$trimmed" ]]; then
    jq -nc '{mode:"invalid", question:"", why:"", body:""}'
    return 0
  fi

  # Detect ASK fence.
  if printf '%s\n' "$trimmed" | grep -qE '^---ASK---[[:space:]]*$'; then
    local q why
    q="$(printf '%s\n' "$trimmed" | awk '
      /^---ASK---[[:space:]]*$/ { in_block = 1; next }
      /^---END---[[:space:]]*$/ { in_block = 0; next }
      in_block && /^question:[[:space:]]*/ { sub(/^question:[[:space:]]*/, ""); print; exit }
    ')"
    why="$(printf '%s\n' "$trimmed" | awk '
      /^---ASK---[[:space:]]*$/ { in_block = 1; next }
      /^---END---[[:space:]]*$/ { in_block = 0; next }
      in_block && /^why:[[:space:]]*/ { sub(/^why:[[:space:]]*/, ""); print; exit }
    ')"
    if [[ -z "$q" ]]; then
      jq -nc --arg body "$trimmed" '{mode:"invalid", question:"", why:"", body:$body}'
      return 0
    fi
    jq -nc --arg q "$q" --arg w "$why" '
      {mode:"ask", question:$q, why:$w, body:""}'
    return 0
  fi

  # Strip outer ```markdown / ``` fences if present.
  local stripped="$trimmed"
  if printf '%s\n' "$trimmed" | head -1 | grep -qE '^```'; then
    stripped="$(printf '%s\n' "$trimmed" | awk '
      NR == 1 && /^```/ { next }
      { lines[++n] = $0 }
      END {
        if (n > 0 && lines[n] ~ /^```[[:space:]]*$/) n--
        for (i = 1; i <= n; i++) print lines[i]
      }
    ')"
  fi

  if [[ -z "$stripped" ]]; then
    jq -nc '{mode:"invalid", question:"", why:"", body:""}'
    return 0
  fi

  jq -nc --arg body "$stripped" '{mode:"shape", question:"", why:"", body:$body}'
}

# wip_shaper_initial_user_message <kind> <classify-json> <file> [<yes-mode>]
# Build the first user message body for the shape request.
wip_shaper_initial_user_message() {
  local kind="$1" classify_json="$2" file="$3" yes_mode="${4:-0}"
  local body
  body="$(cat "$file")"
  local guidance=""
  if [[ "$yes_mode" == "1" ]]; then
    guidance="Mode: non-interactive (--yes). Do NOT emit ASK. Use your best
judgment for missing facts; mark every guess in a final
\`## TODO (shaper guesses)\` list at the end of the shaped artifact."
  else
    guidance="Mode: interactive. If a required field is missing and you
cannot guess it confidently, emit a single ASK per the protocol."
  fi
  cat <<EOF
# Original artifact

\`\`\`
$body
\`\`\`

# Classify (plumbing's best guess)

$classify_json

# Task

Shape the artifact into kind=$kind per the rules in your system prompt.
$guidance
EOF
}

# wip_shaper_retry_user_message <kind> <missing-json>
# Build the user follow-up for a validate-failure retry.
wip_shaper_retry_user_message() {
  local kind="$1" missing="$2"
  cat <<EOF
\`wip-plumbing intake validate --kind $kind\` rejected your last response.

Missing fields: $missing

Re-emit the FULL shaped artifact (no diff, no commentary) with the
missing fields filled in. Same protocol as before.
EOF
}

# wip_shaper_followup_user_message <answer>
# Build the user follow-up after answering a clarifying ASK.
wip_shaper_followup_user_message() {
  local answer="$1"
  cat <<EOF
User answer: $answer

Now emit the shaped artifact (or another ASK if a different question is
still blocking — but prefer to finish).
EOF
}
