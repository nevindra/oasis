---
name: oasis-pptx
description: Generate PowerPoint presentations using PptxGenJS. Use when the user needs .pptx files with slides, text, charts, images, or professional layouts.
compatibility: Requires node and pptxgenjs package in sandbox
tags: [document, pptx, powerpoint, presentation, export]
tools: [shell, file_write, file_read, execute_code]
references: [oasis-design-system]
---

# PPTX Generation

Generate PowerPoint presentations by writing a Node.js script that uses the PptxGenJS library directly, then executing it in the sandbox.

## Approach

1. Write a Node.js script using `pptxgenjs` to build the presentation
2. Execute the script in the sandbox
3. Validate the output file exists and has reasonable size

## Basic Structure

```javascript
const PptxGenJS = require('pptxgenjs');
const pptx = new PptxGenJS();

// Presentation metadata
pptx.author = 'Oasis';
pptx.subject = 'Quarterly Report';

// Slide dimensions (default 10x7.5 inches -- standard 4:3)
// For widescreen 16:9:
pptx.defineLayout({ name: 'WIDE', width: 13.33, height: 7.5 });
pptx.layout = 'WIDE';

// Add slides
const slide = pptx.addSlide();
slide.addText('Hello World', { x: 1, y: 1, w: 8, h: 1, fontSize: 24 });

// Save
pptx.writeFile({ fileName: '/path/to/output.pptx' })
    .then(() => console.log('Presentation saved'))
    .catch(err => console.error(err));
```

## Text

All positions use `x`, `y`, `w`, `h` in **inches**.

```javascript
// Simple text
slide.addText('Title Text', {
    x: 0.5, y: 0.5, w: 9, h: 1,
    fontSize: 36,
    fontFace: 'Inter',
    color: '1B2A4A',  // No # prefix
    bold: true,
    align: 'center',
    valign: 'middle',
});

// Text with multiple styled segments
slide.addText([
    { text: 'Bold part ', options: { bold: true, color: '1B2A4A' } },
    { text: 'and normal part', options: { color: '333333' } },
], {
    x: 0.5, y: 2, w: 9, h: 0.5,
    fontSize: 14,
    fontFace: 'Inter',
});

// Bullet list
slide.addText([
    { text: 'First point', options: { bullet: true } },
    { text: 'Second point', options: { bullet: true } },
    { text: 'Third point', options: { bullet: true } },
], {
    x: 0.5, y: 2, w: 8, h: 3,
    fontSize: 16,
    fontFace: 'Inter',
    color: '333333',
    lineSpacing: 28,
});

// Numbered list
slide.addText([
    { text: 'Step one', options: { bullet: { type: 'number' } } },
    { text: 'Step two', options: { bullet: { type: 'number' } } },
], {
    x: 0.5, y: 2, w: 8, h: 2,
    fontSize: 16,
});
```

### Text Options

| Option | Type | Description |
|--------|------|-------------|
| `x, y, w, h` | number | Position and size in inches |
| `fontSize` | number | Font size in points |
| `fontFace` | string | Font family name |
| `color` | string | Hex color without `#` |
| `bold` | bool | Bold text |
| `italic` | bool | Italic text |
| `underline` | bool | Underline text |
| `align` | string | `left`, `center`, `right`, `justify` |
| `valign` | string | `top`, `middle`, `bottom` |
| `lineSpacing` | number | Line spacing in points |
| `fill` | object | Background `{color: 'F5F5F5'}` |
| `shadow` | object | `{type: 'outer', blur: 3, offset: 2, color: '000000', opacity: 0.3}` |
| `rotate` | number | Rotation in degrees |

## Charts

```javascript
// Bar chart
slide.addChart(pptx.charts.BAR, [
    { name: '2025', labels: ['Q1','Q2','Q3','Q4'], values: [1.2, 1.4, 1.5, 1.8] },
    { name: '2026', labels: ['Q1','Q2','Q3','Q4'], values: [1.9, 2.1, 2.3, 2.6] },
], {
    x: 0.5, y: 1.5, w: 6, h: 4,
    showTitle: true,
    title: 'Revenue by Quarter ($M)',
    titleColor: '1B2A4A',
    titleFontSize: 14,
    showValue: false,
    showLegend: true,
    legendPos: 'b',
    chartColors: ['2D5F8A', 'E8734A'],  // No # prefix
    valAxisTitle: 'Revenue ($M)',
    catAxisTitle: 'Quarter',
});

// Line chart
slide.addChart(pptx.charts.LINE, [
    { name: 'Users', labels: ['Jan','Feb','Mar','Apr','May','Jun'],
      values: [100, 150, 200, 280, 350, 420] },
], {
    x: 0.5, y: 1.5, w: 6, h: 4,
    showTitle: true,
    title: 'User Growth',
    lineSmooth: true,
    showMarker: true,
    chartColors: ['1B2A4A'],
});

// Pie chart
slide.addChart(pptx.charts.PIE, [
    { name: 'Revenue', labels: ['Product','Services','Support'],
      values: [60, 25, 15] },
], {
    x: 0.5, y: 1.5, w: 5, h: 4,
    showTitle: true,
    title: 'Revenue Breakdown',
    showPercent: true,
    showLegend: true,
    chartColors: ['1B2A4A', '2D5F8A', 'E8734A'],
});

// Doughnut chart
slide.addChart(pptx.charts.DOUGHNUT, [
    { name: 'Split', labels: ['A','B','C'], values: [40, 35, 25] },
], {
    x: 0.5, y: 1.5, w: 5, h: 4,
    chartColors: ['1B2A4A', '2D5F8A', 'E8734A'],
});
```

### Available Chart Types

| Type | Constant | Best For |
|------|----------|----------|
| Bar | `pptx.charts.BAR` | Comparing categories |
| Line | `pptx.charts.LINE` | Trends over time |
| Pie | `pptx.charts.PIE` | Parts of a whole |
| Doughnut | `pptx.charts.DOUGHNUT` | Parts of a whole (with center) |
| Area | `pptx.charts.AREA` | Volume over time |
| Scatter | `pptx.charts.SCATTER` | Correlation |

### Chart Data Format

Every chart type uses an array of objects with `name`, `labels`, and `values`:

```javascript
[
    { name: 'Series 1', labels: ['A', 'B', 'C'], values: [10, 20, 30] },
    { name: 'Series 2', labels: ['A', 'B', 'C'], values: [15, 25, 35] },
]
```

Pie/Doughnut charts use a single series.

## Images

```javascript
// From file path
slide.addImage({
    path: '/path/to/image.png',
    x: 1, y: 1.5, w: 4, h: 3,
});

// From base64 data
slide.addImage({
    data: 'data:image/png;base64,iVBOR...',
    x: 1, y: 1.5, w: 4, h: 3,
});

// Sizing options
slide.addImage({
    path: '/path/to/image.png',
    x: 0.5, y: 1, w: 9, h: 5,
    sizing: { type: 'contain', w: 9, h: 5 },  // or 'cover'
});
```

## Tables

```javascript
// Table rows: array of arrays, each inner array is a row of cells
const rows = [
    // Header row
    [
        { text: 'Metric', options: { bold: true, color: 'FFFFFF', fill: { color: '1B2A4A' } } },
        { text: 'Value', options: { bold: true, color: 'FFFFFF', fill: { color: '1B2A4A' } } },
        { text: 'Change', options: { bold: true, color: 'FFFFFF', fill: { color: '1B2A4A' } } },
    ],
    // Data rows
    [{ text: 'Revenue' }, { text: '$2.1M' }, { text: '+25%' }],
    [{ text: 'Customers' }, { text: '178' }, { text: '+23%' }],
    [{ text: 'Margin' }, { text: '42%' }, { text: '+7pp' }],
];

slide.addTable(rows, {
    x: 0.5, y: 1.5, w: 9, h: 3,
    fontSize: 12,
    fontFace: 'Inter',
    border: { type: 'solid', pt: 0.5, color: 'E5E7EB' },
    colW: [3, 3, 3],  // Column widths in inches
    rowH: [0.5, 0.4, 0.4, 0.4],  // Row heights in inches
    align: 'center',
    valign: 'middle',
});
```

## Shapes

```javascript
slide.addShape(pptx.shapes.ROUNDED_RECTANGLE, {
    x: 0.5, y: 0.5, w: 3, h: 1.5,
    fill: { color: 'F5F5F5' },
    shadow: { type: 'outer', blur: 3, offset: 2, color: '000000', opacity: 0.2 },
});

// Shape with text
slide.addText('KPI Card', {
    shape: pptx.shapes.ROUNDED_RECTANGLE,
    x: 0.5, y: 0.5, w: 3, h: 1.5,
    fill: { color: 'F5F5F5' },
    fontSize: 14,
    color: '1B2A4A',
    align: 'center',
    valign: 'middle',
});
```

## Slide Backgrounds

```javascript
// Solid color background
slide.background = { color: '0F172A' };

// Image background
slide.background = { path: '/path/to/bg.png' };
```

## Master Slides

Define reusable layouts for consistent branding across slides.

```javascript
pptx.defineSlideMaster({
    title: 'BRANDED',
    background: { color: 'FFFFFF' },
    objects: [
        // Top bar
        { rect: { x: 0, y: 0, w: '100%', h: 0.5, fill: { color: '1B2A4A' } } },
        // Footer bar
        { rect: { x: 0, y: 7, w: '100%', h: 0.5, fill: { color: 'F5F5F5' } } },
        // Company name in footer
        { text: {
            text: 'Acme Corp',
            options: { x: 0.5, y: 7.05, w: 3, h: 0.4, fontSize: 10, color: '6B7280' }
        }},
        // Page number placeholder
        { text: {
            text: '',
            options: { x: 11, y: 7.05, w: 2, h: 0.4, fontSize: 10, color: '6B7280', align: 'right' }
        }},
    ],
    slideNumber: { x: 11.5, y: 7.05, w: 1, h: 0.4, fontSize: 10, color: '6B7280' },
});

// Use the master slide
const slide = pptx.addSlide({ masterName: 'BRANDED' });
```

## Complete Example: Quarterly Review Deck

```javascript
const PptxGenJS = require('pptxgenjs');
const pptx = new PptxGenJS();

// Widescreen 16:9
pptx.defineLayout({ name: 'WIDE', width: 13.33, height: 7.5 });
pptx.layout = 'WIDE';

// Theme colors from oasis-design-system Corporate palette
const C = {
    primary: '1B2A4A',
    secondary: '2D5F8A',
    accent: 'E8734A',
    light: 'F5F5F5',
    bg: 'FFFFFF',
    text: '333333',
    muted: '6B7280',
};

// --- Cover Slide ---
const cover = pptx.addSlide();
cover.background = { color: C.primary };
cover.addText('Q4 Business Review', {
    x: 1, y: 2, w: 11, h: 1.5,
    fontSize: 40, fontFace: 'Inter', color: 'FFFFFF', bold: true, align: 'center',
});
cover.addText('Prepared for Board of Directors', {
    x: 1, y: 3.8, w: 11, h: 0.8,
    fontSize: 18, fontFace: 'Inter', color: '94A3B8', align: 'center',
});
cover.addText('March 2026', {
    x: 1, y: 5.5, w: 11, h: 0.5,
    fontSize: 14, fontFace: 'Inter', color: '64748B', align: 'center',
});

// --- Agenda Slide ---
const agenda = pptx.addSlide();
agenda.addText('Agenda', {
    x: 0.5, y: 0.3, w: 12, h: 0.8,
    fontSize: 28, fontFace: 'Inter', color: C.primary, bold: true,
});
agenda.addText([
    { text: '1. Financial Overview', options: { bullet: true, breakLine: true } },
    { text: '2. Product Updates', options: { bullet: true, breakLine: true } },
    { text: '3. Growth Strategy', options: { bullet: true, breakLine: true } },
    { text: '4. Q&A', options: { bullet: true } },
], {
    x: 1.5, y: 1.5, w: 10, h: 4,
    fontSize: 20, fontFace: 'Inter', color: C.text, lineSpacing: 36,
});

// --- Section Divider ---
const sectionSlide = pptx.addSlide();
sectionSlide.background = { color: C.light };
sectionSlide.addText('Financial Overview', {
    x: 1, y: 2.5, w: 11, h: 1.5,
    fontSize: 36, fontFace: 'Inter', color: C.primary, bold: true, align: 'center',
});
sectionSlide.addText('Revenue, margins, and growth metrics', {
    x: 1, y: 4, w: 11, h: 0.8,
    fontSize: 16, fontFace: 'Inter', color: C.muted, align: 'center',
});

// --- Data Slide: Chart + Insight ---
const dataSlide = pptx.addSlide();
dataSlide.addText('Revenue Growth', {
    x: 0.5, y: 0.3, w: 12, h: 0.8,
    fontSize: 28, fontFace: 'Inter', color: C.primary, bold: true,
});

// Chart on the left
dataSlide.addChart(pptx.charts.BAR, [
    { name: '2025', labels: ['Q1','Q2','Q3','Q4'], values: [1.2, 1.4, 1.5, 1.8] },
    { name: '2026', labels: ['Q1','Q2','Q3','Q4'], values: [1.9, 2.1, 2.3, 2.6] },
], {
    x: 0.5, y: 1.3, w: 7.5, h: 5,
    showTitle: false,
    showLegend: true,
    legendPos: 'b',
    chartColors: [C.secondary, C.accent],
    valAxisTitle: 'Revenue ($M)',
});

// Insight text on the right
dataSlide.addText('Revenue grew 25% YoY\ndriven by enterprise expansion', {
    x: 8.5, y: 2, w: 4, h: 2,
    fontSize: 16, fontFace: 'Inter', color: C.secondary, lineSpacing: 24,
});

// KPI card
dataSlide.addText([
    { text: '+25%\n', options: { fontSize: 36, bold: true, color: C.accent } },
    { text: 'YoY Growth', options: { fontSize: 12, color: C.muted } },
], {
    shape: pptx.shapes.ROUNDED_RECTANGLE,
    x: 8.5, y: 4.5, w: 4, h: 1.8,
    fill: { color: C.light },
    align: 'center',
    valign: 'middle',
});

// --- Summary Slide ---
const summary = pptx.addSlide();
summary.addText('Key Takeaways', {
    x: 0.5, y: 0.3, w: 12, h: 0.8,
    fontSize: 28, fontFace: 'Inter', color: C.primary, bold: true,
});
summary.addText([
    { text: 'Revenue exceeded targets by 12%', options: { bullet: true, breakLine: true } },
    { text: 'Enterprise segment grew 40%', options: { bullet: true, breakLine: true } },
    { text: 'Q1 2026 pipeline is 2.3x target', options: { bullet: true } },
], {
    x: 1.5, y: 1.8, w: 10, h: 4,
    fontSize: 20, fontFace: 'Inter', color: C.text, lineSpacing: 36,
});

pptx.writeFile({ fileName: '/path/to/presentation.pptx' })
    .then(() => console.log('Presentation saved'))
    .catch(err => console.error(err));
```

## Design Rules

1. **One idea per slide** -- do not cram multiple charts or concepts onto one slide
2. **6 slides maximum** for a focused deck. Expand only if the user explicitly needs more.
3. **All measurements in inches** -- x, y, w, h, colW, rowH are all in inches
4. **Chart + insight pattern** -- when showing a chart, pair it with a text element that states the key takeaway
5. **Consistent colors** -- pick a palette from oasis-design-system and use it throughout

## Gotchas

1. **All measurements are in inches** -- there is no pixel or percentage mode. Standard slide is 13.33 x 7.5 inches (16:9) or 10 x 7.5 (4:3).
2. **Colors have no `#` prefix** -- use `'1B2A4A'` not `'#1B2A4A'`. This applies to all color properties: text, fill, chart, border.
3. **Chart data format** -- always an array of objects with `name`, `labels`, `values`. Even single-series charts use this format.
4. **`writeFile` is async** -- always `await` it or use `.then()`. The script will exit before writing if you forget.
5. **Bullet lists use text arrays** -- each item is an object with `text` and `options: { bullet: true }`. Add `breakLine: true` to separate items.
6. **Table cells are objects** -- use `{ text: 'value', options: {...} }` for styled cells, not plain strings (plain strings work but cannot be styled per-cell).
7. **Image paths must be absolute** or relative to the script's working directory.
8. **Master slide objects use different syntax** -- shapes in `defineSlideMaster` use `rect`, `text`, `image` keys, not `addShape`/`addText`.
9. **Always validate output** -- check the file exists and has reasonable size after writing.

## Validation

```bash
ls -la /path/to/output.pptx
node -e "const fs = require('fs'); const s = fs.statSync('/path/to/output.pptx'); console.log(s.size + ' bytes')"
```
