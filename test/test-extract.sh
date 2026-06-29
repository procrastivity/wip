#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="extract"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# build_lds_enabled_root <dir>
#   Minimal LDS-enabled tempdir consumer with engineering/.lds-manifest.yaml
#   left empty; per-test cases overwrite it with the manifest they want
#   extract to consume.
build_lds_enabled_root() {
  local dir="$1"
  mkdir -p "$dir/engineering/decisions" "$dir/engineering/specs" "$dir/legacy"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
YAML
  : >"$dir/engineering/.lds-manifest.yaml"
}

# write_manifest <dir> <yaml-body>
write_manifest() {
  printf '%s' "$2" >"$1/engineering/.lds-manifest.yaml"
}

# range_sha256 <file> <start> <end> — compute the expected source.hash with
# the SAME recipe the impl uses (OQ1, locked): the extracted range body bytes
# exactly as wip_extract_source_body emits them — awk range, trailing newline
# included — through sha256sum. Keeps test and impl self-consistent without a
# magic digest.
range_sha256() {
  awk -v s="$2" -v e="$3" 'NR >= s && NR <= e' "$1" | sha256sum | awk '{print $1}'
}

# --- 1. Happy path: verbatim + content modes both write. ----------------------
d1="$tmp/c1"
build_lds_enabled_root "$d1"
cat >"$d1/legacy/source.md" <<'EOF'
line 1
line 2
line 3
line 4
line 5
EOF
write_manifest "$d1" '
metadata:
  schema_version: "1.0.0"
  status: approved
  eng_docs_dir: engineering
entries:
  - id: adr-verbatim
    source:
      file: legacy/source.md
      start_line: 2
      end_line: 4
    target: decisions/0001-verbatim.md
    mode: verbatim
    confidence: high
    classification_reason: testing
  - id: spec-inline
    target: specs/inline.md
    mode: content
    confidence: high
    classification_reason: testing
    inline_content: |
      # Inline spec body
      one
      two
'
out="$(WIP_ROOT="$d1" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O1 < <(jq -r '.ok, .entries_total, (.wrote | length), .hash_verification' <<<"$out")
assert_eq "true" "${O1[0]}" "[happy] ok:true"
assert_eq "2" "${O1[1]}" "[happy] total=2"
assert_eq "2" "${O1[2]}" "[happy] wrote=2"
assert_eq "skipped-v1" "${O1[3]}" "[happy] hash skipped"
assert_file "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim target"
assert_file "$d1/engineering/specs/inline.md" "[happy] content target"
assert_grep '<!-- Migrated from legacy/source.md:2-4 -->' \
  "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim attribution"
assert_grep '^line 2' "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim body start"
assert_grep '^line 4' "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim body end"
assert_not_grep '^line 5' "$d1/engineering/decisions/0001-verbatim.md" "[happy] line range respected"
assert_grep '<!-- Generated content - no source file -->' \
  "$d1/engineering/specs/inline.md" "[happy] content attribution"
assert_grep '^# Inline spec body' "$d1/engineering/specs/inline.md" "[happy] content body"
# Extraction report (LDS §7): on-disk YAML reconciles with the stdout ledger.
assert_file "$d1/engineering/extraction-report.yaml" "[report] yaml written"
assert_file "$d1/engineering/extraction-report.md" "[report] md written"
rj1="$(yq -o=json '.' "$d1/engineering/extraction-report.yaml")"
mapfile -t R1 < <(jq -r '
  .extraction_report.summary.total_entries,
  .extraction_report.summary.successful,
  .extraction_report.summary.failed,
  .extraction_report.summary.skipped,
  (.extraction_report.files_created[] | select(.target == "engineering/decisions/0001-verbatim.md") | .status),
  (.extraction_report.files_created[] | select(.target == "engineering/specs/inline.md") | .status),
  .extraction_report.verification_results.content_hash_check.status,
  .extraction_report.verification_results.line_count_check.status,
  .extraction_report.line_statistics.source_lines_processed,
  .extraction_report.layer_breakdown.decisions.total_lines,
  .extraction_report.metadata.executed_at,
  .extraction_report.metadata.manifest_hash
' <<<"$rj1")
assert_eq "2" "${R1[0]}" "[report] total=2"
assert_eq "2" "${R1[1]}" "[report] successful=2"
assert_eq "0" "${R1[2]}" "[report] failed=0"
assert_eq "0" "${R1[3]}" "[report] skipped=0"
# count reconciliation: stdout .wrote length == report successful.
assert_eq "${O1[2]}" "${R1[1]}" "[report] wrote count reconciles"
# per-target reconciliation: each stdout .wrote[] target is a success row.
assert_eq "success" "${R1[4]}" "[report] verbatim target success row"
assert_eq "success" "${R1[5]}" "[report] content target success row"
assert_eq "skipped-v1" "${R1[6]}" "[report] content hash check skipped-v1"
assert_eq "skipped-v1" "${R1[7]}" "[report] line count check skipped-v1"
# v1 null fields (keys present, values null) — assert null, never a value.
assert_eq "null" "${R1[8]}" "[report] line_statistics null"
assert_eq "null" "${R1[9]}" "[report] layer total_lines null"
# md carries the §7.4 Status line.
assert_grep '^Status: COMPLETED$' "$d1/engineering/extraction-report.md" "[report] md status COMPLETED"
# Non-determinism guards: never assert exact executed_at; treat hash as presence/null.
ea="${R1[10]}"
case "$ea" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z) ea_shape=ok ;;
  *) ea_shape=bad ;;
esac
assert_eq "ok" "$ea_shape" "[report] executed_at is ISO-8601 shaped"
mh="${R1[11]}"
[[ -n "$mh" ]] && mh_ok=ok || mh_ok=bad
assert_eq "ok" "$mh_ok" "[report] manifest_hash present (hash or null)"

# --- 2. Unsupported modes get skipped (not failed). ---------------------------
# step-19 REQUIRED MIGRATION: skip-transform was `type: heading_adjust` (the
# step-18 fixture asserting the transform SKIP path). heading_adjust is now a
# SUPPORTED transform (step-19), so this fixture is repointed to
# `type: markdown_format` — a still-unsupported transform type — to keep
# exercising the skip path. Not a regression: the supported heading_adjust
# behavior is covered by the new transform tests below.
d2="$tmp/c2"
build_lds_enabled_root "$d2"
echo "src" >"$d2/legacy/x.md"
write_manifest "$d2" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: ok
    source: legacy/x.md
    target: decisions/0001-ok.md
    mode: verbatim
    confidence: high
    classification_reason: testing
  - id: skip-transform
    source: legacy/x.md
    target: decisions/0002-tr.md
    mode: transform
    confidence: high
    classification_reason: testing
    transform_config:
      type: markdown_format
  - id: skip-summarize
    source: legacy/x.md
    target: decisions/0003-sum.md
    mode: summarize
    confidence: high
    classification_reason: testing
'
out="$(WIP_ROOT="$d2" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O2 < <(jq -r '
  .ok,
  (.wrote | length),
  (.unsupported | length),
  (.unsupported[] | select(.id == "skip-transform") | .mode),
  (.unsupported[] | select(.id == "skip-summarize") | .mode)
' <<<"$out")
assert_eq "true" "${O2[0]}" "[unsupported] ok:true"
assert_eq "1" "${O2[1]}" "[unsupported] wrote=1"
assert_eq "2" "${O2[2]}" "[unsupported] count=2"
assert_eq "transform" "${O2[3]}" "[unsupported] transform mode"
assert_eq "summarize" "${O2[4]}" "[unsupported] summarize mode"
assert_file "$d2/engineering/decisions/0001-ok.md" "[unsupported] supported wrote"
assert_absent "$d2/engineering/decisions/0002-tr.md" "[unsupported] transform not written"
# Report: unsupported entries land in their own block (not files_created/errors).
rj2="$(yq -o=json '.' "$d2/engineering/extraction-report.yaml")"
mapfile -t R2 < <(jq -r '
  .extraction_report.summary.successful,
  .extraction_report.summary.unsupported,
  (.extraction_report.unsupported[] | select(.id == "skip-transform") | .reason),
  (.extraction_report.unsupported[] | select(.id == "skip-summarize") | .reason),
  ([.extraction_report.files_created[] | select(.id == "skip-transform")] | length)
' <<<"$rj2")
assert_eq "1" "${R2[0]}" "[report-unsup] successful=1"
assert_eq "2" "${R2[1]}" "[report-unsup] unsupported=2"
assert_eq "markdown_format transform not supported in v1" "${R2[2]}" \
  "[report-unsup] markdown_format transform reason"
assert_eq "summarize mode not supported in v1" "${R2[3]}" \
  "[report-unsup] summarize reason"
assert_eq "0" "${R2[4]}" "[report-unsup] not in files_created"

# --- 3. Multi-file source → unsupported-source. -------------------------------
d3="$tmp/c3"
build_lds_enabled_root "$d3"
echo "a" >"$d3/legacy/a.md"
echo "b" >"$d3/legacy/b.md"
write_manifest "$d3" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: multi
    source:
      files:
        - file: legacy/a.md
        - file: legacy/b.md
      separator: "\n---\n"
    target: specs/multi.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
out="$(WIP_ROOT="$d3" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O3 < <(jq -r '(.unsupported | length), .unsupported[0].source_kind' <<<"$out")
assert_eq "1" "${O3[0]}" "[multi-file] unsupported=1"
assert_eq "multi-file" "${O3[1]}" "[multi-file] kind"
assert_absent "$d3/engineering/specs/multi.md" "[multi-file] not written"
# Report: the multi-file entry is unsupported, not a created file.
rj3="$(yq -o=json '.' "$d3/engineering/extraction-report.yaml")"
mapfile -t R3 < <(jq -r '
  .extraction_report.summary.unsupported,
  .extraction_report.unsupported[0].source_kind,
  ([.extraction_report.files_created[] | select(.target == "engineering/specs/multi.md")] | length)
' <<<"$rj3")
assert_eq "1" "${R3[0]}" "[report-multi] unsupported=1"
assert_eq "multi-file" "${R3[1]}" "[report-multi] source_kind"
assert_eq "0" "${R3[2]}" "[report-multi] not in files_created"

# --- 4. Manifest not approved → exit 4 manifest-not-approved. -----------------
d4="$tmp/c4"
build_lds_enabled_root "$d4"
write_manifest "$d4" '
metadata:
  schema_version: "1.0.0"
  status: pending
entries:
  - id: x
    source: legacy/x.md
    target: decisions/0001-x.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
set +e
out="$(WIP_ROOT="$d4" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[not-approved] exit 4"
assert_eq "manifest-not-approved" "$(jq -r '.error.kind' <<<"$out")" "[not-approved] kind"

# --- 5. Incompatible schema_version → exit 4 incompatible-schema. -------------
d5="$tmp/c5"
build_lds_enabled_root "$d5"
write_manifest "$d5" '
metadata:
  schema_version: "2.0.0"
  status: approved
entries:
  - id: x
    source: legacy/x.md
    target: decisions/0001-x.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
set +e
out="$(WIP_ROOT="$d5" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[bad-schema] exit 4"
assert_eq "incompatible-schema" "$(jq -r '.error.kind' <<<"$out")" "[bad-schema] kind"

# --- 6. Empty entries → exit 4 manifest-empty. --------------------------------
d6="$tmp/c6"
build_lds_enabled_root "$d6"
write_manifest "$d6" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries: []
'
set +e
out="$(WIP_ROOT="$d6" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[empty] exit 4"
assert_eq "manifest-empty" "$(jq -r '.error.kind' <<<"$out")" "[empty] kind"

# --- 7. Idempotent: 2nd run all skipped. --------------------------------------
d7="$tmp/c7"
build_lds_enabled_root "$d7"
echo "hello" >"$d7/legacy/h.md"
write_manifest "$d7" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/h.md
    target: decisions/0001-h.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
WIP_ROOT="$d7" bin/wip-plumbing extract >/dev/null 2>&1
out="$(WIP_ROOT="$d7" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O7 < <(jq -r '.ok, (.wrote | length), (.skipped_idempotent | length)' <<<"$out")
assert_eq "true" "${O7[0]}" "[idem] ok"
assert_eq "0" "${O7[1]}" "[idem] wrote=0"
assert_eq "1" "${O7[2]}" "[idem] skipped=1"
# Report bypasses idempotency: 2nd run regenerates it (no content-drift refusal
# on the report file itself), reflecting the all-skipped ledger.
assert_file "$d7/engineering/extraction-report.yaml" "[report-idem] report regenerated on re-run"
rj7="$(yq -o=json '.' "$d7/engineering/extraction-report.yaml")"
mapfile -t R7 < <(jq -r '.extraction_report.summary.skipped, .extraction_report.summary.successful' <<<"$rj7")
assert_eq "1" "${R7[0]}" "[report-idem] skipped=1"
assert_eq "0" "${R7[1]}" "[report-idem] successful=0"

# --- 8. Content drift → exit 4; --force overwrites. ---------------------------
d8="$tmp/c8"
build_lds_enabled_root "$d8"
echo "orig" >"$d8/legacy/h.md"
write_manifest "$d8" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/h.md
    target: decisions/0001-h.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
WIP_ROOT="$d8" bin/wip-plumbing extract >/dev/null 2>&1
echo "tampered" >>"$d8/engineering/decisions/0001-h.md"
set +e
out="$(WIP_ROOT="$d8" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[drift] exit 4"
mapfile -t O8 < <(jq -r '.error.kind, (.error.paths | length)' <<<"$out")
assert_eq "content-drift" "${O8[0]}" "[drift] kind"
assert_eq "1" "${O8[1]}" "[drift] paths=1"
# §7.3: the report is written before exit 4; the drifted target shows failed.
assert_file "$d8/engineering/extraction-report.yaml" "[report-drift] written despite exit 4"
rj8="$(yq -o=json '.' "$d8/engineering/extraction-report.yaml")"
mapfile -t R8 < <(jq -r '
  .extraction_report.summary.failed,
  (.extraction_report.files_created[] | select(.target == "engineering/decisions/0001-h.md") | .status)
' <<<"$rj8")
assert_eq "1" "${R8[0]}" "[report-drift] failed=1"
assert_eq "failed" "${R8[1]}" "[report-drift] drifted target status failed"
out="$(WIP_ROOT="$d8" bin/wip-plumbing extract --force 2>/dev/null)"
mapfile -t O8F < <(jq -r '.ok, (.wrote_forced | length)' <<<"$out")
assert_eq "true" "${O8F[0]}" "[--force] ok"
assert_eq "1" "${O8F[1]}" "[--force] wrote_forced=1"

# --- 9. --manifest override. --------------------------------------------------
d9="$tmp/c9"
build_lds_enabled_root "$d9"
echo "ovr" >"$d9/legacy/o.md"
cat >"$d9/custom-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/o.md
    target: decisions/0001-o.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d9" bin/wip-plumbing extract --manifest custom-manifest.yaml 2>/dev/null)"
mapfile -t O9 < <(jq -r '.ok, .manifest' <<<"$out")
assert_eq "true" "${O9[0]}" "[--manifest] ok"
assert_eq "custom-manifest.yaml" "${O9[1]}" "[--manifest] path echoed"
assert_file "$d9/engineering/decisions/0001-o.md" "[--manifest] target written"

# --- 10. LDS disabled / sentinel missing — same shape as graduate. ------------
d10="$tmp/c10"
mkdir -p "$d10"
cat >"$d10/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: false
YAML
set +e
out="$(WIP_ROOT="$d10" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[lds-disabled] exit 3"
assert_eq "lds-not-enabled" "$(jq -r '.error.kind' <<<"$out")" "[lds-disabled] kind"

d10b="$tmp/c10b"
mkdir -p "$d10b/engineering"
cat >"$d10b/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
YAML
set +e
out="$(WIP_ROOT="$d10b" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[no-sentinel] exit 3"
assert_eq "lds-sentinel-missing" "$(jq -r '.error.kind' <<<"$out")" "[no-sentinel] kind"

# --- 11. Duplicate entry ids → exit 4 duplicate-entry-id. ---------------------
d11="$tmp/c11"
build_lds_enabled_root "$d11"
echo "x" >"$d11/legacy/x.md"
write_manifest "$d11" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: dup
    source: legacy/x.md
    target: decisions/0001-a.md
    mode: verbatim
    confidence: high
    classification_reason: t
  - id: dup
    source: legacy/x.md
    target: decisions/0002-b.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
set +e
out="$(WIP_ROOT="$d11" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[dup-id] exit 4"
assert_eq "duplicate-entry-id" "$(jq -r '.error.kind' <<<"$out")" "[dup-id] kind"

# --- 12. Bad-shape entry: verbatim with no source. ----------------------------
d12="$tmp/c12"
build_lds_enabled_root "$d12"
write_manifest "$d12" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: bad
    target: decisions/0001-bad.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
set +e
out="$(WIP_ROOT="$d12" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[bad-shape] exit 4"
assert_eq "bad-entry-shape" "$(jq -r '.error.kind' <<<"$out")" "[bad-shape] kind"
# §7.3: report written before exit 4; bad entry mirrored into errors[] + failed row.
assert_file "$d12/engineering/extraction-report.yaml" "[report-bad] written despite exit 4"
rj12="$(yq -o=json '.' "$d12/engineering/extraction-report.yaml")"
mapfile -t R12 < <(jq -r '
  .extraction_report.summary.failed,
  .extraction_report.errors[0].entry,
  (.extraction_report.files_created[] | select(.id == "bad") | .status)
' <<<"$rj12")
assert_eq "1" "${R12[0]}" "[report-bad] failed=1"
assert_eq "bad" "${R12[1]}" "[report-bad] bad entry in errors"
assert_eq "failed" "${R12[2]}" "[report-bad] bad entry status failed"

# --- 13. Template field bumps entry to unsupported (v1 deferral). -------------
d13="$tmp/c13"
build_lds_enabled_root "$d13"
echo "x" >"$d13/legacy/x.md"
write_manifest "$d13" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tpl
    source: legacy/x.md
    target: decisions/0001-tpl.md
    mode: verbatim
    confidence: high
    classification_reason: t
    template: adr-madr-minimal.md
    field_mappings:
      title: Hello
'
out="$(WIP_ROOT="$d13" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O13 < <(jq -r '.ok, (.unsupported | length), .unsupported[0].reason' <<<"$out")
assert_eq "true" "${O13[0]}" "[template] ok"
assert_eq "1" "${O13[1]}" "[template] unsupported=1"
assert_eq "template/field_mappings not supported in v1" "${O13[2]}" "[template] reason"
assert_absent "$d13/engineering/decisions/0001-tpl.md" "[template] not written"

# --- 14. --dry-run writes neither targets nor report. -------------------------
d14="$tmp/c14"
build_lds_enabled_root "$d14"
printf 'x\ny\nz\n' >"$d14/legacy/s.md"
write_manifest "$d14" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/s.md
    target: decisions/0001-x.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
out="$(WIP_ROOT="$d14" bin/wip-plumbing --dry-run extract 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[dry-run] ok"
assert_absent "$d14/engineering/extraction-report.yaml" "[dry-run] no report yaml"
assert_absent "$d14/engineering/extraction-report.md" "[dry-run] no report md"
assert_absent "$d14/engineering/decisions/0001-x.md" "[dry-run] no target written"

# --- 15. --verify-hashes match → pass. ----------------------------------------
d15="$tmp/c15"
build_lds_enabled_root "$d15"
printf 'line 1\nline 2\nline 3\nline 4\nline 5\n' >"$d15/legacy/source.md"
h15="$(range_sha256 "$d15/legacy/source.md" 2 4)"
cat >"$d15/engineering/.lds-manifest.yaml" <<EOF
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
      start_line: 2
      end_line: 4
      hash: "$h15"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d15" bin/wip-plumbing extract --verify-hashes 2>/dev/null)"
mapfile -t O15 < <(jq -r '.ok, .hash_verification, (.wrote | length)' <<<"$out")
assert_eq "true" "${O15[0]}" "[verify-match] ok:true"
assert_eq "verified" "${O15[1]}" "[verify-match] hash_verification verified"
assert_eq "1" "${O15[2]}" "[verify-match] wrote=1"
assert_file "$d15/engineering/decisions/0001-v.md" "[verify-match] target written"
rj15="$(yq -o=json '.' "$d15/engineering/extraction-report.yaml")"
mapfile -t R15 < <(jq -r '
  .extraction_report.verification_results.content_hash_check.status,
  .extraction_report.verification_results.content_hash_check.entries_checked,
  .extraction_report.verification_results.content_hash_check.entries_matched
' <<<"$rj15")
assert_eq "pass" "${R15[0]}" "[verify-match] report content hash pass"
assert_eq "1" "${R15[1]}" "[verify-match] entries_checked=1"
assert_eq "1" "${R15[2]}" "[verify-match] entries_matched=1"
assert_grep 'Content hash check:   pass' "$d15/engineering/extraction-report.md" \
  "[verify-match] md content hash line"

# --- 16. --verify-hashes mismatch → exit 4 pre-write gate. --------------------
d16="$tmp/c16"
build_lds_enabled_root "$d16"
printf 'line 1\nline 2\nline 3\nline 4\nline 5\n' >"$d16/legacy/source.md"
cat >"$d16/engineering/.lds-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
      start_line: 2
      end_line: 4
      hash: "0000000000000000000000000000000000000000000000000000000000000000"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
set +e
out="$(WIP_ROOT="$d16" bin/wip-plumbing extract --verify-hashes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[verify-mismatch] exit 4"
mapfile -t O16 < <(jq -r '.error.kind, .error.paths[0], .error.mismatches[0].status' <<<"$out")
assert_eq "hash-mismatch" "${O16[0]}" "[verify-mismatch] kind"
assert_eq "legacy/source.md" "${O16[1]}" "[verify-mismatch] source in error.paths"
assert_eq "mismatch" "${O16[2]}" "[verify-mismatch] mismatch row status"
assert_absent "$d16/engineering/decisions/0001-v.md" "[verify-mismatch] no target written (gate)"
assert_file "$d16/engineering/extraction-report.yaml" "[verify-mismatch] report written despite exit 4"
rj16="$(yq -o=json '.' "$d16/engineering/extraction-report.yaml")"
mapfile -t R16 < <(jq -r '
  .extraction_report.verification_results.content_hash_check.status,
  (.extraction_report.verification_results.content_hash_check.mismatches | length)
' <<<"$rj16")
assert_eq "fail" "${R16[0]}" "[verify-mismatch] report content hash fail"
assert_eq "1" "${R16[1]}" "[verify-mismatch] one report mismatch row"

# --- 17. --verify-hashes missing hashed source → missing mismatch. -----------
d17="$tmp/c17"
build_lds_enabled_root "$d17"
# legacy/gone.md is never created.
cat >"$d17/engineering/.lds-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/gone.md
      start_line: 2
      end_line: 4
      hash: "0000000000000000000000000000000000000000000000000000000000000000"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
set +e
out="$(WIP_ROOT="$d17" bin/wip-plumbing extract --verify-hashes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[verify-missing] exit 4"
mapfile -t O17 < <(jq -r '.error.kind, .error.mismatches[0].status' <<<"$out")
assert_eq "hash-mismatch" "${O17[0]}" "[verify-missing] kind"
assert_eq "missing" "${O17[1]}" "[verify-missing] status missing"
assert_absent "$d17/engineering/decisions/0001-v.md" "[verify-missing] no target written"
rj17="$(yq -o=json '.' "$d17/engineering/extraction-report.yaml")"
assert_eq "missing" \
  "$(jq -r '.extraction_report.verification_results.content_hash_check.mismatches[0].status' <<<"$rj17")" \
  "[verify-missing] report missing row"

# --- 18. --verify-hashes no declared hash → skipped, not failed. -------------
d18="$tmp/c18"
build_lds_enabled_root "$d18"
printf 'line 1\nline 2\nline 3\n' >"$d18/legacy/source.md"
cat >"$d18/engineering/.lds-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d18" bin/wip-plumbing extract --verify-hashes 2>/dev/null)"
mapfile -t O18 < <(jq -r '.ok, .hash_verification, (.wrote | length)' <<<"$out")
assert_eq "true" "${O18[0]}" "[verify-nohash] ok:true"
assert_eq "no-hashes" "${O18[1]}" "[verify-nohash] hash_verification no-hashes"
assert_eq "1" "${O18[2]}" "[verify-nohash] wrote=1"
assert_file "$d18/engineering/decisions/0001-v.md" "[verify-nohash] target still written"
rj18="$(yq -o=json '.' "$d18/engineering/extraction-report.yaml")"
mapfile -t R18 < <(jq -r '
  .extraction_report.verification_results.content_hash_check.entries_no_hash,
  (.extraction_report.warnings | length),
  .extraction_report.warnings[0].type
' <<<"$rj18")
assert_eq "1" "${R18[0]}" "[verify-nohash] entries_no_hash=1"
assert_eq "1" "${R18[1]}" "[verify-nohash] one warning"
assert_eq "no-verifiable-hashes" "${R18[2]}" "[verify-nohash] warning type"

# --- 19. --verify-hashes mixed manifest (one hashed, one not). ---------------
d19="$tmp/c19"
build_lds_enabled_root "$d19"
printf 'line 1\nline 2\nline 3\nline 4\nline 5\n' >"$d19/legacy/a.md"
printf 'alpha\nbeta\ngamma\n' >"$d19/legacy/b.md"
h19="$(range_sha256 "$d19/legacy/a.md" 2 4)"
cat >"$d19/engineering/.lds-manifest.yaml" <<EOF
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: hashed
    source:
      file: legacy/a.md
      start_line: 2
      end_line: 4
      hash: "$h19"
    target: decisions/0001-a.md
    mode: verbatim
    confidence: high
    classification_reason: t
  - id: nohash
    source:
      file: legacy/b.md
    target: decisions/0002-b.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d19" bin/wip-plumbing extract --verify-hashes 2>/dev/null)"
mapfile -t O19 < <(jq -r '.ok, .hash_verification, (.wrote | length)' <<<"$out")
assert_eq "true" "${O19[0]}" "[verify-mixed] ok:true"
assert_eq "verified" "${O19[1]}" "[verify-mixed] verified"
assert_eq "2" "${O19[2]}" "[verify-mixed] wrote=2"
assert_file "$d19/engineering/decisions/0001-a.md" "[verify-mixed] hashed target written"
assert_file "$d19/engineering/decisions/0002-b.md" "[verify-mixed] no-hash target written"
rj19="$(yq -o=json '.' "$d19/engineering/extraction-report.yaml")"
mapfile -t R19 < <(jq -r '
  .extraction_report.verification_results.content_hash_check.entries_checked,
  .extraction_report.verification_results.content_hash_check.entries_no_hash
' <<<"$rj19")
assert_eq "1" "${R19[0]}" "[verify-mixed] entries_checked=1"
assert_eq "1" "${R19[1]}" "[verify-mixed] entries_no_hash=1"

# --- 20. Flag off regression — a present hash is ignored; skipped-v1. --------
d20="$tmp/c20"
build_lds_enabled_root "$d20"
printf 'line 1\nline 2\nline 3\n' >"$d20/legacy/source.md"
h20="$(range_sha256 "$d20/legacy/source.md" 1 2)"
cat >"$d20/engineering/.lds-manifest.yaml" <<EOF
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
      start_line: 1
      end_line: 2
      hash: "$h20"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d20" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O20 < <(jq -r '.ok, .hash_verification' <<<"$out")
assert_eq "true" "${O20[0]}" "[verify-off] ok:true"
assert_eq "skipped-v1" "${O20[1]}" "[verify-off] hash_verification skipped-v1"
rj20="$(yq -o=json '.' "$d20/engineering/extraction-report.yaml")"
mapfile -t R20 < <(jq -r '
  .extraction_report.verification_results.content_hash_check.status,
  (.extraction_report.warnings | length)
' <<<"$rj20")
assert_eq "skipped-v1" "${R20[0]}" "[verify-off] content_hash_check skipped-v1"
assert_eq "0" "${R20[1]}" "[verify-off] no warnings"
assert_grep 'Content hash check:   skipped-v1' "$d20/engineering/extraction-report.md" \
  "[verify-off] md line skipped-v1"

# --- 21. --dry-run + --verify-hashes: check runs, writes nothing. ------------
d21="$tmp/c21"
build_lds_enabled_root "$d21"
printf 'line 1\nline 2\nline 3\nline 4\n' >"$d21/legacy/source.md"
h21="$(range_sha256 "$d21/legacy/source.md" 1 3)"
cat >"$d21/engineering/.lds-manifest.yaml" <<EOF
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
      start_line: 1
      end_line: 3
      hash: "$h21"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d21" bin/wip-plumbing --dry-run extract --verify-hashes 2>/dev/null)"
mapfile -t O21 < <(jq -r '.ok, .hash_verification' <<<"$out")
assert_eq "true" "${O21[0]}" "[verify-dry-match] ok:true"
assert_eq "verified" "${O21[1]}" "[verify-dry-match] verified in stdout"
assert_absent "$d21/engineering/decisions/0001-v.md" "[verify-dry-match] no target"
assert_absent "$d21/engineering/extraction-report.yaml" "[verify-dry-match] no report"
# Same fixture, now a wrong hash: dry-run still surfaces the mismatch as exit 4,
# but writes neither target nor report.
cat >"$d21/engineering/.lds-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: adr-v
    source:
      file: legacy/source.md
      start_line: 1
      end_line: 3
      hash: "0000000000000000000000000000000000000000000000000000000000000000"
    target: decisions/0001-v.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
set +e
out="$(WIP_ROOT="$d21" bin/wip-plumbing --dry-run extract --verify-hashes 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[verify-dry-mismatch] exit 4"
assert_eq "hash-mismatch" "$(jq -r '.error.kind' <<<"$out")" "[verify-dry-mismatch] kind"
assert_absent "$d21/engineering/decisions/0001-v.md" "[verify-dry-mismatch] no target"
assert_absent "$d21/engineering/extraction-report.yaml" "[verify-dry-mismatch] no report"

# --- 22. transform/heading_adjust e2e: +1 shift, skip_first, fence + indent
#         untouched, verbatim-style attribution, §7 report reconciliation. ----
d22="$tmp/c22"
build_lds_enabled_root "$d22"
cat >"$d22/legacy/doc.md" <<'EOF'
# Title
intro text
## Section
```
# fenced not a heading
```
    # indented not a heading
### Sub
EOF
write_manifest "$d22" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-shift
    source: legacy/doc.md
    target: specs/shifted.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: heading_adjust
      options:
        level_offset: 1
        skip_first: true
'
out="$(WIP_ROOT="$d22" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O22 < <(jq -r '.ok, (.wrote | length), (.unsupported | length)' <<<"$out")
assert_eq "true" "${O22[0]}" "[tr-shift] ok:true"
assert_eq "1" "${O22[1]}" "[tr-shift] wrote=1"
assert_eq "0" "${O22[2]}" "[tr-shift] none unsupported"
tgt22="$d22/engineering/specs/shifted.md"
assert_file "$tgt22" "[tr-shift] target written"
assert_grep '<!-- Migrated from legacy/doc.md -->' "$tgt22" "[tr-shift] verbatim-style attribution"
assert_grep '<!-- Extraction ID: tr-shift -->' "$tgt22" "[tr-shift] extraction id"
assert_grep '^# Title$' "$tgt22" "[tr-shift] skip_first leaves first heading"
assert_grep '^### Section$' "$tgt22" "[tr-shift] ## -> ### (+1)"
assert_grep '^#### Sub$' "$tgt22" "[tr-shift] ### -> #### (+1)"
assert_grep '^# fenced not a heading$' "$tgt22" "[tr-shift] fenced # untouched"
assert_grep '^    # indented not a heading$' "$tgt22" "[tr-shift] indented # untouched"
# §7 report reconciliation: transform success counts in summary.successful +
# files_created (not unsupported), and reconciles with the stdout ledger.
rj22="$(yq -o=json '.' "$d22/engineering/extraction-report.yaml")"
mapfile -t R22 < <(jq -r '
  .extraction_report.summary.successful,
  .extraction_report.summary.unsupported,
  (.extraction_report.files_created[] | select(.target == "engineering/specs/shifted.md") | .status)
' <<<"$rj22")
assert_eq "1" "${R22[0]}" "[tr-shift] report successful=1"
assert_eq "0" "${R22[1]}" "[tr-shift] report unsupported=0"
assert_eq "success" "${R22[2]}" "[tr-shift] report success row"
assert_eq "${O22[1]}" "${R22[0]}" "[tr-shift] wrote count reconciles"

# --- 23. transform/heading_adjust -1 shift + clamp floor (# stays #). --------
d23="$tmp/c23"
build_lds_enabled_root "$d23"
printf '# Stays at one\n## Down to one\n### Down to two\n' >"$d23/legacy/doc.md"
write_manifest "$d23" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-down
    source: legacy/doc.md
    target: specs/down.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: heading_adjust
      options:
        level_offset: -1
'
out="$(WIP_ROOT="$d23" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "1" "$(jq -r '.wrote | length' <<<"$out")" "[tr-down] wrote=1"
tgt23="$d23/engineering/specs/down.md"
assert_grep '^# Stays at one$' "$tgt23" "[tr-down] # -1 clamps to #"
assert_grep '^# Down to one$' "$tgt23" "[tr-down] ## -> #"
assert_grep '^## Down to two$' "$tgt23" "[tr-down] ### -> ##"

# --- 24. transform/heading_adjust clamp ceiling (###### +1 stays ######). ----
d24="$tmp/c24"
build_lds_enabled_root "$d24"
printf '##### Five\n###### Six\n' >"$d24/legacy/doc.md"
write_manifest "$d24" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-up
    source: legacy/doc.md
    target: specs/up.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: heading_adjust
      options:
        level_offset: 1
'
out="$(WIP_ROOT="$d24" bin/wip-plumbing extract 2>/dev/null)"
tgt24="$d24/engineering/specs/up.md"
assert_grep '^###### Five$' "$tgt24" "[tr-up] ##### -> ######"
assert_grep '^###### Six$' "$tgt24" "[tr-up] ###### +1 clamps to ######"
assert_not_grep '#######' "$tgt24" "[tr-up] never emits 7 hashes"

# --- 25. transform/heading_adjust idempotent re-run (three-way). -------------
d25="$tmp/c25"
build_lds_enabled_root "$d25"
printf '# A\n## B\n' >"$d25/legacy/doc.md"
write_manifest "$d25" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-idem
    source: legacy/doc.md
    target: specs/idem.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: heading_adjust
      options:
        level_offset: 1
'
WIP_ROOT="$d25" bin/wip-plumbing extract >/dev/null 2>&1
out="$(WIP_ROOT="$d25" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O25 < <(jq -r '.ok, (.wrote | length), (.skipped_idempotent | length)' <<<"$out")
assert_eq "true" "${O25[0]}" "[tr-idem] ok"
assert_eq "0" "${O25[1]}" "[tr-idem] wrote=0 on re-run"
assert_eq "1" "${O25[2]}" "[tr-idem] skipped_idempotent=1"

# --- 26. transform link_rewrite / custom still unsupported (skip, not fail). -
d26="$tmp/c26"
build_lds_enabled_root "$d26"
echo "x" >"$d26/legacy/x.md"
write_manifest "$d26" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-link
    source: legacy/x.md
    target: specs/link.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: link_rewrite
      options:
        base_path: "../"
  - id: tr-custom
    source: legacy/x.md
    target: specs/custom.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: custom
'
out="$(WIP_ROOT="$d26" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O26 < <(jq -r '
  .ok,
  (.unsupported | length),
  (.unsupported[] | select(.id == "tr-link") | .reason),
  (.unsupported[] | select(.id == "tr-custom") | .reason)
' <<<"$out")
assert_eq "true" "${O26[0]}" "[tr-unsup] ok:true (skip not fail)"
assert_eq "2" "${O26[1]}" "[tr-unsup] count=2"
assert_eq "link_rewrite transform not supported in v1" "${O26[2]}" "[tr-unsup] link_rewrite reason"
assert_eq "custom transform not supported in v1" "${O26[3]}" "[tr-unsup] custom reason"
assert_absent "$d26/engineering/specs/link.md" "[tr-unsup] link_rewrite not written"
assert_absent "$d26/engineering/specs/custom.md" "[tr-unsup] custom not written"

# --- 27. transform on multi-file source → unsupported-source:multi-file. -----
#     (multi-file takes precedence over the transform type, per D6.)
d27="$tmp/c27"
build_lds_enabled_root "$d27"
echo "a" >"$d27/legacy/a.md"
echo "b" >"$d27/legacy/b.md"
write_manifest "$d27" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-multi
    source:
      files:
        - file: legacy/a.md
        - file: legacy/b.md
    target: specs/multi.md
    mode: transform
    confidence: high
    classification_reason: t
    transform_config:
      type: heading_adjust
      options:
        level_offset: 1
'
out="$(WIP_ROOT="$d27" bin/wip-plumbing extract 2>/dev/null)"
mapfile -t O27 < <(jq -r '
  .ok,
  (.unsupported | length),
  (.unsupported[] | select(.id == "tr-multi") | .source_kind)
' <<<"$out")
assert_eq "true" "${O27[0]}" "[tr-multi] ok:true"
assert_eq "1" "${O27[1]}" "[tr-multi] unsupported=1"
assert_eq "multi-file" "${O27[2]}" "[tr-multi] multi-file source_kind (not transform_type)"
assert_absent "$d27/engineering/specs/multi.md" "[tr-multi] not written"

# --- 28. transform with absent transform_config → bad-entry-shape. ----------
d28="$tmp/c28"
build_lds_enabled_root "$d28"
echo "x" >"$d28/legacy/x.md"
write_manifest "$d28" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tr-noconfig
    source: legacy/x.md
    target: specs/noconfig.md
    mode: transform
    confidence: high
    classification_reason: t
'
set +e
out="$(WIP_ROOT="$d28" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[tr-badshape] exit 4"
assert_eq "bad-entry-shape" "$(jq -r '.error.kind' <<<"$out")" "[tr-badshape] kind"
assert_absent "$d28/engineering/specs/noconfig.md" "[tr-badshape] not written"

test_summary
