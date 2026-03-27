---
name: oasis-docx
description: Generate Word documents (DOCX) from a JSON content specification. Use when the user asks to create, generate, or export a Word document — reports, letters, memos, contracts, or academic papers.
tags: [document, docx, word, export]
tools: [shell, file_write, file_read]
references: [oasis-design-system]
---

# DOCX Generation

Generate Word documents by writing a JSON content specification, then rendering via python-docx.

## Route Table

| Route | Trigger | Pipeline |
|-------|---------|----------|
| CREATE | "create/generate/make a Word document" | Write JSON spec -> `oasis-render docx` |
| TEMPLATE-FILL | "fill this template / use this Word template" | Write data JSON -> `oasis-render docx-fill` |

## CREATE Route

### Step 1: Write JSON Spec

```json
{
    "style": "business",
    "page": {
        "size": "A4",
        "margins": { "top": 1, "bottom": 1, "left": 1.25, "right": 1.25 }
    },
    "content": [
        { "type": "heading", "level": 1, "text": "Document Title" },
        { "type": "paragraph", "text": "Introduction paragraph..." },
        { "type": "heading", "level": 2, "text": "Section" },
        { "type": "table", "headers": ["Col A", "Col B"], "rows": [["val1", "val2"]] },
        { "type": "image", "path": "chart.png", "width": 5, "caption": "Figure 1" },
        { "type": "page_break" },
        { "type": "list", "ordered": true, "items": ["First", "Second", "Third"] },
        { "type": "toc" }
    ]
}
```

### Content Block Types

| Type | Required Fields | Optional Fields | Description |
|------|----------------|-----------------|-------------|
| `heading` | `level` (1-4), `text` | -- | Section heading |
| `paragraph` | `text` | `bold`, `italic`, `align` | Body paragraph |
| `table` | `headers`, `rows` | `caption`, `style` | Data table |
| `image` | `path` | `width` (inches), `caption`, `align` | Embedded image |
| `list` | `items` | `ordered` (bool) | Bulleted or numbered list |
| `page_break` | -- | -- | Force page break |
| `toc` | -- | `depth` (default 3) | Table of contents (field code) |
| `code` | `text` | `language` | Code block (monospace, gray background) |
| `quote` | `text` | `author` | Block quote with optional attribution |
| `hr` | -- | -- | Horizontal rule |

### Step 2: Render

```bash
oasis-render docx spec.json report.docx --style business
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `--template` | none | Base .docx template to use |
| `--style` | `business` | Preset: business, academic, minimal, formal |

## TEMPLATE-FILL Route

For filling existing Word templates with placeholder tokens.

### Step 1: Write data JSON

```json
{
    "{{company_name}}": "Acme Corp",
    "{{date}}": "March 27, 2026",
    "{{client_name}}": "Jane Smith",
    "{{total_amount}}": "$15,000.00"
}
```

### Step 2: Fill

```bash
oasis-render docx-fill template.docx output.docx --data data.json
```

### Style Presets

| Style | Font | Heading Color | Body Size | Use For |
|-------|------|--------------|-----------|---------|
| business | Calibri | #1B2A4A | 11pt | Corporate reports, memos |
| academic | Times New Roman | #000000 | 12pt | Papers, theses (double-spaced) |
| minimal | Inter | #111111 | 10.5pt | Clean, modern documents |
| formal | Garamond | #1a1a1a | 12pt | Legal, contracts |
