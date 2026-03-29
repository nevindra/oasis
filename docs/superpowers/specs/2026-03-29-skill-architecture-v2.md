# Skill Architecture v2 — Design Spec

**Date:** 2026-03-29
**Status:** Approved design, pending implementation

---

## Context

Oasis has a skill system (`SkillProvider`, `FileSkillProvider`, `BuiltinSkillProvider`) that lets agents discover and activate instruction packages at runtime. Alongside this, document generation uses a spec-driven pipeline (`oasis-render`) where agents write JSON specs and a deterministic renderer produces output files.

Two questions prompted this design review:

1. **Do we need a second primitive** (Recipe/Procedure) to separate "knowledge" skills from "capability" skills?
2. **Is oasis-render the right approach** given that LLMs are strong at HTML/CSS and direct library usage?

Additionally, the [AgentSkills open specification](https://agentskills.io/specification.md) has emerged as a cross-tool standard adopted by Claude Code, OpenClaw, and others. Oasis should align with this ecosystem.

---

## Decisions

### 1. One Skill Type — No Procedure

After evaluating two separate types (Skill + Procedure) against a single type across seven real-world scenarios (coding agent, document generation, customer support, multi-agent routing, self-improvement, composition, future extensibility), the conclusion is:

**A single Skill type is sufficient.** The difference between "knowledge" and "capability" is expressed through instruction content, not through the type system. This aligns with:

- **AgentSkills spec** — one type, no subtypes
- **ENGINEERING.md** — "Consolidate aggressively. Fewer, more powerful primitives beat many narrow ones."

Typed schemas (Input/Output fields) were evaluated and rejected because:
- The primary consumer of skill metadata is LLMs, which read text, not typed fields
- Multi-agent routing uses semantic matching (LLM reads descriptions), not programmatic type checking
- No framework-level logic currently requires typed fields
- Schemas can be added post-v1 via optional interface assertion if a real programmatic consumer emerges

### 2. Remove oasis-render

Production observation: LLMs rarely trigger oasis-render. They prefer generating HTML/CSS directly (then converting via Playwright) because it gives unlimited creative freedom. The JSON spec format is a ceiling — it can only express what the spec schema supports.

**oasis-render is removed.** Replaced by prescriptive skills that teach agents to use underlying libraries directly:

| Format | Before (spec-driven) | After (skill-driven) |
|--------|---------------------|---------------------|
| PDF | JSON → `render.js` | Agent writes HTML/CSS → Playwright converts to PDF |
| DOCX | JSON → `generate.py` | Agent uses python-docx or docx-js directly, guided by skill |
| XLSX | JSON → `generate.py` | Agent uses openpyxl directly, guided by skill |
| PPTX | JSON → `compile.js` | Agent uses PptxGenJS directly, guided by skill |

Why this is better:
- **No capability ceiling** — agent has full library API access
- **Scales with LLM intelligence** — smarter LLMs produce better output with no framework changes
- **Quality** — LLM-generated HTML/CSS PDFs are more beautiful than template-rendered ones
- **Consistency** — achieved through validation scripts in `scripts/`, not through deterministic rendering
- **Aligns with ENGINEERING.md** — "Will this still work when agents get 10x smarter?"

### 3. AgentSkills Compatibility

Oasis aligns with the [AgentSkills specification](https://agentskills.io/specification.md) for cross-tool skill sharing. Skills written for Claude Code or OpenClaw work in Oasis, and vice versa.

Spec-required fields:
- `name` (required) — matches parent directory name
- `description` (required) — what the skill does and when to use it

Spec-optional fields Oasis should support:
- `compatibility` — environment requirements (replaces ad-hoc gating)
- `metadata` — arbitrary key-value for extensions
- `allowed-tools` — pre-approved tools (experimental in spec)
- `license` — license reference

Oasis-specific extensions (stored as first-class fields, compatible via metadata fallback):
- `model` — LLM override for complex skills
- `tags` — categorization
- `references` — other skill names to auto-load

Scan paths for cross-tool discovery:
- Project-level: `<project>/.agents/skills/`
- User-level: `~/.agents/skills/`
- Bundled: `BuiltinSkillProvider` (go:embed)

---

## Framework Behavior Changes

Five gaps in how the framework handles skills, addressed without adding new exported types.

### Gap 1: Tool Validation at Activation

**Current:** `Tools` field is metadata only — no validation.
**New:** Framework validates all declared tools exist when a skill is activated. Fail early with actionable error rather than failing mid-task.

### Gap 2: Auto-load Referenced Skills

**Current:** `References` field is metadata only — agent must manually activate each reference.
**New:** Framework auto-loads referenced skills at activation time. Referenced skill instructions are injected into context before the activating skill's own instructions. Graceful fallback if a reference is missing.

### Gap 3: Pre-activation Support

**Current:** All skills must be discovered and activated by the agent at runtime.
**New:** `WithActiveSkills()` agent option pre-activates skills at init time. Three progressive disclosure levels:

```go
// Level 1: Pre-activated (simple bots — capability always available)
oasis.WithActiveSkills(pdfSkill, designSkill)

// Level 2: Agent-discovered (autonomous agents — agent explores)
oasis.WithSkills(provider)

// Level 3: Both (production apps — core + discoverable)
oasis.WithActiveSkills(coreSkills...)
oasis.WithSkills(provider)
```

### Gap 4: Directory Placeholder Resolution

**Current:** Skill has `Dir` field but no convention for referencing bundled files.
**New:** `{dir}` placeholder in Instructions is resolved to the skill's absolute directory path at activation time. Convention for subdirectories:

```
skill-name/
├── SKILL.md          # instructions
├── scripts/          # executable code (validation, conversion)
├── references/       # detailed docs loaded on-demand
├── templates/        # copy-paste starting points
└── assets/           # static resources
```

### Gap 5: Compatibility Gating at Discovery

**Current:** All skills appear in discovery regardless of environment.
**New:** `Compatibility` field parsed at discovery time. Skills with unmet requirements are filtered out — agent never sees skills it can't execute.

---

## Skill Content Strategy

The power of a skill comes from its instructions, not from framework machinery. Skills should follow AgentSkills best practices:

### Prescriptive Patterns
- **Gotchas sections** — concrete corrections to mistakes the agent will make without being told
- **Validation loops** — agent validates output, fixes issues, re-validates
- **Plan-validate-execute** — structured pipeline within instructions
- **Defaults over menus** — pick one approach, mention alternatives briefly
- **Templates** — concrete output format examples in `templates/`
- **Bundled scripts** — reusable tested code in `scripts/`

### Progressive Disclosure Within Skills
- `SKILL.md` body: <500 lines, <5000 tokens — core instructions for every run
- `references/`: detailed docs loaded only when needed
- `scripts/`: validation and conversion tools
- `templates/`: starting points for output

### Example: Rewritten oasis-pdf Skill

```
skills/oasis-pdf/
├── SKILL.md
├── scripts/
│   └── validate-pdf.sh
├── references/
│   ├── chart-patterns.md
│   └── print-css.md
└── templates/
    ├── invoice.html
    └── report.html
```

SKILL.md teaches: write HTML + Tailwind CSS → render via Playwright → validate. Full creative freedom. Gotchas section prevents common Playwright PDF mistakes. Templates provide starting points. Validation script checks output.

---

## Migration

### Files to Remove
- `bin/oasis-render` — CLI entry point
- `renderers/` — all renderer scripts (pdf/render.js, docx/generate.py, xlsx/generate.py, pptx/compile.js, fill scripts)
- `requirements.txt` — Python deps for renderers (if only used by renderers)

### Files to Rewrite
- `skills/oasis-pdf/SKILL.md` — prescriptive HTML/CSS + Playwright approach
- `skills/oasis-docx/SKILL.md` — prescriptive python-docx or docx-js approach
- `skills/oasis-xlsx/SKILL.md` — prescriptive openpyxl approach
- `skills/oasis-pptx/SKILL.md` — prescriptive PptxGenJS approach
- `skills/oasis-design-system/SKILL.md` — design tokens (unchanged, still relevant)
- `skill_builtin.go` — update embedded skills

### Type Changes
- Add `Compatibility string` to Skill struct
- Add `License string` to Skill struct
- Add `Metadata map[string]string` to Skill struct
- Update frontmatter parser to handle new fields

### New Framework Behavior
- Tool validation in activation path
- Reference auto-loading in activation path
- `WithActiveSkills()` agent option
- `{dir}` placeholder resolution
- Compatibility filtering in discovery path
- Scan `~/.agents/skills/` and `<project>/.agents/skills/` directories

### Dockerfile Impact
- Remove renderer-specific deps from `cmd/ix/Dockerfile` if no skill needs them
- Keep Playwright/Chromium (needed for PDF generation via skill-driven approach)
- Keep Python + openpyxl, python-docx (needed for DOCX/XLSX via skill-driven approach)
- Keep Node + PptxGenJS (needed for PPTX via skill-driven approach)
- Remove oasis-render CLI from image

### CI Impact
- Update `build-ix.yml` paths: remove `renderers/**` and `bin/oasis-render` triggers
- Update Dockerfile path remains relevant

---

## Non-Goals

- **Typed schemas for skill I/O** — not needed today. Can be added post-v1 via interface assertion.
- **Skill marketplace/registry** — out of scope. AgentSkills ecosystem (ClawHub, etc.) handles this.
- **Programmatic recipe execution** — skills are LLM-driven, not framework-executed pipelines.
- **Strict AgentSkills validation** — Oasis follows lenient parsing per spec recommendation.
