# wip-intake-shaper-lib.bash — LLM glue for the `wip intake` porcelain.
#
# Builds the system + user messages sent to the provider; parses responses
# (shape body vs ---ASK--- fenced clarifying question). Pure bash + jq.
# I/O lives in wip-subcommands/intake.bash; this lib is pure data transforms
# so the shape rules and ASK protocol are auditable in one place.
# shellcheck shell=bash

# wip_shaper_system_prompt <kind> — emit the system prompt for <kind>.
# The shaper preamble + per-kind shape rules from intake-kinds.md §2/§3
# are inlined here. New kinds add a case below; the spec stays the source
# of truth.
wip_shaper_system_prompt() {
  local kind="$1"
  local preamble
  preamble="$(_wip_shaper_preamble)"
  local rules
  rules="$(_wip_shaper_rules "$kind")"
  printf '%s\n\n%s\n' "$preamble" "$rules"
}

_wip_shaper_preamble() {
  cat <<'EOF'
You are the SHAPER stage of the `wip intake` pipeline (ADR-0009).

Your job: take an arbitrary inbound planning artifact and rewrite it into
the canonical form for its declared kind so that the deterministic
`wip-plumbing intake validate` gate downstream accepts it.

Output protocol — exactly one of:

1. A single shaped markdown document. Emit ONLY the document body, no
   preamble, no commentary, no ``` fences. The first line of the document
   should be its YAML front-matter `---` head (when required by the kind),
   followed by the markdown body with its `# Title` heading.

2. A single clarifying question, formatted EXACTLY as:

   ---ASK---
   question: <one short sentence>
   why: <one short sentence describing what the artifact is missing>
   ---END---

   Emit nothing else. The orchestrator will inject the user's answer and
   re-issue the shape request. Ask at most ONE question per turn.

Hard rules:
- Never invent facts. If you cannot fill a required section from the
  artifact, ASK or (when told to skip questions) add a TODO list at the
  end of the shaped artifact under `## TODO (shaper guesses)`.
- Never emit both a shaped document and an ASK in the same response.
- Never wrap the shaped document in code fences.
- Preserve the original artifact's intent and prose voice; restructure
  rather than rewrite-from-scratch where possible.
EOF
}

_wip_shaper_rules() {
  local kind="$1"
  case "$kind" in
    brief)
      cat <<'EOF'
Target kind: brief — a new initiative.

Required shape (validator rules from intake-kinds.md §2):
- Title heading: `# <Title>`.
- One of: `## Goal` OR `## Summary` (one is required).
- Optional YAML front-matter with `slug: <kebab-case>` if a slug should be
  forced; otherwise the slug is derived from the H1.
- Do NOT add `target:` referencing an existing initiative slug. If the
  artifact is about an existing initiative, return an ASK clarifying
  whether this is a new initiative or an amendment.
EOF
      ;;
    amendment)
      cat <<'EOF'
Target kind: amendment — an edit to an existing initiative's roadmap.

Required shape (validator rules from intake-kinds.md §2/§3):
- YAML front-matter MUST include:
  - `target: <initiative-slug>` — the slug being amended.
  - exactly ONE directive: `insert-after: step-NN`, `replace: step-NN`,
    or `append-round: <Round title>`.
- A title heading (`# <Title>`) below the front-matter.
- Body content per the directive:
  - `insert-after` / `replace`: include a `### step-XX — <title>` heading
    where `XX` is the new step's id (may be a `.5` slot per the
    distillation convention) plus a one-or-more-paragraph body.
  - `append-round`: include `## Round <N> — <title>` plus at least one
    `### step-NN — <title>` entry.

If `target:`, the directive, or the step id is unknown, return an ASK.
EOF
      ;;
    workplan-seed)
      cat <<'EOF'
Target kind: workplan-seed — input narrative for a specific step's workplan.

Required shape:
- YAML front-matter with `target: <slug>/<step-id>` (the slug AND the
  step-id, separated by `/`).
- A title heading (`# <Title>`) below the front-matter.
- Narrative body (no required section set).

If either the slug or the step-id is unclear, return an ASK.
EOF
      ;;
    spec)
      cat <<'EOF'
Target kind: spec — an LDS-shaped feature spec.

Required shape (minimal fallback rules; LDS delegation is out of scope):
- Title heading (`# <Title>`).
- `## Summary` section.
- One of `## User stories` OR `## Requirements`.
EOF
      ;;
    handoff)
      cat <<'EOF'
Target kind: handoff — loose narrative.

Required shape:
- Title heading (`# <Title>`) and parseable markdown body.

Note: `handoff` is not a terminal kind. The pipeline will refuse to
`apply` it. You typically reach this kind only when the user has
explicitly forced `--kind handoff`.
EOF
      ;;
    *)
      cat <<EOF
Target kind: $kind — unknown.
EOF
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
