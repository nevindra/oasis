# PPTX JSON Spec Format

Complete reference for the JSON specification format accepted by `oasis-render pptx`.

## Top-Level Structure

```json
{
    "theme": { ... },
    "slides": [ ... ]
}
```

## Theme Object

```json
{
    "primary": "#1B2A4A",
    "secondary": "#2D5F8A",
    "accent": "#E8734A",
    "light": "#F5F5F5",
    "bg": "#FFFFFF",
    "fontFace": "Inter"
}
```

All colors must include `#` prefix. `fontFace` defaults to "Inter" if omitted.

## Slide Object

Every slide has a `layout` field plus layout-specific fields.

### cover
```json
{
    "layout": "cover",
    "title": "Presentation Title",
    "subtitle": "Optional subtitle",
    "date": "March 2026"
}
```

### toc
```json
{
    "layout": "toc",
    "title": "Agenda",
    "items": ["Topic 1", "Topic 2", "Topic 3"]
}
```

### section
```json
{
    "layout": "section",
    "title": "Section Name",
    "subtitle": "Optional description"
}
```

### content
```json
{
    "layout": "content",
    "title": "Slide Title",
    "elements": [ ... ]
}
```

### summary
```json
{
    "layout": "summary",
    "title": "Key Takeaways",
    "bullets": ["Point 1", "Point 2", "Point 3"]
}
```

## Element Types

### text
```json
{
    "type": "text",
    "text": "Content here",
    "position": { "x": "10%", "y": "20%", "w": "80%", "h": "10%" },
    "fontSize": 16,
    "color": "secondary",
    "bold": true,
    "align": "center"
}
```

### chart
```json
{
    "type": "chart",
    "chartType": "bar",
    "data": {
        "labels": ["Q1", "Q2", "Q3", "Q4"],
        "series": [
            { "name": "Revenue", "values": [1.2, 1.5, 1.8, 2.1] }
        ]
    },
    "position": { "x": "5%", "y": "20%", "w": "55%", "h": "70%" },
    "title": "Quarterly Revenue",
    "showValue": false,
    "showLegend": true
}
```

### table
```json
{
    "type": "table",
    "headers": ["Metric", "Value", "Change"],
    "rows": [
        ["ARR", "$22.5M", "+25%"],
        ["NRR", "122%", "+4pp"]
    ],
    "position": { "x": "5%", "y": "20%", "w": "90%", "h": "35%" },
    "fontSize": 12
}
```

### image
```json
{
    "type": "image",
    "path": "/path/to/image.png",
    "position": { "x": "10%", "y": "15%", "w": "80%", "h": "70%" },
    "sizing": "contain"
}
```

### shape
```json
{
    "type": "shape",
    "shapeType": "roundRect",
    "position": { "x": "10%", "y": "10%", "w": "20%", "h": "20%" },
    "fill": "primary",
    "text": "Label",
    "shadow": true
}
```

### kpi
```json
{
    "type": "kpi",
    "label": "YoY Growth",
    "value": "+25%",
    "position": { "x": "65%", "y": "25%", "w": "30%", "h": "20%" },
    "valueSize": 36,
    "labelSize": 12,
    "color": "accent"
}
```

## Position Object

All values are **percentage strings**. This ensures slides scale correctly on any screen.

```json
{ "x": "5%", "y": "15%", "w": "90%", "h": "70%" }
```

| Field | Description |
|-------|-------------|
| `x` | Left edge (% from slide left) |
| `y` | Top edge (% from slide top) |
| `w` | Width (% of slide width) |
| `h` | Height (% of slide height) |
