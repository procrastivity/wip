#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="workplan-init"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1
export WIP_NOW=2026-06-13

mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
cat >"$tmp/.wip/initiatives/demo/roadmap.md" <<'MD'
# Roadmap

## Round 1 — Build

- **step-01 — Auth bootstrap** ✅ shipped 2026-05-01 — done.
- **step-02 — Refresh tokens** — current.
- **step-02.5 — MFA prompt** — slot.
- **step-03 — Use * wildcard** — special-title regression.
MD

run() { WIP_ROOT="$tmp" bin/wip-plumbing workplan init "$@"; }

# 1. Happy path: writes file under workplans/.
out="$(run demo step-02)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "ok"
assert_eq "step-02" "$(jq -r '.step' <<<"$out")" "step echo"
assert_file "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md" "workplan written"
assert_grep "step-02 · Refresh tokens" "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md" "workplan h1 rendered"

# Step-01 closeout-write-ladder pin: workplan init resolves a `*`-titled step
# instead of failing `step-not-in-roadmap` after roadmap parse preserves it.
out_star="$(run demo step-03)"
assert_eq "true" "$(jq -r '.ok' <<<"$out_star")" "special-title workplan ok"
assert_eq "step-03" "$(jq -r '.step' <<<"$out_star")" "special-title step echo"
assert_file "$tmp/.wip/initiatives/demo/workplans/step-03-use-wildcard.md" "special-title workplan written"
assert_grep "step-03 · Use \\* wildcard" "$tmp/.wip/initiatives/demo/workplans/step-03-use-wildcard.md" "special-title h1 rendered"

# 2. --slug override.
out2="$(run demo step-02.5 --slug mfa)"
assert_eq "true" "$(jq -r '.ok' <<<"$out2")" "slug override ok"
assert_file "$tmp/.wip/initiatives/demo/workplans/step-02.5-mfa.md" "override path"

# 3. Existing file -> exit 4, --force -> overwrites.
set +e
run demo step-02 >/dev/null 2>&1
rc=$?
set -e
assert_eq "4" "$rc" "existing file exit 4"

# Touch a marker before --force to verify overwrite happens.
echo "OLD" >"$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md"
out3="$(run demo step-02 --force)"
assert_eq "true" "$(jq -r '.ok' <<<"$out3")" "force ok"
assert_grep "step-02 · Refresh tokens" "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md" "--force overwrote"

# 4. Unknown step -> exit 4.
set +e
out4="$(run demo step-99 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "unknown step exit 4"
assert_eq "step-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out4")" "unknown step kind"

# 5. Seed file appended.
cat >"$tmp/seed.md" <<'MD'
---
target: demo/step-01
---
# Seed for step-01

Some narrative seed content for the workplan.
MD
out5="$(run demo step-01 --from "$tmp/seed.md")"
assert_eq "true" "$(jq -r '.ok' <<<"$out5")" "seed ok"
assert_grep "## Seed (from intake)" "$tmp/.wip/initiatives/demo/workplans/step-01-auth-bootstrap.md" "seed section present"
assert_grep "Some narrative seed content" "$tmp/.wip/initiatives/demo/workplans/step-01-auth-bootstrap.md" "seed body present"

# 6. Seed shape failure -> exit 4.
cat >"$tmp/bad-seed.md" <<'MD'
# Seed without target

Body.
MD
set +e
out6="$(run demo step-01 --from "$tmp/bad-seed.md" --force 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "bad seed exit 4"
assert_eq "false" "$(jq -r '.valid' <<<"$out6")" "bad seed valid=false"

# 7. dry-run no writes.
out7="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run workplan init demo step-02.5 --slug new-thing)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out7")" "dry-run flag"
assert_absent "$tmp/.wip/initiatives/demo/workplans/step-02.5-new-thing.md" "dry-run no file"

# 8. Unknown initiative -> exit 3.
set +e
run bogus step-02 >/dev/null 2>&1
rc=$?
set -e
assert_eq "3" "$rc" "unknown initiative exit 3"

# 9. Missing step-id -> exit 2.
set +e
run demo >/dev/null 2>&1
rc=$?
set -e
assert_eq "2" "$rc" "missing step-id exit 2"

# 10. --activate (fresh workplan): writes the file AND sets active_step.
out10="$(run demo step-02.5 --slug fresh-activate --activate)"
assert_eq "true" "$(jq -r '.ok' <<<"$out10")" "activate ok"
assert_eq "step-02.5" "$(jq -r '.active_step' <<<"$out10")" "activate ledger active_step"
assert_file "$tmp/.wip/initiatives/demo/workplans/step-02.5-fresh-activate.md" "activate wrote workplan"
assert_eq "step-02.5" \
  "$(WIP_ROOT="$tmp" bin/wip-plumbing status 2>/dev/null | jq -r '.active_step.id')" \
  "status reflects active_step"

# 11. --activate on an existing workplan: no exit 4, skips the write, still activates.
before="$(cat "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md")"
out11="$(run demo step-02 --activate)"
assert_eq "true" "$(jq -r '.ok' <<<"$out11")" "activate existing ok (no exit 4)"
assert_eq "step-02" "$(jq -r '.active_step' <<<"$out11")" "activate existing active_step"
assert_eq "[]" "$(jq -c '.wrote' <<<"$out11")" "activate existing wrote empty"
assert_eq ".wip/initiatives/demo/workplans/step-02-refresh-tokens.md" \
  "$(jq -r '.skipped[0]' <<<"$out11")" "activate existing lists skipped"
after="$(cat "$tmp/.wip/initiatives/demo/workplans/step-02-refresh-tokens.md")"
assert_eq "$before" "$after" "existing workplan untouched"
assert_eq "step-02" \
  "$(WIP_ROOT="$tmp" bin/wip-plumbing status 2>/dev/null | jq -r '.active_step.id')" \
  "status now step-02"

# 12. --dry-run --activate touches nothing (no file, manifest unchanged).
man_before="$(cat "$tmp/.wip.yaml")"
out12="$(WIP_ROOT="$tmp" bin/wip-plumbing --dry-run workplan init demo step-01 --slug dryactivate --activate)"
assert_eq "true" "$(jq -r '.dry_run' <<<"$out12")" "dry-run activate flag"
assert_eq "step-01" "$(jq -r '.active_step' <<<"$out12")" "dry-run activate reports active_step"
assert_absent "$tmp/.wip/initiatives/demo/workplans/step-01-dryactivate.md" "dry-run activate no file"
assert_eq "$man_before" "$(cat "$tmp/.wip.yaml")" "dry-run activate manifest unchanged"

# 13. Non-roadmap step still exits 4, even with --activate.
set +e
out13="$(run demo step-99 --activate 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "activate non-roadmap exit 4"
assert_eq "step-not-in-roadmap" "$(jq -r '.error.kind' <<<"$out13")" "activate non-roadmap kind"

test_summary
