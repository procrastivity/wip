# Layered Documentation System (LDS) — Audit

**For AI Agents**: This document describes the audit system. During installation, this file is customized and placed in `{ENG_DOCS_DIR}/maintenance/audit.md`.

**Terminology**: See the [Glossary](../DOCUMENTATION-GUIDE.md#glossary) for definitions of `ENG_DOCS_DIR`, `LEGACY_DOCS_DIR`, and other terms.

---

## Overview

The audit system provides instructions for verifying documentation completeness and correctness. During installation, a customized version of this file is generated in the maintenance directory.

### Audit File Location

After running `install.md`, the audit file is located at:
- `{ENG_DOCS_DIR}/maintenance/audit.md`

For example: `engineering/maintenance/audit.md`

### Audit Templates

This file is generated from templates in `maintenance/audit/`:

| Template | Purpose |
|----------|---------|
| `common-audit.md` | Core audit process (always included) |
| `audit-fresh-install.md` | For fresh installs without source migration |
| `audit-migration.md` | For migrations with source comparison |

---

## Purpose

The audit workflow:
1. Compares generated documentation against original source (for migrations)
2. Calculates content distribution across layers
3. Identifies content that may have been missed or misclassified
4. Reports on documentation completeness

---

## When to Run the Audit

- **Immediately after migration**: Validates the migration was complete
- **After manual content moves**: Verify content distribution after human review
- **Periodic maintenance**: Check for drift or orphaned content
- **Before major updates**: Baseline before adding new content

---

## Running the Audit

To run an audit:

```
Execute the audit in @engineering/maintenance/audit.md
```

The audit file contains all necessary instructions, including:
- Source and target directories (for migrations)
- Step-by-step audit process
- Report format

---

## Manual Audit (Without Generated File)

If you need to run an audit without a generated `audit.md` file, use the process below.

### Step 1: Gather Inputs

Identify the directories to compare:

> **Audit Configuration**
>
> Please provide:
> 1. **Documentation directory**: Path to installed docs (e.g., `engineering/`)
> 2. **Source directory** (optional): Path to original source docs for comparison
>
> If no source directory is provided, the audit will analyze the installed documentation structure only.

Store as:
- `ENG_DOCS_DIR` — Documentation root (installed documentation)
- `LEGACY_DOCS_DIR` — Legacy documentation (null if not provided)

---

### Step 2: Read Thresholds from DOCUMENTATION-GUIDE.md

Read `{ENG_DOCS_DIR}/DOCUMENTATION-GUIDE.md` and extract the Content Thresholds section:

```
THRESHOLDS = {
  CODE_BLOCK_LINES: {value},
  STEP_LIST_ITEMS: {value},
  TABLE_ROWS: {value},
  EXAMPLE_FILE_ALWAYS_APPENDIX: {value},
  ERROR_CATALOG_ALWAYS_APPENDIX: {value},
  SHELL_SCRIPT_ALWAYS_APPENDIX: {value}
}
```

If thresholds are not found, use the defaults documented in `DOCUMENTATION-GUIDE.md`.

---

### Step 3: Inventory Installed Documentation

Scan `{ENG_DOCS_DIR}` and categorize all content:

#### 3a: Layer Content

For each layer directory, count:
- Number of documents (README.md files)
- Total lines in main content
- Number of appendix files
- Total lines in appendices

```
LAYER_INVENTORY = {
  "decisions": {
    documents: N,
    main_lines: N,
    appendix_files: N,
    appendix_lines: N
  },
  "product": { ... },
  "architecture": { ... },
  "specs": { ... },
  "reference": { ... },
  "implementation": { ... }
}
```

#### 3b: Migration Content

If `{ENG_DOCS_DIR}/migration/` exists:
- List all files with their categories
- Count total lines
- Extract suggested destinations from comments

```
MIGRATION_INVENTORY = {
  files: [
    { path: "migration/decisions-or-specs/alternatives.md", lines: N, suggested: "..." },
    ...
  ],
  total_lines: N
}
```

#### 3c: Blackhole Content

If `{ENG_DOCS_DIR}/blackhole/` exists:
- List all files with source attribution
- Count total lines
- Extract suggested heuristic updates from README.md

```
BLACKHOLE_INVENTORY = {
  files: [
    { path: "blackhole/source-lines-N-M.md", lines: N, source: "...", reason: "..." },
    ...
  ],
  total_lines: N,
  suggested_heuristics: [ ... ]
}
```

---

### Step 4: Compare Against Source (If Available)

**Skip if `LEGACY_DOCS_DIR` is null.**

#### 4a: Inventory Legacy Documentation

For each file in `{LEGACY_DOCS_DIR}`:
- Count total lines
- Identify major sections (by headings)
- Note content types (code blocks, tables, lists)

```
SOURCE_INVENTORY = {
  files: [
    { path: "prd.md", lines: N, sections: [...] },
    { path: "technical-spec.md", lines: N, sections: [...] },
    ...
  ],
  total_lines: N
}
```

#### 4b: Calculate Coverage

Compare source lines against generated output:

```
COVERAGE = {
  source_total: N,

  migrated_to_layers: {
    main: N,
    appendices: N,
    total: N,
    percentage: X%
  },

  in_migration: {
    lines: N,
    percentage: X%
  },

  in_blackhole: {
    lines: N,
    percentage: X%
  },

  unaccounted: {
    lines: N,
    percentage: X%,
    notes: "May include formatting, comments, or truly lost content"
  }
}
```

**Understanding Coverage Variance**:

Line counts between source and output are expected to differ. Common causes:

| Factor | Effect on Line Count |
|--------|---------------------|
| Template boilerplate | + Output (+10-30 lines/templated file) |
| Source attribution comments | + Output (+2-3 lines/file) |
| Whitespace normalization | +/- Variable |
| Removed duplicate content | - Output |
| Appendix restructuring | Neutral (lines moved) |

**Acceptable thresholds**:
- **migrated_to_layers.percentage** ≥ 90%: Good migration
- **unaccounted.percentage** < 10%: Normal variance
- **unaccounted.percentage** > 15%: Requires investigation

#### 4c: Identify Missing Content

If `unaccounted` lines > 5% of source, investigate:
1. Compare source sections against generated content
2. Identify sections that don't appear in any generated file
3. Check if content was intentionally excluded (e.g., duplicates removed)
4. Flag truly missing content

```
MISSING_CONTENT = [
  { source: "file.md", section: "Section Name", lines: "N-M", status: "missing" | "duplicate" | "excluded" },
  ...
]
```

---

### Step 5: Analyze Appendix Effectiveness

Check if appendix usage matches thresholds:

#### 5a: Check Main Content for Threshold Violations

Scan main README.md files for content that should be in appendices:

```
THRESHOLD_VIOLATIONS = [
  { file: "reference/cli/README.md", issue: "Code block with 75 lines (threshold: 50)", suggested: "Move to appendix" },
  ...
]
```

#### 5b: Check Appendix Content for Unnecessary Splits

Scan appendix files for content that could be in main:

```
UNNECESSARY_APPENDICES = [
  { file: "specs/feature/appendices/small-example.md", lines: 20, note: "Below threshold, could be inline" },
  ...
]
```

---

### Step 6: Analyze Classification Confidence

Review content in migration/ and blackhole/:

#### 6a: Migration Content Review

For each file in migration/:
- Could it be classified with higher confidence now?
- Is the suggested destination correct?
- Should it be in an appendix?

```
MIGRATION_REVIEW = [
  { file: "...", current_confidence: "medium", recommended_action: "move to specs/feature/appendices/", reason: "..." },
  ...
]
```

#### 6b: Blackhole Content Review

For each file in blackhole/:
- Can heuristics be updated to capture this pattern?
- Is this truly unclassifiable?
- What pattern would match this content?

```
BLACKHOLE_REVIEW = [
  { file: "...", pattern_suggestion: "Add heuristic: 'Complete Go file with package declaration' → implementation appendix", priority: "high" },
  ...
]
```

---

### Step 7: Generate Audit Report

Create a comprehensive report:

```markdown
# Documentation Audit Report

**Date**: {timestamp}
**Documentation Root**: {ENG_DOCS_DIR}
**Legacy Documentation**: {LEGACY_DOCS_DIR or "N/A"}

---

## Executive Summary

- **Content Coverage**: {X}% of source content successfully migrated
- **Appendix Usage**: {N} files, {M} lines of detailed content preserved
- **Needs Review**: {N} items in migration/, {M} items in blackhole/
- **Threshold Violations**: {N} items need attention

---

## Content Distribution

| Category | Lines | Percentage |
|----------|-------|------------|
| Layer main content | {N} | {X}% |
| Layer appendices | {N} | {X}% |
| Migration (needs review) | {N} | {X}% |
| Blackhole (unclassified) | {N} | {X}% |
| Unaccounted | {N} | {X}% |

---

## Layer Breakdown

| Layer | Documents | Main Lines | Appendix Files | Appendix Lines |
|-------|-----------|------------|----------------|----------------|
| Decisions | {N} | {N} | {N} | {N} |
| Vision | {N} | {N} | {N} | {N} |
| ... | ... | ... | ... | ... |

---

## Migration Directory Contents

| File | Lines | Category | Suggested Destination |
|------|-------|----------|----------------------|
| {file} | {N} | {cat} | {dest} |
| ... | ... | ... | ... |

**Recommended Actions**:
1. {action for each file}

---

## Blackhole Contents

| File | Lines | Source | Reason |
|------|-------|--------|--------|
| {file} | {N} | {src} | {reason} |
| ... | ... | ... | ... |

**Suggested Heuristic Updates**:

Add these patterns to DOCUMENTATION-GUIDE.md to prevent future blackhole content:

1. `{pattern}` → `{destination}`
2. ...

---

## Threshold Violations

Content in main documents that should be in appendices:

| File | Issue | Recommendation |
|------|-------|----------------|
| {file} | {issue} | {recommendation} |
| ... | ... | ... |

---

## Missing Content (if applicable)

Content from source that could not be found in generated output:

| Source | Section | Lines | Status |
|--------|---------|-------|--------|
| {file} | {section} | {N-M} | {status} |
| ... | ... | ... | ... |

---

## Thresholds Used

| Threshold | Value |
|-----------|-------|
| CODE_BLOCK_LINES | {N} |
| STEP_LIST_ITEMS | {N} |
| TABLE_ROWS | {N} |
| EXAMPLE_FILE_ALWAYS_APPENDIX | {bool} |
| ERROR_CATALOG_ALWAYS_APPENDIX | {bool} |
| SHELL_SCRIPT_ALWAYS_APPENDIX | {bool} |

---

## Recommendations

1. **High Priority**: {recommendations}
2. **Medium Priority**: {recommendations}
3. **Low Priority**: {recommendations}
```

---

### Step 8: Present Report

Present the audit report to the user:

> **Audit Complete**
>
> [Display full report or summary based on size]
>
> **Key Findings**:
> - {summary of main findings}
>
> **Actions Required**:
> - {list of required actions}
>
> Would you like to:
> 1. Save this report to `{ENG_DOCS_DIR}/AUDIT-REPORT.md`?
> 2. Apply suggested fixes automatically?
> 3. Update heuristics in `DOCUMENTATION-GUIDE.md`?

---

## Quick Reference

### Audit Checks Performed

| Check | What It Detects |
|-------|-----------------|
| Layer inventory | Content in each layer with appendices |
| Migration review | Content needing human review |
| Blackhole review | Unclassified content, heuristic gaps |
| Threshold violations | Content in wrong location based on thresholds |
| Coverage analysis | Missing or lost content from source |

### Common Issues and Fixes

| Issue | Cause | Fix |
|-------|-------|-----|
| High blackhole % | Heuristics missing patterns | Add patterns to DOCUMENTATION-GUIDE.md |
| Threshold violations | Content added without checking thresholds | Move to appendices |
| Missing content | Classification failed silently | Check original source, add to appropriate layer |
| Large unaccounted % | Significant content loss | Review source sections, may need re-migration |

---

## Related Workflows

- `install.md` — Initial installation (includes automatic audit for migrations)
- `sync.md` — Synchronize documentation with code changes
- `refine.md` — Improve documentation quality
