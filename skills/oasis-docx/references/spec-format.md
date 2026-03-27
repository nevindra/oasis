# DOCX JSON Spec Format

Complete reference for the JSON specification format accepted by `oasis-render docx`.

## Top-Level Fields

```json
{
    "style": "business",
    "page": { ... },
    "content": [ ... ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `style` | string | no | Style preset (business, academic, minimal, formal) |
| `page` | object | no | Page setup (size, margins) |
| `content` | array | yes | Content blocks in document order |

## Page Object

```json
{
    "size": "A4",
    "margins": { "top": 1, "bottom": 1, "left": 1.25, "right": 1.25 }
}
```

All margin values are in inches.

## Content Blocks

### heading
```json
{ "type": "heading", "level": 2, "text": "Section Title" }
```

### paragraph
```json
{ "type": "paragraph", "text": "Body text here.", "bold": false, "italic": false, "align": "left" }
```
`align`: left, center, right, justify

### table
```json
{
    "type": "table",
    "headers": ["Name", "Value", "Change"],
    "rows": [
        ["Revenue", "$2.1M", "+25%"],
        ["Customers", "178", "+23%"]
    ],
    "caption": "Table 1: Key Metrics"
}
```

### image
```json
{ "type": "image", "path": "/path/to/chart.png", "width": 5, "caption": "Figure 1: Revenue" }
```
`width` is in inches. Use absolute paths.

### list
```json
{ "type": "list", "ordered": true, "items": ["First item", "Second item"] }
```

### page_break
```json
{ "type": "page_break" }
```

### toc
```json
{ "type": "toc", "depth": 3 }
```
Inserts a TOC field code. User must update fields in Word to populate.

### code
```json
{ "type": "code", "text": "func main() {\n    fmt.Println(\"hello\")\n}" }
```

### quote
```json
{ "type": "quote", "text": "To be or not to be.", "author": "Shakespeare" }
```

### hr
```json
{ "type": "hr" }
```
