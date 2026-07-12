#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="backlog-verb"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# ---------------------------------------------------------------------------
# Scope: the COMPOSED `backlog retire <tracker-id>` verb driven END-TO-END
# through `bin/wip-plumbing backlog` — arg surface, root resolution, the JSON
# ledger, and the dispatcher wiring that makes the verb reachable at all. Every
# case here invokes the BINARY, never `_wip_backlog_retire_entry` directly: a lib
# function that works while the dispatcher's case list omits `backlog` is a verb
# no operator can run, and that is precisely the wiring this file exists to pin.
#
# Seam-internal mechanics (the entry grammar, the markdown-link tracker
# fallback, the splice boundary that preserves pre-existing `- _(pruned …)_`
# history, the wrapped-title case) are owned by test-repo-backlog-parse.sh and
# test-backlog-retire.sh and are NOT re-tested here.
#
# Contract: workplan step-06 chunk 7.
#
# NB — hard boundary: every fixture is a throwaway tmp dir rooted by WIP_ROOT.
# This file never reads or writes the live repo's .wip/backlog.md.
# ---------------------------------------------------------------------------

export WIP_NO_REGISTRY=1
# Pin the pruned marker's date so the ledger and the written line are both
# assertable — the writer honors $WIP_NOW (the seam every dated writer uses).
export WIP_NOW="2026-07-12"

# setup_backlog — a fresh root carrying a manifest and a two-entry
# `.wip/backlog.md` (BDS-70, BDS-71), each multi-paragraph and each using the
# live-shaped markdown-link tracker form. Sets the globals `tmp` and `backlog`.
setup_backlog() {
  tmp="$(wip_mktemp)"
  wip_fixture_init "$tmp"
  cat >"$tmp/.wip/backlog.md" <<'EOF'
# Backlog — cross-cutting

## Nice-to-have

- **First entry, the one being retired**. Multi-paragraph prose block, exactly
  like the live file's entries.

  A second paragraph, to prove the whole block is spliced and not just the
  opening line.
  ([BDS-70](https://linear.app/beausimensen/issue/BDS-70))

- **Second entry, which must be left completely alone**. This block is the
  mutation pin: a verb that prunes "whatever entry it finds first" rather than
  matching on the tracker id will disturb it.

  Its own second paragraph.
  ([BDS-71](https://linear.app/beausimensen/issue/BDS-71))
EOF
  backlog="$tmp/.wip/backlog.md"
}

run() { WIP_ROOT="$tmp" bin/wip-plumbing backlog "$@"; }

# snapshot — copy the current backlog.md aside; echo the snapshot path.
snapshot() {
  local s
  s="$(wip_mktemp)/backlog.md"
  cp "$backlog" "$s"
  printf '%s\n' "$s"
}

# ---------------------------------------------------------------------------
# Case A — the positive retirement, through the dispatcher. The whole
#   multi-paragraph block goes (not just its bullet line), the canonical pruned
#   marker lands, and the ledger carries the `{ok, action, status, changed,
#   path}` shape `gitignore sync` established.
# ---------------------------------------------------------------------------
setup_backlog
out="$(run retire BDS-70)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "A: ok true"
assert_eq "retire" "$(jq -r '.action' <<<"$out")" "A: action retire"
assert_eq "retired" "$(jq -r '.status' <<<"$out")" "A: status retired"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "A: changed true"
assert_eq "BDS-70" "$(jq -r '.tracker' <<<"$out")" "A: ledger echoes the tracker it retired"
assert_eq "$backlog" "$(jq -r '.path' <<<"$out")" "A: ledger names the resolved backlog path"
assert_eq "null" "$(jq -r '.dry_run' <<<"$out")" "A: dry_run absent without the flag"

assert_not_grep "First entry, the one being retired" "$backlog" "A: BDS-70's title line is gone"
assert_not_grep "second paragraph, to prove the whole block is spliced" "$backlog" \
  "A: BDS-70's SECOND PARAGRAPH is gone too (the whole block was spliced)"
assert_grep '^- _(pruned 2026-07-12 → filed as BDS-70: retired via wip backlog retire\.)_$' "$backlog" \
  "A: the canonical pruned marker is appended, dated from \$WIP_NOW"

# ---------------------------------------------------------------------------
# MUTATION PIN — retire by TRACKER, not by position.
#
# The plausible-wrong verb passes whatever entry it finds first to the writer
# (or ignores the tracker arg entirely). It would pass every assertion in case A
# above, because BDS-70 IS the first entry. What it cannot survive is BDS-71
# being byte-identical afterwards. The pin is the untouched-ness of the entry
# that was NOT named — the property "retire by tracker" has and "retire by
# position" does not.
# ---------------------------------------------------------------------------
bds71_expected="$(wip_mktemp)/bds71-expected.txt"
cat >"$bds71_expected" <<'EOF'
- **Second entry, which must be left completely alone**. This block is the
  mutation pin: a verb that prunes "whatever entry it finds first" rather than
  matching on the tracker id will disturb it.

  Its own second paragraph.
  ([BDS-71](https://linear.app/beausimensen/issue/BDS-71))
EOF
bds71_actual="$(wip_mktemp)/bds71-actual.txt"
sed -n '/^- \*\*Second entry/,/issue\/BDS-71/p' "$backlog" >"$bds71_actual"
assert_cmp "$bds71_expected" "$bds71_actual" \
  "MUTATION PIN: BDS-71's block is BYTE-IDENTICAL after retiring BDS-70 (kills a retire-first stub)"

# The other half of the pin: naming the SECOND entry in a fresh fixture must
# retire IT and leave the first alone — a retire-first stub deletes BDS-70 here.
setup_backlog
bds70_expected="$(wip_mktemp)/bds70-expected.txt"
sed -n '/^- \*\*First entry/,/issue\/BDS-70/p' "$backlog" >"$bds70_expected"
out="$(run retire BDS-71)"
assert_eq "retired" "$(jq -r '.status' <<<"$out")" "MUTATION PIN: naming the SECOND entry retires it"
assert_not_grep "issue/BDS-71" "$backlog" "MUTATION PIN: BDS-71's block is gone when BDS-71 is named"
bds70_actual="$(wip_mktemp)/bds70-actual.txt"
sed -n '/^- \*\*First entry/,/issue\/BDS-70/p' "$backlog" >"$bds70_actual"
assert_cmp "$bds70_expected" "$bds70_actual" \
  "MUTATION PIN: retiring BDS-71 leaves BDS-70 BYTE-IDENTICAL (kills a retire-FIRST stub directly)"

# ---------------------------------------------------------------------------
# Case B — idempotency: a second identical invocation is a quiet noop at exit 0,
#   and writes nothing. The BYTE-IDENTICAL check is the real assertion — "exit 0
#   twice" would pass against a verb that appended a second pruned marker on
#   every run.
# ---------------------------------------------------------------------------
setup_backlog
run retire BDS-70 >/dev/null # run 1 — mutates
before="$(snapshot)"
set +e
out2="$(run retire BDS-70)" # run 2 — already retired
rc=$?
set -e
assert_eq "0" "$rc" "B: re-retiring an already-pruned tracker exits 0 (not an error)"
assert_eq "true" "$(jq -r '.ok' <<<"$out2")" "B: re-run ok true"
assert_eq "noop" "$(jq -r '.status' <<<"$out2")" "B: re-run status noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out2")" "B: re-run changed false"
assert_cmp "$before" "$backlog" "B: backlog.md BYTE-IDENTICAL across the re-run (no duplicate marker)"

# ---------------------------------------------------------------------------
# Case C — a tracker that was NEVER present is `noop` on the FIRST call, at exit
#   0. This is the convention that makes the verb safe for an operator holding a
#   list of ids without knowing which of them the backlog actually carries; it is
#   deliberately NOT `ship`'s hard-refuse-on-mismatch shape.
# ---------------------------------------------------------------------------
setup_backlog
before="$(snapshot)"
set +e
out="$(run retire BDS-99)"
rc=$?
set -e
assert_eq "0" "$rc" "C: a never-present tracker exits 0 on the FIRST call"
assert_eq "noop" "$(jq -r '.status' <<<"$out")" "C: status noop"
assert_eq "false" "$(jq -r '.changed' <<<"$out")" "C: changed false"
assert_cmp "$before" "$backlog" "C: a noop never writes"

# C2 — a root with no `.wip/backlog.md` at all is `noop`, not a crash: a repo
#   need not have a backlog to already be in the state this verb establishes.
setup_backlog
rm "$backlog"
set +e
out="$(run retire BDS-70)"
rc=$?
set -e
assert_eq "0" "$rc" "C2: a missing backlog.md exits 0"
assert_eq "noop" "$(jq -r '.status' <<<"$out")" "C2: a missing backlog.md is noop"
assert_absent "$backlog" "C2: the verb did not conjure a backlog.md"

# ---------------------------------------------------------------------------
# Case D — --dry-run: the full ledger is still computed (same status word, same
#   resolved path) but nothing is written — the mirror image of case A.
# ---------------------------------------------------------------------------
setup_backlog
before="$(snapshot)"
out="$(run retire BDS-70 --dry-run)"
assert_eq "retired" "$(jq -r '.status' <<<"$out")" "D: dry-run reports the status it WOULD have written"
assert_eq "true" "$(jq -r '.changed' <<<"$out")" "D: dry-run changed true"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "D: dry_run true"
assert_cmp "$before" "$backlog" "D: backlog.md unwritten under --dry-run"

# D2 — the GLOBAL --dry-run flag (before the verb) reaches the seam identically.
setup_backlog
before="$(snapshot)"
out="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run backlog retire BDS-70)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out")" "D2: global --dry-run honored"
assert_cmp "$before" "$backlog" "D2: backlog.md unwritten under the global flag"

# ---------------------------------------------------------------------------
# Case E — the arg surface (mirrors gitignore's/closeout's skeleton pins).
# ---------------------------------------------------------------------------
setup_backlog

set +e
out="$(run 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: missing subcommand exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "E: missing subcommand kind usage"

set +e
out="$(run bogus BDS-70 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unknown subcommand exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "E: unknown subcommand kind usage"

set +e
out="$(run retire 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: missing tracker-id exits 2"
assert_eq "usage" "$(jq -r '.error.kind' <<<"$out")" "E: missing tracker-id kind usage"

set +e
out="$(run retire BDS-70 --bogus 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unknown flag exits 2"

set +e
out="$(run retire BDS-70 extra 2>/dev/null)"
rc=$?
set -e
assert_eq "2" "$rc" "E: unexpected positional arg exits 2"

# The usage failures are refusals, not partial writes.
assert_grep "issue/BDS-70" "$backlog" "E: no usage failure touched the backlog"

# ---------------------------------------------------------------------------
# Case F — no manifest: exit 4 / `no-manifest` (closeout + gitignore parity).
# ---------------------------------------------------------------------------
bare="$(wip_mktemp)"
set +e
out="$(WIP_ROOT="$bare" bin/wip-plumbing backlog retire BDS-70 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "F: no .wip.yaml exits 4"
assert_eq "false" "$(jq -r '.ok' <<<"$out")" "F: ok false"
assert_eq "no-manifest" "$(jq -r '.error.kind' <<<"$out")" "F: kind no-manifest"

# ---------------------------------------------------------------------------
# Case G — the verb is DISCOVERABLE: `wip_usage()` lists it. A verb the
#   dispatcher reaches but the help text never mentions is one no operator finds.
# ---------------------------------------------------------------------------
help_out="$(wip_mktemp)/help.txt"
bin/wip-plumbing --help >"$help_out"
assert_grep '^  backlog ' "$help_out" "G: --help lists the backlog command"
assert_grep 'usage: backlog retire <tracker-id> \[--dry-run\]' "$help_out" \
  "G: --help documents the retire usage line"

test_summary
