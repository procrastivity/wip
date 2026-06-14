#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="wip-proxy"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
# Pin the plumbing binary so the porcelain doesn't reach for $PATH.
export WIP_PLUMBING_BIN="$PWD/bin/wip-plumbing"

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
current_initiative: demo
initiatives:
  - slug: demo
    title: Demo
    status: in-flight
    active_step: step-02
    brief: .wip/initiatives/demo/BRIEF.md
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap — demo

## Round 1 — One

- **step-01 — First** ✅ shipped 2026-05-01 — done.
- **step-02 — Second** — current.
- **step-03 — Third** — later.
MD

# --- detect: porcelain output == plumbing output ----------------------------
porcelain="$(WIP_ROOT="$tmp" bin/wip detect)"
plumbing="$(WIP_ROOT="$tmp" bin/wip-plumbing detect)"
assert_eq "$plumbing" "$porcelain" "detect: byte-identical"

# --- status, next, doctor -----------------------------------------------------
for verb in status next doctor; do
  porcelain="$(WIP_ROOT="$tmp" bin/wip "$verb" 2>/dev/null || true)"
  plumbing="$(WIP_ROOT="$tmp" bin/wip-plumbing "$verb" 2>/dev/null || true)"
  assert_eq "$plumbing" "$porcelain" "$verb: byte-identical"
done

# --- unknown verb bubbles plumbing's exit-2 envelope ------------------------
set +e
porc_out="$(WIP_ROOT="$tmp" bin/wip totally-not-a-verb 2>/dev/null)"
porc_rc=$?
plum_out="$(WIP_ROOT="$tmp" bin/wip-plumbing totally-not-a-verb 2>/dev/null)"
plum_rc=$?
set -e
assert_eq "$plum_rc" "$porc_rc" "unknown verb: exit code matches"
assert_eq "$plum_out" "$porc_out" "unknown verb: envelope matches"

# --- --project resolution: porcelain passes --project verbatim to plumbing.
# Use an absolute path as the project id (no registry needed).
set +e
porc_out="$(bin/wip --project "$tmp" status 2>/dev/null)"
porc_rc=$?
plum_out="$(bin/wip-plumbing --project "$tmp" status 2>/dev/null)"
plum_rc=$?
set -e
assert_eq "$plum_rc" "$porc_rc" "--project: exit code matches"
assert_eq "$plum_out" "$porc_out" "--project: output matches"

# --- WIP_PLUMBING_BIN override is honored ------------------------------------
# Point at a fake binary that just echoes its argv; assert wip exec's it.
fake="$tmp/fake-plumbing"
cat >"$fake" <<'EOF'
#!/usr/bin/env bash
printf 'fake: %s\n' "$*"
exit 7
EOF
chmod +x "$fake"
set +e
out="$(WIP_PLUMBING_BIN="$fake" bin/wip arbitrary --foo bar 2>/dev/null)"
rc=$?
set -e
assert_eq "7" "$rc" "WIP_PLUMBING_BIN: exit propagated"
assert_eq "fake: arbitrary --foo bar" "$out" "WIP_PLUMBING_BIN: argv forwarded"

# --- missing plumbing -> exit 3 ---------------------------------------------
# Copy bin/wip to an isolated location so the sibling-lookup fails, then
# point WIP_PLUMBING_BIN at a non-existent file. PATH stays intact so the
# shebang still finds bash, but we sanitize PATH-resolved wip-plumbing by
# prepending an empty directory and a fake bin dir.
iso="$tmp/iso"
mkdir -p "$iso/bin" "$iso/empty"
cp bin/wip "$iso/bin/wip"
# Symlink the lib so $WIP_LIB resolution still works (../lib/wip relative to bin/).
mkdir -p "$iso/lib"
ln -s "$PWD/lib/wip" "$iso/lib/wip"
set +e
out="$(WIP_PLUMBING_BIN="/no/such/file" PATH="$iso/empty:/usr/bin:/bin" \
  "$iso/bin/wip" status 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "no plumbing -> exit 3"
assert_eq "no-plumbing" "$(jq -r '.error.kind' <<<"$out")" "no plumbing -> kind=no-plumbing"

test_summary
