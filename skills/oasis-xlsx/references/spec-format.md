# XLSX JSON Spec Format

Complete reference for the JSON specification format accepted by `oasis-render xlsx`.

## Top-Level Structure

```json
{
    "sheets": [ ... ]
}
```

## Sheet Object

```json
{
    "name": "Revenue",
    "freeze_panes": "A2",
    "columns": [ ... ],
    "rows": [ ... ],
    "formulas": [ ... ],
    "charts": [ ... ],
    "conditional_formatting": [ ... ]
}
```

## Column Object

```json
{ "header": "Revenue", "width": 15, "format": "$#,##0" }
```

### Common Excel Format Codes

| Format | Code | Example |
|--------|------|---------|
| Currency | `$#,##0` | $1,250 |
| Currency (cents) | `$#,##0.00` | $1,250.00 |
| Percentage | `0.0%` | 29.2% |
| Number (comma) | `#,##0` | 1,250 |
| Date | `yyyy-mm-dd` | 2026-03-27 |
| Date (readable) | `mmmm d, yyyy` | March 27, 2026 |
| Accounting | `_($* #,##0_)` | $ 1,250 |

## Formulas

```json
{ "cell": "D2", "formula": "=B2-C2" }
```

Common formulas:
- Sum: `=SUM(B2:B13)`
- Average: `=AVERAGE(B2:B13)`
- Cross-sheet: `=SUM(Revenue!B2:B13)`
- Percentage: `=D2/B2`

## Charts

```json
{
    "type": "bar",
    "title": "Monthly Revenue",
    "data_range": "A1:C13",
    "position": "G2",
    "size": { "width": 15, "height": 10 }
}
```

| Chart Type | Best For |
|-----------|----------|
| `bar` | Comparing categories |
| `line` | Trends over time |
| `pie` | Parts of a whole (3-7 segments) |
| `doughnut` | Parts of a whole (with center space) |
| `area` | Volume over time |
| `scatter` | Correlation between variables |

## Conditional Formatting

### Color Scale
```json
{
    "range": "E2:E13",
    "type": "color_scale",
    "min_color": "#F87171",
    "max_color": "#34D399"
}
```

### Data Bar
```json
{
    "range": "B2:B13",
    "type": "data_bar",
    "color": "#2D5F8A"
}
```
