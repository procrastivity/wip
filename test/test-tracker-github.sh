#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-github"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# The github issue-tracker backend (ADR-0026, BDS-75) over the `gh issue` CLI.
# Hermetic: every `gh` call lands on a PATH stub, never a network. The stub is not
# a dumb echo — for `issue view` it replays canned JSON through real `jq` using the
# adapter's OWN `--jq` filter, so these assertions exercise the real reduction
# rather than a reimplementation of it. For writes it records argv.
#
# The adapter is NOT sourced directly: the transport lib's glob loader pulls it in.
# Letting the loader do it is what proves the loader works.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
# shellcheck source=lib/wip/wip-plumbing-tracker-lib.bash
source lib/wip/wip-plumbing-tracker-lib.bash
# shellcheck source=lib/wip/wip-plumbing-tracker-transport-lib.bash
source lib/wip/wip-plumbing-tracker-transport-lib.bash
WIP=bin/wip-plumbing

# --- the gh PATH stub --------------------------------------------------------
# Canned JSON is faithful to live gh 2.96.0: `state` is uppercase OPEN/CLOSED;
# `stateReason` is "" when open, COMPLETED/NOT_PLANNED when closed; `labels` is an
# array of OBJECTS (hence the adapter's `[.labels[].name]`).
stub="$(wip_mktemp)"
export GH_LOG="$stub/gh.log"
cat >"$stub/gh" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_LOG"          # record argv for the write assertions
[[ "$1 $2" == "issue view" ]] || exit 0 # edit/close: record-only
n="$3"; shift 3
filter=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --jq) filter="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$n" in
  1) json='{"state":"OPEN","stateReason":"","labels":[]}' ;;
  2) json='{"state":"OPEN","stateReason":"","labels":[{"name":"wip:in-progress"}]}' ;;
  3) json='{"state":"OPEN","stateReason":"","labels":[{"name":"bug"},{"name":"wip:in-review"}]}' ;;
  4) json='{"state":"CLOSED","stateReason":"COMPLETED","labels":[{"name":"wip:in-progress"}]}' ;;
  5) json='{"state":"CLOSED","stateReason":"NOT_PLANNED","labels":[]}' ;;
  6) json='{"state":"OPEN","stateReason":"","labels":[{"name":"wip:in-progress"},{"name":"wip:in-review"}]}' ;;
  7) json='{"state":"OPEN","stateReason":"","labels":[]}' ;;
  *) exit 1 ;;
esac
printf '%s' "$json" | jq -r "$filter"
STUB
chmod +x "$stub/gh"
export PATH="$stub:$PATH"

# Invoke a resolved command EXACTLY as production does (sync.bash:112/144,
# doctor.bash:214) — a fresh shell with the issue appended as a positional arg —
# so the tests cover the real calling convention, not a convenient approximation.
run_read() { # <cmd> <issue>
  bash -c "$1 \"\$1\"" _ "$2"
}
run_write() { # <cmd> <issue> <token>
  bash -c "$1 \"\$1\" \"\$2\"" _ "$2" "$3"
}
gh_log_reset() { : >"$GH_LOG"; }
gh_log() { cat "$GH_LOG"; }

# --- 1. command resolution (proves the glob loader + the dispatcher) ---------
read_cmd="$(_wip_tracker_transport_read_cmd github)"
assert_eq "yes" "$([[ -n "$read_cmd" ]] && echo yes || echo no)" \
  "glob loader + dispatch resolve a github read cmd"

# --- 2. env seams (dispatcher precedence, ADR-0026 §Decision 2) --------------
assert_eq "MY_READ" "$(WIP_GITHUB_READ_CMD=MY_READ _wip_tracker_transport_read_cmd github)" \
  "WIP_GITHUB_READ_CMD overrides the default read snippet"
assert_eq "GENERIC_READ" \
  "$(WIP_TRACKER_READ_CMD=GENERIC_READ WIP_GITHUB_READ_CMD=MY_READ _wip_tracker_transport_read_cmd github)" \
  "generic WIP_TRACKER_READ_CMD beats the WIP_GITHUB_READ_CMD seam"

# --- 3. all five states through the stub ------------------------------------
assert_eq "todo" "$(run_read "$read_cmd" '#1')" "open, no labels -> todo"
assert_eq "in-progress" "$(run_read "$read_cmd" '#2')" "open + wip:in-progress -> in-progress"
assert_eq "in-review" "$(run_read "$read_cmd" '#3')" "open + wip:in-review (among others) -> in-review"
assert_eq "done" "$(run_read "$read_cmd" '#4')" "closed COMPLETED -> done"
assert_eq "canceled" "$(run_read "$read_cmd" '#5')" "closed NOT_PLANNED -> canceled"

# --- 4. the COMPLETED/NOT_PLANNED split -------------------------------------
# #4 and #5 are BOTH CLOSED and differ only by stateReason. This is the assertion
# that pins done != canceled — the whole reason the read fetches stateReason.
assert_eq "done canceled" "$(printf '%s %s' "$(run_read "$read_cmd" '#4')" "$(run_read "$read_cmd" '#5')")" \
  "closed done vs canceled split purely on stateReason"

# --- 5. arm precedence ------------------------------------------------------
assert_eq "in-review" "$(run_read "$read_cmd" '#6')" \
  "both middle labels -> in-review (in-review arm precedes in-progress)"
assert_eq "done" "$(run_read "$read_cmd" '#4')" \
  "closed with a STALE wip:in-progress -> done (CLOSED arm precedes the label arms)"

# --- 6. ref parsing (assert against the argv log) ---------------------------
gh_log_reset
assert_eq "todo" "$(run_read "$read_cmd" '#1')" "bare #N resolves"
assert_not_grep "\-\-repo" "$GH_LOG" "bare #N emits NO --repo (gh infers from the cwd remote)"

gh_log_reset
assert_eq "in-review" "$(run_read "$read_cmd" 'octo/hello#3')" "qualified owner/repo#N resolves"
assert_grep "issue view 3 \-\-repo octo/hello " "$GH_LOG" "qualified ref emits --repo octo/hello"

gh_log_reset
assert_eq "canceled" "$(run_read "$read_cmd" 'grp/sub/proj#5')" "nested group/sub/proj#N resolves"
assert_grep "issue view 5 \-\-repo grp/sub/proj " "$GH_LOG" "nested ref emits --repo grp/sub/proj"

# --- 7. write composition ---------------------------------------------------
write_cmd="$(_wip_tracker_transport_write_cmd github)"
assert_eq "yes" "$([[ -n "$write_cmd" ]] && echo yes || echo no)" \
  "glob loader + dispatch resolve a github write cmd"
assert_eq "MY_WRITE" "$(WIP_GITHUB_WRITE_CMD=MY_WRITE _wip_tracker_transport_write_cmd github)" \
  "WIP_GITHUB_WRITE_CMD overrides the default write snippet"
assert_eq "GENERIC_WRITE" \
  "$(WIP_TRACKER_WRITE_CMD=GENERIC_WRITE WIP_GITHUB_WRITE_CMD=MY_WRITE _wip_tracker_transport_write_cmd github)" \
  "generic WIP_TRACKER_WRITE_CMD beats the WIP_GITHUB_WRITE_CMD seam"

# One gh call per token, exactly per the Chunk 2 table.
assert_write() { # <token> <expected-argv>
  local token="$1" expected="$2"
  gh_log_reset
  run_write "$write_cmd" '#7' "$token"
  assert_eq "$expected" "$(gh_log)" "write $token -> $expected"
}
assert_write todo "issue edit 7 --remove-label wip:in-progress --remove-label wip:in-review"
assert_write in-progress "issue edit 7 --add-label wip:in-progress --remove-label wip:in-review"
assert_write in-review "issue edit 7 --add-label wip:in-review --remove-label wip:in-progress"
assert_write "done" "issue close 7 --reason completed"
assert_write canceled "issue close 7 --reason not planned"

# D1: `canceled` carries NO wip:canceled label — NOT_PLANNED is the native carrier,
# and the read never consults such a label. github needs only TWO labels to pre-exist.
assert_not_grep "wip:canceled" "$GH_LOG" "canceled write emits NO wip:canceled label (D1)"

# The write splits a qualified ref the same way the read does.
gh_log_reset
run_write "$write_cmd" 'octo/hello#7' in-progress
assert_eq "issue edit 7 --repo octo/hello --add-label wip:in-progress --remove-label wip:in-review" \
  "$(gh_log)" "qualified ref -> write emits --repo octo/hello"

# An unknown token fails loudly: rc 2 and ZERO gh calls (sync buckets it as
# skipped{write failed} rather than falsely reporting it applied).
gh_log_reset
set +e
run_write "$write_cmd" '#7' bogus
rc=$?
set -e
assert_eq "2" "$rc" "unknown write token -> rc 2"
assert_eq "" "$(gh_log)" "unknown write token makes ZERO gh calls"

# --- 8. end-to-end: `sync github` through the real binary --------------------
# The adapter reaches sync only via the glob loader, so this proves the whole
# chain: loader -> dispatcher -> emitted snippet -> gh. wip's cache floor for
# step-01 is `in-review`; the tracker (stub #7) reads `todo`, i.e. BEHIND wip, so
# sync applies the forward transition. The roadmap MUST carry the matching
# `[tracker: ...]` marker — the mirror-drift gate compares roadmap vs manifest and
# dies with exit 4 if they disagree.
e="$(wip_mktemp)"
mkdir -p "$e/.wip/initiatives/demo"
cat >"$e/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: github } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_map: { step-01: "octo/hello#7" }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$e/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** — x. [tracker: octo/hello#7]
MD
_wip_tracker_cache_set "$e" "demo/step-01" "in-review" "ship" "2026-07-11" >/dev/null

gh_log_reset
s="$(WIP_ROOT="$e" $WIP sync github)"
assert_eq "true" "$(jq -r '.ok' <<<"$s")" "sync github ok"
assert_eq "cli" "$(jq -r '.transport' <<<"$s")" "the glob-loaded adapter wires a write -> transport cli"
assert_eq "1" "$(jq -r '.applied | length' <<<"$s")" "sync github applies the one forward transition"
assert_eq "demo/step-01" "$(jq -r '.applied[0].node' <<<"$s")" "step-01 applied"
# `in-review` (not "In Review"): the token IS wip's semantic vocabulary, so it rides
# the `*)` passthrough in _wip_tracker_provider_state untouched — no github arm added.
assert_eq "in-review" "$(jq -r '.applied[0].to' <<<"$s")" \
  "target rides the *) passthrough as a semantic token (no provider arm needed)"
# ...and the edit actually fired through to gh, rather than merely being planned.
assert_grep "issue edit 7 \-\-repo octo/hello \-\-add-label wip:in-review" "$GH_LOG" \
  "the wip:in-review edit really fired through the glob-loaded adapter"

test_summary
