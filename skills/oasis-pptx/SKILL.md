---
name: oasis-pptx
description: Generate PowerPoint presentations (PPTX) from a JSON specification. Use when the user asks to create, generate, or export a presentation — pitch decks, quarterly reviews, training materials, or any slide-based content.
tags: [document, pptx, powerpoint, presentation, export]
tools: [shell, file_write, file_read]
references: [oasis-design-system]
---

# PPTX Generation

Generate presentations by writing a JSON specification with slides and a theme, then rendering via PptxGenJS.

## Route Table

| Route | Trigger | Pipeline |
|-------|---------|----------|
| CREATE | "create/make/generate a presentation/deck/slides" | Write JSON spec -> `oasis-render pptx` |

## CREATE Route

### Step 1: Write JSON Spec

```json
{
    "theme": {
        "primary": "#1B2A4A",
        "secondary": "#2D5F8A",
        "accent": "#E8734A",
        "light": "#F5F5F5",
        "bg": "#FFFFFF",
        "fontFace": "Inter"
    },
    "slides": [
        {
            "layout": "cover",
            "title": "Q4 Business Review",
            "subtitle": "Prepared for Board of Directors",
            "date": "March 2026"
        },
        {
            "layout": "toc",
            "title": "Agenda",
            "items": ["Financial Overview", "Product Updates", "Roadmap", "Q&A"]
        },
        {
            "layout": "section",
            "title": "Financial Overview",
            "subtitle": "Revenue, margins, and growth metrics"
        },
        {
            "layout": "content",
            "title": "Revenue Growth",
            "elements": [
                {
                    "type": "chart",
                    "chartType": "bar",
                    "data": {
                        "labels": ["Q1", "Q2", "Q3", "Q4"],
                        "series": [
                            { "name": "2025", "values": [1.2, 1.4, 1.5, 1.8] },
                            { "name": "2026", "values": [1.9, 2.1, 2.3, null] }
                        ]
                    },
                    "position": { "x": "5%", "y": "20%", "w": "55%", "h": "70%" }
                },
                {
                    "type": "text",
                    "text": "Revenue grew 25% YoY driven by enterprise expansion",
                    "position": { "x": "65%", "y": "25%", "w": "30%" },
                    "fontSize": 16,
                    "color": "secondary"
                }
            ]
        },
        {
            "layout": "summary",
            "title": "Key Takeaways",
            "bullets": [
                "Revenue exceeded targets by 12%",
                "Enterprise segment grew 40%",
                "Q1 2026 pipeline is 2.3x target"
            ]
        }
    ]
}
```

### Theme Object

The theme is passed to every slide. All color references in elements use theme token names.

| Field | Required | Description |
|-------|----------|-------------|
| `primary` | yes | Headings, primary elements, chart color 1 |
| `secondary` | yes | Subheadings, supporting elements, chart color 2 |
| `accent` | yes | Highlights, call-to-action, chart color 3 |
| `light` | yes | Backgrounds, alternating rows |
| `bg` | yes | Slide background |
| `fontFace` | no | Font family (default: Inter) |

### Slide Layouts

| Layout | Purpose | Required Fields |
|--------|---------|----------------|
| `cover` | Title slide | `title`, optional `subtitle`, `date` |
| `toc` | Table of contents | `title`, `items` (string array) |
| `section` | Section divider | `title`, optional `subtitle` |
| `content` | Main content | `title`, `elements` (array) |
| `summary` | Closing / takeaways | `title`, `bullets` (string array) |

### Element Types

All positions use **percentage-based coordinates** -- no pixel/inch math needed.

| Type | Required Fields | Optional Fields |
|------|----------------|-----------------|
| `text` | `text`, `position` | `fontSize`, `fontFace`, `color` (theme token), `bold`, `italic`, `align` |
| `chart` | `chartType`, `data`, `position` | `title`, `showValue`, `showLegend` |
| `table` | `headers`, `rows`, `position` | `headerColor` (theme token), `fontSize` |
| `image` | `path`, `position` | `sizing` (`cover`, `contain`) |
| `shape` | `shapeType`, `position` | `fill` (theme token), `text`, `shadow` |
| `kpi` | `label`, `value`, `position` | `valueSize` (default 36), `labelSize` (default 12), `color` |

### Chart Types

| chartType | Data Format |
|-----------|-------------|
| `bar` | labels + series with values |
| `line` | labels + series with values |
| `pie` | labels + single series |
| `doughnut` | labels + single series |
| `area` | labels + series with values |
| `scatter` | series with `[x, y]` value pairs |

### Step 2: Render

```bash
oasis-render pptx spec.json presentation.pptx
```

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `--theme` | none | Theme JSON file override (overrides spec theme) |

### Design Rules

1. **6 slides maximum** for a focused deck. Expand only if the user explicitly requests more.
2. **One idea per slide.** Don't cram multiple charts or concepts.
3. **Percentage positions only.** Never use absolute inches/pixels -- percentages scale to any screen.
4. **Chart + insight pattern.** When showing a chart, pair it with a text element that states the key takeaway.
5. **Consistent colors.** Always use theme token names (`primary`, `accent`), never raw hex in elements.
