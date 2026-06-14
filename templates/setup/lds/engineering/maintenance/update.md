# Layered Documentation System (LDS) — Update

**For AI Agents**: This document contains instructions for updating documentation after implementation changes. Follow this process when code changes require documentation updates.

**Terminology**: See the [Glossary](../DOCUMENTATION-GUIDE.md#glossary) for definitions of key terms.

---

## When to Use This Guide

Use this guide when:
- Code implementation has changed
- A bug fix affects documented behavior
- A feature has been modified or extended
- Configuration options have changed
- CLI commands have been added or modified
- API endpoints have changed

**Do NOT use this guide for**:
- Quality improvements without code changes (use `refine.md`)
- Periodic audits (use `sync.md`)

---

## Update Process

### Step 1: Identify the Change Scope

Determine what changed in the implementation:

> **Implementation Change Summary**
>
> What changed?
> - [ ] New feature added
> - [ ] Existing feature modified
> - [ ] Feature removed
> - [ ] Bug fix that changes behavior
> - [ ] Configuration options changed
> - [ ] CLI commands changed
> - [ ] API endpoints changed
> - [ ] Dependencies or tech stack changed
>
> Describe the change: {brief description}

Store as `CHANGE_DESCRIPTION`.

---

### Step 2: Identify Affected Documents

Based on the change type, identify which documents need updating:

#### Change Type → Document Mapping

| Change Type | Documents to Check |
|-------------|-------------------|
| New feature | Specs, Reference (+ appendices), possibly Architecture |
| Feature modified | Specs, Reference (+ appendices), Behaviors |
| Feature removed | All layers (remove references from main docs and appendices) |
| Bug fix (behavior change) | Specs, Behaviors |
| Config changed | Reference (config + appendices), Specs if behavior affected |
| CLI changed | Reference (CLI + appendices), Specs if behavior affected |
| API changed | Reference (API + appendices), Architecture if contracts changed |
| Dependencies changed | Implementation (tech-stack + appendices), possibly ADR |

**Note**: Remember to check both main `{topic}.md` files and `appendices/{topic}/` directories for affected content.

List all potentially affected documents:

> **Potentially Affected Documents**
>
> Based on the change, these documents may need updating:
> - `engineering/specs/{feature}.md`
> - `engineering/specs/appendices/{feature}/{detail}.md`
> - `engineering/reference/cli.md`
> - `engineering/reference/appendices/cli/detailed-examples.md`
> - `features/{feature}.feature`
> - ...

---

### Step 3: Check Document Hierarchy

Determine which document is the **authoritative source** for the changed information.

Hierarchy (highest to lowest authority):
1. ADRs — If the change contradicts an ADR, the ADR needs updating first (or superseding)
2. Gherkin Behaviors — If tests fail, either fix code or update tests
3. Vision — Rarely needs updating for implementation changes
4. Specifications — Primary target for behavior changes
5. Reference — Primary target for CLI/config/API changes
6. Implementation — Update if tech stack changed

> **Authority Check**
>
> Authoritative source for this change: {document}
> Update this document first, then cascade to derived documents.

---

### Step 4: Check for Decision Changes

Ask: Does this implementation change imply a new architectural decision?

Signals that an ADR is needed:
- "We changed from X to Y because..."
- "We're now using a different pattern..."
- "This breaks backward compatibility because..."
- "Future developers should know that we changed..."

If a new decision is implied:

> **New Decision Detected**
>
> This change implies an architectural decision:
> {describe the decision}
>
> Options:
> 1. Create a new ADR before proceeding
> 2. Document as an inline note (minor change)
> 3. This doesn't warrant an ADR
>
> Which option? (1/2/3)

If option 1: Create a new ADR in `decisions/` using the appropriate template before proceeding.

---

### Step 5: Update Authoritative Document

Update the authoritative source document first.

#### For Specification Updates

1. Read the current specification (`engineering/specs/{feature}.md`)
2. Also read any appendices in `engineering/specs/appendices/{feature}/`
3. Locate the section(s) that describe the changed behavior
4. Update the behavior description to match new implementation
5. Update any examples that are now incorrect
6. Update edge cases if affected
7. Check if appendix content needs updating (detailed examples, schemas)
8. Increment the patch version (e.g., 0.5.0 → 0.5.1)
9. Add entry to Revision History table

```markdown
| 0.5.1 | Jan 2026 | Updated X behavior to handle Y case |
```

#### For Reference Updates

1. Read the current reference document (`engineering/reference/{topic}.md`)
2. Also read any appendices in `engineering/reference/appendices/{topic}/`
3. Locate the section(s) for the changed command/option
4. Update syntax, options, defaults as needed
5. Update examples in main doc (brief) and appendices (detailed)
6. If adding new content, follow existing format exactly
7. Check thresholds: new large content may need to go to appendix
8. Increment version if versioned

#### For Behavior Updates

1. Read the current feature file
2. Determine if scenarios need modification or addition
3. Update Given/When/Then steps to match new behavior
4. Add new scenarios for new behavior
5. Remove scenarios for removed behavior
6. Ensure step definitions are updated accordingly

---

### Step 6: Cascade Updates

After updating the authoritative source, update derived/referencing documents.

#### Update Flow

```
ADR (if new decision)
    ↓
Specification (behavior details)
    ↓
Reference (command/config details)
    ↓
Behaviors (test scenarios)
    ↓
Architecture (if structural change)
```

For each document in the cascade:

1. Check if it references the changed content
2. Update references if needed
3. Remove any duplicated information that's now stale
4. Add links to updated authoritative sources

---

### Step 7: Verify Cross-Links

Ensure all cross-links between documents are still valid:

1. Check links from specifications → ADRs
2. Check links from specifications → reference docs
3. Check links from behaviors → specifications
4. Check links from architecture → specifications

If any links are broken or point to outdated sections:
- Update the link target
- Or update the link to point to the correct location

---

### Step 8: Run Validation Checks

Verify the updates are consistent:

#### For Specifications

- [ ] Behavior description matches implementation
- [ ] Examples are accurate and runnable
- [ ] Edge cases are documented
- [ ] Cross-links to ADRs are correct
- [ ] Version was incremented
- [ ] Revision history updated

#### For Reference Docs

- [ ] All options documented
- [ ] Defaults are correct
- [ ] Examples work as shown
- [ ] Syntax is accurate

#### For Behaviors

- [ ] All scenarios pass
- [ ] New behaviors have scenarios
- [ ] Removed behaviors have scenarios removed
- [ ] Step definitions are implemented

---

### Step 9: Document the Update

Add a revision history entry to each updated document:

```markdown
## Revision History

| Version | Date | Changes |
|---------|------|---------|
| 0.5.2 | Jan 2026 | Updated X to support Y; removed deprecated Z option |
| 0.5.1 | Dec 2025 | Initial specification |
```

---

### Step 10: Final Report

Present a summary of all updates:

> **Documentation Update Complete**
>
> **Change**: {CHANGE_DESCRIPTION}
>
> **Documents Updated**:
> | Document | Changes Made | New Version |
> |----------|--------------|-------------|
> | `engineering/specs/X.md` | Updated Y behavior | 0.5.2 |
> | `engineering/reference/cli.md` | Added Z option | — |
> | `features/X.feature` | Updated scenario | — |
>
> **Cross-Links Verified**: Yes
>
> **New ADR Created**: {Yes: ADR-NNNN | No}
>
> **Next Steps**:
> - Run Gherkin tests to verify behaviors
> - Review changes for accuracy
> - Commit documentation with code changes

---

## Quick Reference

### Version Bump Rules

| Document Type | When to Bump |
|---------------|--------------|
| Specifications | Any behavior change |
| Reference | Only if versioned (often unversioned) |
| ADRs | Never (create new, supersede old) |
| Vision | Major product changes only |
| Architecture | Structural changes |

### Revision History Format

```markdown
| Version | Date | Changes |
|---------|------|---------|
| 0.5.1 | Jan 2026 | Brief description of what changed |
```

### Change Type → Primary Document

| Change | Update First |
|--------|--------------|
| Behavior change | Specification |
| New CLI option | Reference (CLI) |
| Config change | Reference (Config) |
| Architecture change | Architecture → new ADR if significant |
| Decision change | New ADR (supersedes old) |
