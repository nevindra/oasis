# Layout Patterns

Visual hierarchy patterns for effective slide composition with PptxGenJS.

## The Golden Rule

**One idea per slide.** If you have two things to say, make two slides.

## Positioning Grid (16:9 slide = 13.33 x 7.5 inches)

```
+----------------------------------------------+
| 0.5in |      Content Area         | 0.5in   |
|       |                           |         |
| ~1in  |  Title Zone               |         |
|       |  x:0.5 y:0.3 w:12 h:0.8  |         |
|       |                           |         |
| ~1.3in|  [Main Content Zone]      |         |
|       |  x:0.5 y:1.3 w:12 h:5.5  |         |
|       |                           |         |
| ~7in  |  [Footer Zone]            |         |
+----------------------------------------------+
```

## Two-Column Layout

Left: data/chart. Right: insight/text.

```javascript
// Chart on left
slide.addChart(pptx.charts.BAR, data, {
    x: 0.5, y: 1.3, w: 7.5, h: 5,
});

// Insight text on right
slide.addText('Key takeaway here', {
    x: 8.5, y: 2, w: 4, h: 2,
    fontSize: 16, color: '2D5F8A',
});
```

## Three-KPI Row

Across the top, chart below.

```javascript
const kpis = [
    { label: 'Revenue', value: '$2.1M', x: 0.5 },
    { label: 'Growth', value: '+25%', x: 4.7 },
    { label: 'Customers', value: '178', x: 8.9 },
];
for (const k of kpis) {
    slide.addText([
        { text: k.value + '\n', options: { fontSize: 32, bold: true, color: 'E8734A' } },
        { text: k.label, options: { fontSize: 12, color: '6B7280' } },
    ], {
        shape: pptx.shapes.ROUNDED_RECTANGLE,
        x: k.x, y: 1.3, w: 3.8, h: 1.5,
        fill: { color: 'F5F5F5' }, align: 'center', valign: 'middle',
    });
}
// Chart below KPIs
slide.addChart(pptx.charts.BAR, chartData, {
    x: 0.5, y: 3.2, w: 12, h: 4,
});
```

## Full-Width Table

```javascript
slide.addTable(rows, {
    x: 0.5, y: 1.3, w: 12, h: 5.5,
    fontSize: 12, fontFace: 'Inter',
});
```

## Image + Caption

```javascript
slide.addImage({
    path: '/path/to/image.png',
    x: 1.5, y: 1.3, w: 10, h: 4.5,
});
slide.addText('Figure 1: Description', {
    x: 1.5, y: 6, w: 10, h: 0.4,
    fontSize: 12, color: '6B7280', align: 'center',
});
```

## Spacing Guidelines

| Between | Gap |
|---------|-----|
| Title and first element | 0.3-0.5 inches |
| Side-by-side elements | 0.3-0.5 inches |
| Stacked elements | 0.2-0.3 inches |
| Element and slide edge | 0.5 inches minimum |

## Color Usage

- **Primary:** Title text, chart color 1, borders
- **Secondary:** Body text, chart color 2, supporting info
- **Accent:** Highlights, KPI values, chart color 3, call-to-action
- **Light:** Background fills for cards, alternating table rows
- **Muted:** Captions, labels, footnotes
