# Document Generation Guide

This guide shows how to wire document generation skills into your Oasis agents. For the concept overview, see [Document Generation](../concepts/document-generation.md).

## Prerequisites

- Sandbox image with document generation libraries pre-installed (Playwright, python-docx, openpyxl, PptxGenJS)
- Skills available via `BuiltinSkillProvider` (compiled in) or on disk

## Setup

### 1. Wire Skills into Your Agent

The simplest approach uses `WithSkills` to auto-register skill discovery and activation tools:

```go
package main

import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/skills"
)

func main() {
    // Built-in skills are compiled into the binary — no filesystem needed.
    builtin := oasis.NewBuiltinSkillProvider()

    // Optionally chain with file-based skills (user skills take priority).
    fileProvider := skills.NewFileSkillProvider("./skills")
    provider := oasis.ChainSkillProviders(fileProvider, builtin)

    // WithSkills auto-registers skill_discover and skill_activate tools.
    agent := oasis.NewLLMAgent("assistant", "Document generation agent", llmProvider,
        oasis.WithSkills(provider),
        oasis.WithSandbox(sb, sandbox.Tools(sb)...),
    )
}
```

Alternatively, pre-activate a specific skill so its instructions are always in the system prompt:

```go
// Pre-activate oasis-pdf — agent always has PDF instructions available.
pdfSkill, _ := oasis.ActivateWithReferences(ctx, provider, "oasis-pdf")

agent := oasis.NewLLMAgent("pdf-agent", "PDF generation agent", llmProvider,
    oasis.WithActiveSkills(pdfSkill),
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),
)
```

### 2. Docker Image

The sandbox image needs document generation libraries installed:

```dockerfile
FROM oasis-ix:latest

# uv for fast Python package installs.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

# Python libs for DOCX and XLSX generation.
RUN uv pip install --system --no-cache python-docx openpyxl pypdf

# Node.js libs for PPTX generation.
RUN npm install -g pptxgenjs
```

Playwright and Chromium are already included in the base `oasis-ix` image for browser automation.

### 3. Skills Directory (Optional)

If using file-based skills alongside built-in ones:

```
skills/
├── oasis-design-system/
│   └── SKILL.md
├── oasis-pdf/
│   └── SKILL.md
├── oasis-docx/
│   └── SKILL.md
├── oasis-xlsx/
│   └── SKILL.md
└── oasis-pptx/
    └── SKILL.md
```

The `{dir}` placeholder in skill instructions is resolved to the absolute path of the skill directory at activation time, so skills can reference their own files (e.g., `{dir}/templates/report.html`).

## Usage Examples

### Generate a PDF Report

User says: "Create a Q4 financial report as a PDF."

The agent will:
1. Call `skill_discover` to list available skills
2. Call `skill_activate("oasis-pdf")` to load PDF generation instructions
3. Write Python code using Playwright to render HTML + CSS to PDF
4. Run the code via `execute_code` inside the sandbox

### Generate an Excel Spreadsheet

User says: "Create a budget spreadsheet with monthly data."

The agent will:
1. Activate `oasis-xlsx`
2. Write Python code using openpyxl to create sheets, rows, formulas, and charts
3. Run the code via `execute_code` inside the sandbox

### Generate a PowerPoint Deck

User says: "Make a pitch deck for our Series A."

The agent will:
1. Activate `oasis-pptx`
2. Write JavaScript code using PptxGenJS to create themed slides with charts
3. Run the code via `execute_code` inside the sandbox

## Adding Custom Skills

Create additional document skills that reference the built-in ones. For example, a company-specific report template:

```
skills/
└── acme-quarterly-report/
    └── SKILL.md
```

The SKILL.md can reference `oasis-design-system` and `oasis-pdf` via the `references` frontmatter field. When activated with `ActivateWithReferences`, the referenced skill instructions are prepended automatically.

## Troubleshooting

### Charts don't render in PDF
Chart.js needs `animation: false` so the chart is fully drawn before Playwright captures the page. Check that all chart configurations include `options: { animation: false }`.

### Fonts look different
The sandbox image includes system fonts. For custom fonts, add a Google Fonts `<link>` in the HTML head. Playwright will load them before rendering.

### Large Excel files are slow
For very large datasets (100k+ rows), consider using `xlsxwriter` instead of openpyxl. The agent can install it at runtime via `install_package('xlsxwriter')`.
