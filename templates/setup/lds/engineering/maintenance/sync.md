# Layered Documentation System (LDS) — Sync

**For AI Agents**: This document contains instructions for auditing documentation against the current implementation and synchronizing any drift. Use this for periodic maintenance or before releases.

**Terminology**: See the [Glossary](../DOCUMENTATION-GUIDE.md#glossary) for definitions of key terms.

---

## When to Use This Guide

Use this guide for:
- Periodic documentation audits (monthly, quarterly)
- Pre-release documentation verification
- After significant refactoring
- When documentation accuracy is questioned
- Onboarding new team members (verify docs are current)

**Do NOT use this guide for**:
- Updating after known code changes (use `update.md`)
- Quality improvements (use `refine.md`)

---

## Sync Process Overview

```
1. Audit Generated Docs → Are CLI/config references current?
2. Run Behavior Tests   → Do Gherkin specs pass?
3. Verify Cross-Links   → Are all links valid?
4. Check Specifications → Do specs match implementation?
5. Review ADRs          → Are decisions still current?
6. Resolve Drift        → Fix discrepancies
7. Report Findings      → Document audit results
```

---

## Sync Process

### Step 1: Audit Generated Reference Docs

Reference documentation is often generated from code or should match code exactly.

#### 1a: CLI Reference Audit

Compare `engineering/reference/cli.md` and `engineering/reference/appendices/cli/` against actual CLI:

```bash
# Get actual CLI help
{command} --help
{command} {subcommand} --help
# ... for all commands
```

For main `README.md`, verify:
- All commands listed
- Brief descriptions accurate
- Links to appendices work

For each command documented (in README or appendices):
- [ ] Command exists in CLI
- [ ] All options are documented
- [ ] All documented options exist
- [ ] Defaults match actual defaults
- [ ] Types are correct
- [ ] Examples work as shown

Record discrepancies:

> **CLI Reference Drift**
>
> | Issue | Document Says | CLI Actually |
> |-------|---------------|--------------|
> | Missing option | — | `--new-flag` exists |
> | Wrong default | `--port=8080` | `--port=3000` |
> | Removed command | `foo bar` documented | Command doesn't exist |

#### 1b: Configuration Reference Audit

Compare `engineering/reference/configuration.md` and `engineering/reference/appendices/configuration/` against actual config handling:

For main `configuration.md`:
- All config sections listed
- Links to detailed appendices work

For each option (in README or appendices):
- [ ] Option exists in code
- [ ] Default in docs matches code default
- [ ] Type is correct
- [ ] Environment variable mapping correct
- [ ] Example configurations work

Record discrepancies similarly.

#### 1c: API Reference Audit (if applicable)

If API documentation exists (`engineering/reference/api.md` and `appendices/api/`):
- Compare against OpenAPI spec (if generated)
- Verify all endpoints are documented in main doc or appendices
- Check request/response schemas in appendices for accuracy
- Verify links from main doc to detailed endpoint docs

---

### Step 2: Run Behavior Tests

Execute Gherkin specifications to verify they match implementation:

```bash
# Run Cucumber/Gherkin tests
npm test
# or
cucumber-js features/
# or equivalent for your test framework
```

Record results:

> **Behavior Test Results**
>
> - **Total Scenarios**: {N}
> - **Passing**: {N}
> - **Failing**: {N}
> - **Pending/Undefined**: {N}
>
> **Failing Scenarios**:
> | Feature | Scenario | Failure |
> |---------|----------|---------|
> | `workspace.feature` | "Start workspace" | Step "Then proxy is running" failed |

For each failure, determine:
1. Is the documentation wrong? (spec needs update)
2. Is the implementation wrong? (code needs fix)
3. Is the test wrong? (step definition needs fix)

---

### Step 3: Verify Cross-Links

Check that all internal documentation links are valid:

#### 3a: Collect All Links

Scan all Markdown files for internal links:
```markdown
[Link text](../path/to/file.md)
[Link text](./file.md#section)
```

#### 3b: Validate Each Link

For each link:
- [ ] Target file exists
- [ ] Target section exists (if anchor specified)
- [ ] Link text is still accurate

Record broken links:

> **Broken Links Found**
>
> | Source File | Link | Issue |
> |-------------|------|-------|
> | `specs/foo.md` | `../decisions/0005-*.md` | File not found |
> | `architecture/overview.md` | `#networking` | Section doesn't exist |

---

### Step 4: Check Specifications Against Implementation

For each specification in `engineering/specs/` (main `.md` files and `appendices/`):

#### 4a: Read the Specification

Read both `engineering/specs/{topic}.md` and any `appendices/{topic}/` files.

Identify key behavioral claims:
- "When X happens, Y occurs"
- "The system validates Z"
- "Error code ABC means..."

#### 4b: Verify Against Code

For each claim, verify it matches implementation:
- Check the relevant code paths
- Verify error handling matches
- Verify state transitions match
- Verify schemas match

#### 4c: Record Discrepancies

Note the file path precisely (main doc vs. appendix):

> **Specification Drift**
>
> | Specification | Claim | Actual Behavior |
> |---------------|-------|-----------------|
> | `port-assignment.md` | "Ports start at 8000" | Ports start at 9000 |
> | `appendices/workspace-lifecycle/states.md` | "Error if workspace exists" | Warning only, continues |

---

### Step 5: Review ADR Currency

For each ADR in `engineering/decisions/` (ADRs are simple single files, no appendices):

#### 5a: Check Status

- Is the decision still "Accepted"?
- Has it been superseded without marking?
- Is it still relevant?

#### 5b: Check Implementation Alignment

- Is the decision actually implemented?
- Has the implementation drifted from the decision?

If implementation differs from ADR:
1. **If intentional**: Create new ADR to supersede
2. **If unintentional**: Flag as implementation bug

> **ADR Review**
>
> | ADR | Status | Implementation Matches? | Notes |
> |-----|--------|------------------------|-------|
> | 0001 | Accepted | Yes | — |
> | 0003 | Accepted | Partial | Feature X not implemented |
> | 0007 | Accepted | No | Changed to different approach |

---

### Step 6: Resolve Drift

For each discrepancy found, determine resolution:

#### Resolution Decision Tree

```
Is the documentation correct and code wrong?
├─ YES → File implementation bug
└─ NO ↓

Is the code correct and documentation wrong?
├─ YES → Update documentation (use update.md)
└─ NO ↓

Is this an intentional undocumented change?
├─ YES → Document the change (update.md), possibly new ADR
└─ NO → Investigate further
```

#### Batch Resolutions

Group similar fixes:

> **Resolutions Needed**
>
> **Documentation Updates**:
> - [ ] Update `cli.md` with 3 new options
> - [ ] Update `appendices/cli/workspace-commands.md` with detailed examples
> - [ ] Update `port-assignment.md` default port value
> - [ ] Fix 2 broken links in `architecture/overview.md`
>
> **Implementation Bugs**:
> - [ ] File bug: Workspace error handling doesn't match spec
>
> **New Documentation Needed**:
> - [ ] Create ADR for changed approach in feature X

---

### Step 7: Execute Resolutions

For each resolution:

#### Documentation Updates

Follow `update.md` process for each update:
1. Update authoritative source
2. Cascade to derived documents
3. Increment versions
4. Update revision history

#### Implementation Bugs

Create issues/tickets for implementation fixes:
```markdown
## Bug: [Title]

**Spec Reference**: engineering/specs/{file}.md

**Expected Behavior** (per spec):
{quote from specification}

**Actual Behavior**:
{description of current behavior}

**Resolution**: Update implementation to match spec
```

#### New Documentation

For new ADRs, create a file in `decisions/` using the appropriate template.
For new specifications, create a file in `specs/` following the project's specification template.

---

### Step 8: Final Validation

After all resolutions are applied:

1. **Re-run behavior tests**: All should pass
2. **Re-check links**: All should resolve
3. **Spot-check specs**: Sample verification

---

### Step 9: Document Audit Results

Create an audit report:

> **Documentation Sync Audit Report**
>
> **Date**: {date}
> **Auditor**: {human/AI}
>
> ## Summary
>
> | Category | Issues Found | Resolved | Remaining |
> |----------|--------------|----------|-----------|
> | CLI Reference | 5 | 5 | 0 |
> | Config Reference | 2 | 2 | 0 |
> | Specifications | 3 | 2 | 1 (bug filed) |
> | Cross-Links | 4 | 4 | 0 |
> | ADRs | 1 | 1 | 0 |
> | Behavior Tests | 2 | 2 | 0 |
>
> ## Documents Updated
>
> | Document | Changes | New Version |
> |----------|---------|-------------|
> | `engineering/reference/cli.md` | Added 3 options, fixed 2 defaults | — |
> | `engineering/reference/appendices/cli/workspace-commands.md` | Updated examples | — |
> | `engineering/specs/port-assignment.md` | Corrected default port | 0.5.2 |
>
> ## Bugs Filed
>
> - #123: Workspace error handling doesn't match spec
>
> ## ADRs Created
>
> - ADR-0015: Supersedes ADR-0007 for new port approach
>
> ## Next Audit
>
> Recommended: {date}

---

## Quick Reference

### Audit Frequency Recommendations

| Project Phase | Audit Frequency |
|---------------|-----------------|
| Active development | Monthly |
| Maintenance mode | Quarterly |
| Pre-release | Always |
| Post-major-refactor | Always |

### Priority Order for Fixes

1. **Behavior test failures** — These block verification
2. **Reference doc drift** — Users rely on these
3. **Broken links** — Breaks navigation
4. **Spec drift** — Important for developers
5. **ADR currency** — Historical accuracy

### Drift Detection Signals

| Signal | Likely Cause |
|--------|--------------|
| Behavior tests failing | Code or test changed without updating other |
| CLI --help differs from docs | Docs not updated after CLI change |
| Config defaults wrong | Code changed without doc update |
| Broken links | File renamed, deleted, or moved to appendix |
| ADR not implemented | Incomplete implementation or changed plan |
| Appendix out of sync with README | Content moved but links not updated |

### Directory Structure to Audit

When auditing, check both main documents and appendices:

```
engineering/
├── decisions/NNNN-{topic}.md   # Check status, implementation (simple files)
├── specs/
│   ├── {feature}.md            # Check behavior claims
│   └── appendices/{feature}/   # Check schemas, examples, edge cases
├── reference/
│   ├── {topic}.md              # Check command/option lists
│   └── appendices/{topic}/     # Check detailed examples, catalogs
└── ...
```
