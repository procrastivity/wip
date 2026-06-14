# Layered Documentation System (LDS) — Refine

**For AI Agents**: This document contains instructions for improving documentation quality without implementation changes. Use this for documentation review cycles, clarity improvements, and structural enhancements.

**Terminology**: See the [Glossary](../DOCUMENTATION-GUIDE.md#glossary) for definitions of key terms.

---

## When to Use This Guide

Use this guide for:
- Documentation quality reviews
- Improving clarity and readability
- Fixing structural issues (wrong layer, duplication)
- Enhancing cross-linking
- Filling gaps in existing documentation
- Consolidating scattered information

**Do NOT use this guide for**:
- Updating after code changes (use `update.md`)
- Auditing against implementation (use `sync.md`)

---

## Refinement Categories

1. **Layer Placement** — Is content in the correct layer?
2. **Appendix Structure** — Is large content properly in appendices?
3. **Duplication** — Is information mastered in one place only?
4. **Cross-Linking** — Are related documents properly connected?
5. **ADR Coverage** — Are major decisions documented?
6. **Completeness** — Are all features/options documented?
7. **Template Compliance** — Do documents follow templates?
8. **Clarity** — Is content clear and unambiguous?

---

## Refinement Process

### Step 1: Select Refinement Scope

Determine the scope of this refinement session:

> **Refinement Scope**
>
> What would you like to refine?
> 1. **Full audit** — Review all documentation
> 2. **Single layer** — Focus on one layer (specify which)
> 3. **Single document** — Focus on one document (specify which)
> 4. **Specific category** — Focus on one refinement category
>
> Selection: {1/2/3/4}

Store as `REFINEMENT_SCOPE`.

---

### Step 2: Layer Placement Review

For each document (or scoped subset), verify content is in the correct layer.

#### 2a: Read Each Document

For each section of content, apply the classification decision tree:

```
Is this explaining WHY a choice was made?
├─ YES → Should be in Layer 1: Decisions (ADR)
└─ NO ↓

Is this about product vision, goals, or concepts?
├─ YES → Should be in Layer 2: Vision
└─ NO ↓

Is this showing how components relate?
├─ YES → Should be in Layer 3: Architecture
└─ NO ↓

Is this detailing HOW a feature works?
├─ YES → Should be in Layer 4: Specifications
└─ NO ↓

Is this a lookup table?
├─ YES → Should be in Layer 5: Reference
└─ NO ↓

Is this a concrete verifiable scenario?
├─ YES → Should be in Layer 6: Behaviors
└─ NO ↓

Is this implementation scaffolding?
├─ YES → Should be in Layer 7: Implementation
└─ NO → May not need documentation
```

#### 2b: Record Misplacements

> **Layer Placement Issues**
>
> | Document | Section | Current Layer | Should Be |
> |----------|---------|---------------|-----------|
> | `specs/foo.md` | "Why we chose X" | Specifications | Decisions (ADR) |
> | `architecture/overview.md` | CLI command table | Architecture | Reference |

#### 2c: Resolve Misplacements

For each misplacement:
1. Extract the content from current location
2. Create or update document in correct layer (using `{topic}.md` structure)
3. Replace original content with a link to new location
4. Update cross-references

---

### Step 3: Appendix Structure Review

Check that large content is properly placed in appendices per `DOCUMENTATION-GUIDE.md` thresholds.

#### 3a: Read Thresholds

Read `DOCUMENTATION-GUIDE.md` to get current thresholds:
- `CODE_BLOCK_LINES` (default: 50)
- `STEP_LIST_ITEMS` (default: 10)
- `TABLE_ROWS` (default: 20)
- `EXAMPLE_FILE_ALWAYS_APPENDIX` (default: true)
- `ERROR_CATALOG_ALWAYS_APPENDIX` (default: true)
- `SHELL_SCRIPT_ALWAYS_APPENDIX` (default: true)

#### 3b: Scan for Threshold Violations

For each document, check:
- [ ] Code blocks < threshold lines (or in appendix)
- [ ] Step lists < threshold items (or in appendix)
- [ ] Tables < threshold rows (or in appendix)
- [ ] Complete file examples in appendix
- [ ] Error catalogs in appendix
- [ ] Shell scripts in appendix

> **Appendix Structure Issues**
>
> | Document | Issue | Should Be |
> |----------|-------|-----------|
> | `specs/ports.md` | 80-line code example | Move to `appendices/ports/` |
> | `reference/cli.md` | Full error catalog inline | Move to `appendices/cli/errors.md` |

#### 3c: Resolve Structure Issues

For each violation:
1. Create `appendices/{topic}/` directory if needed (where `{topic}` matches the main document basename)
2. Move content to appropriately named appendix file
3. Add brief summary in main document
4. Add link from main document to appendix:
   ```markdown
   For detailed examples, see [Complete Examples](./appendices/ports/examples.md).
   ```
5. Add back-link in appendix:
   ```markdown
   > **Parent**: [Main Document](../../ports.md)
   ```

---

### Step 4: Duplication Review

Check for information that appears in multiple places (SSOT violations).

#### 4a: Identify Common Duplication Patterns

| Pattern | Example | Resolution |
|---------|---------|------------|
| Decision rationale in specs | "We chose X because Y" in spec | Move to ADR, link from spec |
| Config details in specs | Full schema in specification | Move to reference, link from spec |
| Behavior details in reference | "When X happens" in CLI docs | Keep in spec, simplify reference |
| Repeated glossary | Terms defined in multiple docs | Centralize in `product/concepts.md` |

#### 4b: Find Duplicates

Search for similar content across documents:
- Same configuration schemas in multiple files
- Same behavioral descriptions
- Same decision rationale
- Same glossary terms

> **Duplication Found**
>
> | Content | Appears In | Canonical Source |
> |---------|------------|------------------|
> | Port assignment algorithm | `specs/ports.md`, `architecture/overview.md` | `specs/ports.md` |
> | Workspace definition | `product/vision.md`, `specs/workspace-lifecycle.md` | `product/vision.md` |

#### 4c: Resolve Duplicates

For each duplicate:
1. Determine canonical source (use document hierarchy)
2. Keep full content in canonical source (with large content in appendices)
3. Replace duplicate with link:
   ```markdown
   For the port assignment algorithm, see [Port Assignment](../specs/ports.md#algorithm).
   ```
4. Optionally keep a brief summary with the link

---

### Step 5: Cross-Linking Review

Ensure documents are properly interconnected.

#### 5a: Expected Links by Layer

| From | To | Link Purpose |
|------|----|--------------|
| Specification | ADR | Explain "why" for design choices |
| Specification | Reference | Point to detailed syntax/options |
| Architecture | ADR | Justify architectural patterns |
| Architecture | Specification | Deep-dive into component behavior |
| Behavior | Specification | Reference the spec being tested |
| Reference | Specification | Provide conceptual context |
| Implementation | ADR | Explain technology choices |
| Implementation | Specification | Reference what's being implemented |

#### 5b: Audit Links

For each document, check:
- [ ] Links to related documents exist
- [ ] Links follow expected patterns (using `{topic}.md` with `appendices/{topic}/`)
- [ ] Links from main document to appendices work
- [ ] No orphan documents (nothing links to them)
- [ ] No dead-end documents (they link to nothing)

> **Cross-Link Gaps**
>
> | Document | Missing Link | Should Link To |
> |----------|--------------|----------------|
> | `specs/proxy.md` | No ADR reference | Should link to ADR-0008 (Traefik) |
> | `features/workspace.feature` | No spec reference | Should reference `specs/workspace-lifecycle.md` |
> | `specs/ports.md` | No appendix link | Should link to `appendices/ports/examples.md` |

#### 5c: Add Missing Links

Add links using standard format:
```markdown
## Related Documents

- [ADR-0008: Traefik Reverse Proxy](../decisions/0008-traefik-reverse-proxy.md) — Design rationale
- [CLI Reference: workspace commands](../reference/cli.md#workspace) — Command syntax

## Appendices

- [Detailed Examples](./appendices/ports/examples.md) — Complete workflow scenarios
- [Error Catalog](./appendices/ports/errors.md) — Full error message reference
```

---

### Step 6: ADR Coverage Review

Verify that significant decisions are documented as ADRs.

#### 6a: Identify Undocumented Decisions

Scan specifications and architecture docs for decision signals:
- "We chose X..."
- "We decided to..."
- "The pattern is X because..."
- "Unlike typical approaches, we..."
- Trade-off discussions
- Technology choices

#### 6b: Evaluate Each Finding

For each potential decision, ask:
- Is this a significant architectural decision?
- Would it be expensive to reverse?
- Might future developers question this?
- Is it already documented as an ADR?

> **Missing ADRs**
>
> | Found In | Decision | Priority |
> |----------|----------|----------|
> | `specs/networking.md` | "Two-layer networking model" | High — architectural |
> | `implementation/tech-stack.md` | "Go over Rust" | Medium — technology choice |

#### 6c: Create Missing ADRs

For each missing ADR:
1. Create ADR file in `decisions/` using the appropriate template
2. Include extracted rationale as the Context and Decision sections
3. Update source document to link to ADR
4. Remove duplicated rationale from source

---

### Step 7: Completeness Review

Check that documentation coverage is complete.

#### 7a: Feature Coverage

For each feature in the product:
- [ ] Has a specification document
- [ ] CLI commands documented in reference
- [ ] Configuration options documented
- [ ] Has Gherkin behavior tests (for critical features)

#### 7b: Reference Completeness

For CLI reference:
- [ ] All commands documented
- [ ] All subcommands documented
- [ ] All flags/options documented
- [ ] All have examples

For configuration reference:
- [ ] All config options documented
- [ ] All have types and defaults
- [ ] All have descriptions

#### 7c: Record Gaps

> **Documentation Gaps**
>
> | Category | Gap | Priority |
> |----------|-----|----------|
> | Feature | `flavor-switching` has no spec | High |
> | CLI | `workspace status` not documented | Medium |
> | Config | `proxy.timeout` option undocumented | Low |

---

### Step 8: Template Compliance Review

Verify documents follow their layer templates.

#### 8a: Check Each Document

For each document, compare against its template:

| Layer | Template | Required Sections |
|-------|----------|-------------------|
| Decisions | MADR | Title, Status, Context, Decision, Consequences |
| Vision | Lean PRD | Problem, Solution, Success Criteria, Non-Goals |
| Architecture | C4-Lite | Context, Containers, Communication, Cross-Cutting |
| Specifications | Feature Spec | Overview, Behavior, Edge Cases, Examples |
| Reference | CLI | Commands, Options, Examples |
| Behaviors | Gherkin | Feature, Scenarios with Given/When/Then |

#### 8b: Record Non-Compliance

> **Template Compliance Issues**
>
> | Document | Missing Section | Template Requires |
> |----------|-----------------|-------------------|
> | `decisions/0003-*.md` | Consequences | Required by MADR |
> | `specs/proxy.md` | Edge Cases | Recommended by Feature Spec |

#### 8c: Add Missing Sections

Fill in missing required sections. If content isn't available:
```markdown
## Consequences

<!-- TODO: Document positive and negative consequences -->
_This section needs to be completed._
```

---

### Step 9: Clarity Review

Improve readability and remove ambiguity.

#### 9a: Check for Ambiguity

Look for:
- Vague terms ("should", "might", "usually")
- Missing specifics ("the appropriate value", "as needed")
- Undefined terms (jargon without definition)
- Passive voice hiding actors ("the request is processed")

#### 9b: Check for Clarity Issues

- Long paragraphs without structure
- Missing examples for complex concepts
- Technical jargon without explanation
- Inconsistent terminology

> **Clarity Issues**
>
> | Document | Issue | Location |
> |----------|-------|----------|
> | `specs/ports.md` | "appropriate port" — what's appropriate? | Line 45 |
> | `architecture/overview.md` | 500-word paragraph, needs structure | Lines 120-180 |

#### 9c: Improve Clarity

For each issue:
- Replace vague terms with specific values
- Add examples
- Break long paragraphs into lists or subsections
- Define technical terms on first use

---

### Step 10: Apply Refinements

Execute all identified improvements:

1. **Layer relocations**: Move content, add links
2. **Appendix restructuring**: Move large content to appendices
3. **Deduplication**: Consolidate, add links
4. **Cross-links**: Add missing links (including to appendices)
5. **ADRs**: Create missing decision records
6. **Gaps**: Create stub documents or sections
7. **Templates**: Add missing sections
8. **Clarity**: Improve wording

For each change:
- Update version if applicable
- Add revision history entry
- Verify links still work

---

### Step 11: Final Report

> **Documentation Refinement Report**
>
> **Date**: {date}
> **Scope**: {full/layer/document/category}
>
> ## Summary
>
> | Category | Issues Found | Resolved |
> |----------|--------------|----------|
> | Layer Placement | 3 | 3 |
> | Appendix Structure | 4 | 4 |
> | Duplication | 5 | 5 |
> | Cross-Links | 8 | 8 |
> | ADR Coverage | 2 | 2 |
> | Completeness | 4 | 2 (stubs created) |
> | Template Compliance | 3 | 3 |
> | Clarity | 6 | 6 |
>
> ## Documents Modified
>
> | Document | Changes |
> |----------|---------|
> | `specs/ports.md` | Moved decision to ADR, added link, improved clarity |
> | `specs/appendices/ports/examples.md` | Created for large code examples |
> | `architecture/overview.md` | Removed duplicate content, restructured |
>
> ## Documents Created
>
> - `decisions/0012-two-layer-networking.md` — Extracted from spec
> - `specs/flavor-switching.md` — Stub for missing spec
>
> ## Appendices Created
>
> - `specs/appendices/ports/examples.md` — Moved 80-line examples
> - `reference/appendices/cli/errors.md` — Moved error catalog
>
> ## Remaining Items
>
> - [ ] Complete `specs/flavor-switching.md` content
> - [ ] Add examples to `reference/appendices/configuration/`

---

## Quick Reference

### Refinement Priority Order

1. **Layer placement** — Foundation for other fixes
2. **Appendix structure** — Ensures thresholds respected
3. **Duplication** — Prevents conflicting information
4. **ADR coverage** — Preserves decision rationale
5. **Cross-links** — Enables navigation (including appendix links)
6. **Completeness** — Fills gaps
7. **Template compliance** — Consistency
8. **Clarity** — Polish

### Common Quick Fixes

| Issue | Quick Fix |
|-------|-----------|
| Decision in spec | Extract to ADR, link back |
| Repeated content | Keep in higher-authority doc, link from others |
| Large code block | Move to appendix, link from main doc |
| Missing link | Add "Related Documents" section |
| Long paragraph | Convert to bullet list |
| Vague term | Replace with specific value |
| Missing example | Add concrete code/command example |
| Error catalog inline | Move to `appendices/{topic}/errors.md` |
| Complete file example | Move to `appendices/{topic}/examples.md` |

### Layer Authority (for deduplication)

When content appears in multiple layers, keep it in the higher-authority layer:

1. ADRs (highest)
2. Vision
3. Architecture
4. Specifications
5. Reference
6. Behaviors
7. Implementation (lowest)
