# Document Generation Guide

This guide shows how to wire document generation skills into your Oasis agents. For the concept overview, see [Document Generation](../concepts/document-generation.md).

## Prerequisites

- Sandbox image with `oasis-render` pre-installed (see Docker setup below)
- Skills directory with `oasis-design-system`, `oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx`

## Setup

### 1. Wire Skills into Your Agent

```go
package main

import (
    "github.com/nevindra/oasis"
    skilltool "github.com/nevindra/oasis/tools/skill"
)

func main() {
    // Create a FileSkillProvider pointing at your skills directory.
    skillProvider := oasis.NewFileSkillProvider("./skills")

    // Create the skill tool.
    skills := skilltool.New(skillProvider)

    // Wire into your agent.
    agent := oasis.NewLLMAgent(oasis.AgentConfig{
        // ... provider, store, etc.
        Tools: []oasis.Tool{
            skills,
            // ... other tools (shell, file_write, etc.)
        },
    })
}
```

The agent now has access to `skill_discover`, `skill_activate`, `skill_create`, and `skill_update`. When the user asks to generate a document, the agent discovers the right skill, activates it, and follows the instructions.

### 2. Docker Image

Extend the sandbox Dockerfile with document generation dependencies:

```dockerfile
FROM oasis-ix:latest

# uv for fast Python package installs.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

# Python deps.
COPY requirements.txt /tmp/requirements.txt
RUN uv pip install --system --no-cache -r /tmp/requirements.txt

# Node.js deps.
RUN npm install -g pptxgenjs playwright-core chartjs-node-canvas sharp

# Renderer scripts + CLI.
COPY renderers/ /opt/oasis/renderers/
COPY bin/oasis-render /usr/local/bin/oasis-render
RUN chmod +x /usr/local/bin/oasis-render
```

### 3. Skills Directory

Place the bundled skills in your project:

```
skills/
├── oasis-design-system/
│   └── SKILL.md
├── oasis-pdf/
│   ├── SKILL.md
│   ├── templates/
│   │   ├── report.html
│   │   └── invoice.html
│   └── references/
│       ├── print-css.md
│       └── chart-patterns.md
├── oasis-docx/
│   ├── SKILL.md
│   └── references/
├── oasis-xlsx/
│   ├── SKILL.md
│   └── references/
└── oasis-pptx/
    ├── SKILL.md
    └── references/
```

## Usage Examples

### Generate a PDF Report

User says: "Create a Q4 financial report as a PDF."

The agent will:
1. Call `skill_discover` to list available skills
2. Call `skill_activate("oasis-pdf")` to load PDF generation instructions
3. Write an HTML file with Tailwind CSS and Chart.js charts
4. Call `shell("oasis-render pdf report.html report.pdf --size A4")`

### Generate an Excel Spreadsheet

User says: "Create a budget spreadsheet with monthly data."

The agent will:
1. Activate `oasis-xlsx`
2. Write a JSON spec with sheets, columns, rows, formulas, and charts
3. Call `shell("oasis-render xlsx spec.json budget.xlsx")`

### Generate a PowerPoint Deck

User says: "Make a pitch deck for our Series A."

The agent will:
1. Activate `oasis-pptx`
2. Write a JSON spec with theme, cover, content slides with charts, and summary
3. Call `shell("oasis-render pptx spec.json pitch-deck.pptx")`

### Fill an Existing PDF Form

User says: "Fill this tax form with my details."

The agent will:
1. Activate `oasis-pdf` (FILL route)
2. Write a JSON file with field name/value pairs
3. Call `shell("oasis-render pdf-fill form.pdf filled.pdf --fields fields.json")`

## Adding Custom Skills

You can create additional document skills. For example, a company-specific report template:

```
skills/
└── acme-quarterly-report/
    └── SKILL.md
```

The SKILL.md can reference `oasis-design-system` and `oasis-pdf`, layering company-specific formatting and content structure on top.

## Troubleshooting

### Charts don't render in PDF
Chart.js needs `animation: false` so the chart is fully drawn before Playwright captures the page. Check that all chart configurations include `options: { animation: false }`.

### Fonts look different
The sandbox image includes system fonts. For custom fonts, add a Google Fonts `<link>` in the HTML head. Playwright will load them before rendering.

### PPTX positions look wrong
Use percentage-based positioning (`"x": "5%"`) not inches or pixels. Percentages scale correctly across screen sizes.

### Large Excel files are slow
For very large datasets (100k+ rows), use `xlsxwriter` via `execute_code` instead of the JSON spec approach. The JSON spec is optimized for structured reports, not bulk data export.
