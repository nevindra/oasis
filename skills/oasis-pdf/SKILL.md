---
name: oasis-pdf
description: Generate beautiful PDF documents by writing HTML+CSS and rendering via Playwright. Use when the user needs reports, invoices, certificates, or any printable document as PDF.
compatibility: Requires node and playwright-core with chromium in sandbox
tags: [document, pdf, export]
tools: [shell, file_write, file_read, execute_code]
references: [oasis-design-system]
---

# PDF Generation

Generate PDFs by writing a standalone HTML file with Tailwind CSS or inline styles, then rendering to PDF via Playwright's Chromium.

## Approach

1. Write a complete, self-contained HTML file (all styles inline or via Tailwind CDN)
2. Render to PDF with a Node.js one-liner using playwright-core
3. Validate the output file exists and has reasonable size

## Rendering Command

```bash
node -e "
const {chromium} = require('playwright-core');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto('file:///path/to/output.html', {waitUntil: 'networkidle'});
  await page.pdf({
    path: '/path/to/output.pdf',
    format: 'A4',
    printBackground: true,
    margin: {top: '0.75in', bottom: '0.75in', left: '0.75in', right: '0.75in'}
  });
  await browser.close();
})();
"
```

**Key pdf() options:**

| Option | Type | Description |
|--------|------|-------------|
| `path` | string | Output file path |
| `format` | string | A4, Letter, Legal, A3, Tabloid |
| `printBackground` | bool | **Must be true** for colors/backgrounds |
| `landscape` | bool | Landscape orientation |
| `margin` | object | `{top, bottom, left, right}` as CSS strings |
| `headerTemplate` | string | HTML for page header |
| `footerTemplate` | string | HTML for page footer |
| `displayHeaderFooter` | bool | Enable header/footer templates |
| `scale` | number | Scale factor (default 1, range 0.1-2) |

**Header/footer template classes** (Playwright injects values automatically):

```html
<div style="font-size:10px; text-align:center; width:100%;">
  Page <span class="pageNumber"></span> of <span class="totalPages"></span>
</div>
```

Available classes: `pageNumber`, `totalPages`, `date`, `title`, `url`.

## HTML Base Structure

```html
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        @page { size: A4; margin: 0; }
        @media print {
            .page-break { break-before: page; }
            .no-break { break-inside: avoid; }
            h1, h2, h3 { break-after: avoid; }
            thead { display: table-header-group; }
        }
    </style>
</head>
<body class="font-sans text-gray-800 text-sm leading-relaxed">

    <!-- Cover page -->
    <div class="flex flex-col justify-center items-center min-h-screen text-center">
        <h1 class="text-4xl font-bold text-[#1B2A4A]">Document Title</h1>
        <p class="mt-4 text-lg text-[#2D5F8A]">Subtitle or description</p>
        <p class="mt-8 text-sm text-gray-400">March 2026</p>
    </div>

    <!-- Page break -->
    <div class="page-break"></div>

    <!-- Content pages -->
    <h2 class="text-xl font-semibold text-[#1B2A4A] border-b-2 border-[#E8734A] pb-2 mb-4">
        Section Title
    </h2>
    <p>Content here...</p>

</body>
</html>
```

## Design Principles

- **Tailwind CDN or inline CSS** -- the HTML file must be fully self-contained
- **Use `@media print` rules** for page breaks, header repetition, and orphan prevention
- **Pick a palette from oasis-design-system** and apply it consistently via hex values in Tailwind arbitrary values `text-[#1B2A4A]` or inline styles
- **Explicit page dimensions** -- set `@page { size: A4; }` in CSS and matching `format` in the pdf() call
- **Use flexbox or table layout for print** -- CSS grid can behave unpredictably in print rendering

## Page Breaks

```html
<!-- Force page break before this element -->
<div class="page-break"></div>

<!-- Or use style directly on a section div -->
<div style="break-before: page;">
    <h2>New Section</h2>
    <p>Content...</p>
</div>

<!-- Prevent page break inside an element -->
<div class="no-break">
    <table>...</table>
</div>
```

## Tables

Use proper HTML tables with CSS styling. Tailwind classes work well for print.

```html
<table class="w-full border-collapse text-sm no-break">
    <thead>
        <tr class="bg-[#1B2A4A] text-white">
            <th class="p-2 text-left">Name</th>
            <th class="p-2 text-right">Value</th>
            <th class="p-2 text-right">Change</th>
        </tr>
    </thead>
    <tbody>
        <tr class="border-b border-gray-200">
            <td class="p-2">Revenue</td>
            <td class="p-2 text-right font-mono">$2,100,000</td>
            <td class="p-2 text-right text-green-600">+25%</td>
        </tr>
        <tr class="border-b border-gray-200 bg-gray-50">
            <td class="p-2">Expenses</td>
            <td class="p-2 text-right font-mono">$1,050,000</td>
            <td class="p-2 text-right text-red-600">+12%</td>
        </tr>
    </tbody>
</table>
```

Tips:
- Use `thead { display: table-header-group; }` in print CSS so headers repeat on every page for long tables
- Add `class="no-break"` for short tables that should not split across pages
- Alternating row backgrounds: apply `bg-gray-50` to even rows

## Charts and Visualizations

Generate charts with Python (matplotlib or seaborn), save as PNG or SVG, then embed in HTML.

**Step 1: Generate chart image**

```python
import matplotlib.pyplot as plt
import matplotlib
matplotlib.use('Agg')
import base64
from io import BytesIO

fig, ax = plt.subplots(figsize=(8, 4))
quarters = ['Q1', 'Q2', 'Q3', 'Q4']
revenue = [1.2, 1.5, 1.8, 2.1]
ax.bar(quarters, revenue, color='#2D5F8A')
ax.set_ylabel('Revenue ($M)')
ax.set_title('Quarterly Revenue')
plt.tight_layout()

# Save as base64 for embedding
buf = BytesIO()
fig.savefig(buf, format='png', dpi=150, bbox_inches='tight')
buf.seek(0)
b64 = base64.b64encode(buf.read()).decode()
print(b64)  # Use this in the img tag
plt.close()
```

**Step 2: Embed in HTML**

```html
<div class="no-break text-center my-6">
    <img src="data:image/png;base64,{{BASE64_STRING}}"
         style="max-width: 100%; height: auto;"
         alt="Quarterly Revenue Chart" />
    <p class="text-xs text-gray-500 mt-2">Figure 1: Quarterly Revenue</p>
</div>
```

Alternative: save as file and use a relative path:

```python
fig.savefig('/path/to/chart.png', dpi=150, bbox_inches='tight')
```
```html
<img src="chart.png" style="max-width: 100%;" />
```

For inline SVG (sharper at any scale):

```python
buf = BytesIO()
fig.savefig(buf, format='svg', bbox_inches='tight')
svg_str = buf.getvalue().decode()
# Write svg_str directly into the HTML
```

## Document Types

| Type | Cover | Font | Palette | Notes |
|------|-------|------|---------|-------|
| Report | Full-page centered title | sans | Corporate | Formal, structured sections |
| Invoice | Company header + invoice # | mono data, sans labels | Minimal | Table-heavy, totals row bolded |
| Resume | Name + contact header | sans | Minimal | Two-column layout, tight spacing |
| Proposal | Full-page hero + subtitle | sans | Corporate/Bold | Executive summary first |
| Certificate | Centered, decorative | serif | Bold | Borders, large title, signature line |
| Dashboard | No cover, data-first | mono data, sans labels | Corporate | Grid of chart cards |

## Gotchas

1. **`printBackground: true` is required** -- without it, all background colors and images are stripped from the PDF
2. **Tailwind CDN needs `waitUntil: 'networkidle'`** -- Tailwind loads asynchronously via the CDN script; `networkidle` ensures it has finished processing before rendering
3. **Viewport width does NOT affect PDF layout** -- the PDF page dimensions come from the `format` and `margin` options, not the browser viewport
4. **Avoid CSS grid for print layouts** -- grid rendering in print contexts is inconsistent; use flexbox or HTML table layout instead
5. **`@media print` for page breaks** -- `break-before: page` and `break-inside: avoid` only work inside `@media print` or with `printBackground: true`
6. **Base64 images can be large** -- for many charts, save as files and use relative paths instead of embedding all as base64
7. **Font loading** -- if using Google Fonts, add the `<link>` in `<head>` and use `waitUntil: 'networkidle'` to ensure fonts load before capture
8. **Always validate output** -- after rendering, check the PDF file exists and has a reasonable size (> 1KB)

## Validation

```bash
ls -la /path/to/output.pdf
# Verify file exists and size > 0
```
