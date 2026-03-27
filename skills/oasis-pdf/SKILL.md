---
name: oasis-pdf
description: Generate PDF documents from HTML and Tailwind CSS. Use when the user asks to create, generate, or export a PDF file — reports, invoices, resumes, proposals, or any printable document.
tags: [document, pdf, export]
tools: [shell, file_write, file_read]
references: [oasis-design-system]
---

# PDF Generation

Generate PDFs by writing HTML with Tailwind CSS, then rendering via Playwright.

## Route Table

| Route | Trigger | Pipeline |
|-------|---------|----------|
| CREATE | "create/generate/make/export a PDF" | Write HTML -> `oasis-render pdf` |
| FILL | "fill this PDF form / populate form fields" | Write fields JSON -> `oasis-render pdf-fill` |

## CREATE Route

### Step 1: Write HTML

Write a complete HTML file with Tailwind CDN. The file must be self-contained — all styles inline or via Tailwind classes.

**Base structure:**

```html
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        @page {
            size: A4;
            margin: 1in;
        }
        @media print {
            .page-break { page-break-before: always; }
            .no-break { page-break-inside: avoid; }
        }
    </style>
</head>
<body class="font-sans text-gray-800 text-sm leading-relaxed">

    <!-- Cover page -->
    <div class="flex flex-col justify-center items-center min-h-screen text-center">
        <h1 class="text-4xl font-bold text-slate-800">Document Title</h1>
        <p class="mt-4 text-lg text-slate-500">Subtitle or description</p>
        <p class="mt-8 text-sm text-slate-400">March 2026</p>
    </div>

    <!-- Page break -->
    <div class="page-break"></div>

    <!-- Content pages -->
    <h2 class="text-xl font-semibold text-slate-700 border-b-2 border-blue-600 pb-2 mb-4">
        Section Title
    </h2>
    <p>Content here...</p>

</body>
</html>
```

### Step 2: Render

```bash
oasis-render pdf report.html report.pdf --size A4
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `--size` | `A4` | A4, Letter, Legal, A3 |
| `--margins` | `1in` | CSS margin format |
| `--landscape` | false | Landscape orientation |
| `--header` | none | HTML for page header |
| `--footer` | none | HTML for page footer |

### Design Rules

1. **Use Tailwind classes** for all styling. Never write raw CSS unless it's print-specific (`@page`, `@media print`).
2. **Use the design system palette.** Apply colors from oasis-design-system — pick one palette and be consistent.
3. **Page breaks.** Use `<div class="page-break"></div>` between logical sections. Use `class="no-break"` on elements that must not split across pages (tables, figures).
4. **Tables.** Always add `class="no-break"` to tables shorter than half a page. For long tables, let them flow naturally.
5. **Charts.** Use inline Chart.js with a `<canvas>` element. The chart renders in Chromium before PDF capture. See references/chart-patterns.md.
6. **Images.** Use `<img>` with absolute paths or base64 data URIs. Relative paths resolve from the HTML file's directory.
7. **Fonts.** Tailwind CDN loads Inter by default. For serif documents, add a Google Fonts link.

### Document Types

| Type | Cover | Font | Palette | Notes |
|------|-------|------|---------|-------|
| Report | Full-page centered title | sans | Corporate | Formal, structured sections |
| Invoice | Company header + invoice # | mono data, sans labels | Minimal | Table-heavy, totals row bolded |
| Resume | Name + contact header | sans | Minimal | Two-column layout, tight spacing |
| Proposal | Full-page hero + subtitle | sans | Corporate or Bold | Executive summary first |
| Academic | Title page + abstract | serif | Minimal | Footnotes, bibliography, double-spaced option |
| Dashboard | No cover, data-first | mono data, sans labels | Corporate | Grid of chart cards |

## FILL Route

For filling existing PDF forms (AcroForms).

### Step 1: Write fields JSON

```json
{
    "full_name": "John Doe",
    "date": "2026-03-27",
    "amount": "$1,250.00",
    "signature": "John Doe"
}
```

### Step 2: Fill

```bash
oasis-render pdf-fill form.pdf filled.pdf --fields fields.json
```
