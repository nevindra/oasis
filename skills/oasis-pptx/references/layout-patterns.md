# Layout Patterns

Visual hierarchy patterns for effective slide composition.

## The Golden Rule

**One idea per slide.** If you have two things to say, make two slides.

## Positioning Grid

Think of the slide as a grid. Common safe zones:

```
+--------------------------------------------+
|  5%  |        Content Area          |  5%  |
|      |                              |      |
| 15%  |  [Main Content Zone]        |      |
|      |  x: 5%, y: 15%              |      |
|      |  w: 90%, h: 75%             |      |
|      |                              |      |
| 90%  |                              |      |
|      |  [Footer Zone: 90-95%]       |      |
+--------------------------------------------+
```

## Two-Column Layout

Left: data/chart. Right: insight/text.

```json
[
    {
        "type": "chart",
        "position": { "x": "5%", "y": "18%", "w": "55%", "h": "70%" }
    },
    {
        "type": "text",
        "position": { "x": "65%", "y": "25%", "w": "30%", "h": "auto" }
    }
]
```

## Three-KPI Row

Across the top, chart below.

```json
[
    { "type": "kpi", "position": { "x": "5%", "y": "15%", "w": "28%", "h": "18%" } },
    { "type": "kpi", "position": { "x": "36%", "y": "15%", "w": "28%", "h": "18%" } },
    { "type": "kpi", "position": { "x": "67%", "y": "15%", "w": "28%", "h": "18%" } },
    { "type": "chart", "position": { "x": "5%", "y": "38%", "w": "90%", "h": "55%" } }
]
```

## Full-Width Table

Table spanning the content area.

```json
[
    {
        "type": "table",
        "position": { "x": "5%", "y": "18%", "w": "90%", "h": "auto" }
    }
]
```

## Image + Caption

Image fills most of the slide, caption below.

```json
[
    {
        "type": "image",
        "position": { "x": "10%", "y": "15%", "w": "80%", "h": "60%" }
    },
    {
        "type": "text",
        "text": "Figure 1: Description",
        "position": { "x": "10%", "y": "78%", "w": "80%", "h": "5%" },
        "fontSize": 12,
        "color": "muted",
        "align": "center"
    }
]
```

## Spacing Guidelines

| Between | Gap |
|---------|-----|
| Title and first element | 3-5% |
| Side-by-side elements | 3-5% |
| Stacked elements | 2-3% |
| Element and slide edge | 5% minimum |

## Color Usage

- **Primary:** Title text, chart color 1, borders
- **Secondary:** Body text, chart color 2, supporting info
- **Accent:** Highlights, KPI values, chart color 3, call-to-action
- **Light:** Background fills for cards, alternating table rows
- **Muted:** Captions, labels, footnotes
