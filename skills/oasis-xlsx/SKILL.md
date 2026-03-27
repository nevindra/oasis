---
name: oasis-xlsx
description: Generate Excel spreadsheets (XLSX) from a JSON specification. Use when the user asks to create, generate, or export a spreadsheet — financial reports, data exports, dashboards, budgets, or any tabular data with charts.
tags: [document, xlsx, excel, spreadsheet, export]
tools: [shell, file_write, file_read]
references: [oasis-design-system]
---

# XLSX Generation

Generate Excel spreadsheets by writing a JSON specification, then rendering via openpyxl.

## Route Table

| Route | Trigger | Pipeline |
|-------|---------|----------|
| CREATE | "create/generate/make a spreadsheet" | Write JSON spec -> `oasis-render xlsx` |
| READ | "read/analyze this Excel file" | `execute_code` with openpyxl/pandas -> agent analyzes |
| EDIT | "modify/update this Excel" | Read -> modify spec -> `oasis-render xlsx` |

## CREATE Route

### Step 1: Write JSON Spec

```json
{
    "sheets": [
        {
            "name": "Revenue",
            "freeze_panes": "A2",
            "columns": [
                { "header": "Month", "width": 15 },
                { "header": "Revenue", "width": 15, "format": "$#,##0" },
                { "header": "Expenses", "width": 15, "format": "$#,##0" },
                { "header": "Profit", "width": 15, "format": "$#,##0" },
                { "header": "Margin", "width": 12, "format": "0.0%" }
            ],
            "rows": [
                ["Jan", 120000, 85000, 35000, 0.292],
                ["Feb", 135000, 90000, 45000, 0.333],
                ["Mar", 128000, 82000, 46000, 0.359]
            ],
            "formulas": [
                { "cell": "D2", "formula": "=B2-C2" },
                { "cell": "E2", "formula": "=D2/B2" }
            ],
            "charts": [
                {
                    "type": "bar",
                    "title": "Monthly Revenue vs Expenses",
                    "data_range": "A1:C13",
                    "position": "G2",
                    "size": { "width": 15, "height": 10 }
                }
            ],
            "conditional_formatting": [
                {
                    "range": "E2:E13",
                    "type": "color_scale",
                    "min_color": "#F87171",
                    "max_color": "#34D399"
                }
            ]
        }
    ]
}
```

### Spec Reference

#### Sheet Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Sheet tab name |
| `columns` | array | yes | Column definitions |
| `rows` | array | yes | Row data (2D array) |
| `formulas` | array | no | Cell formulas |
| `charts` | array | no | Chart configurations |
| `freeze_panes` | string | no | Cell reference for freeze (e.g., "A2") |
| `conditional_formatting` | array | no | Conditional format rules |

#### Column Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `header` | string | yes | Column header text |
| `width` | number | no | Column width in characters |
| `format` | string | no | Number format (Excel format codes) |

#### Chart Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | bar, line, pie, scatter, area, doughnut |
| `title` | string | no | Chart title |
| `data_range` | string | yes | Data range (e.g., "A1:C13") |
| `position` | string | yes | Top-left cell for chart placement |
| `size` | object | no | `{width, height}` in chart units |

### Step 2: Render

```bash
oasis-render xlsx spec.json report.xlsx
```

### Conventions

1. **Financial coloring:** Blue (#0000FF) for hard-coded inputs, black for formulas, green (#006100) for cross-sheet references.
2. **Header row:** Always bold, with bottom border, frozen.
3. **Number formats:** Use Excel format codes -- `$#,##0` for currency, `0.0%` for percentages, `yyyy-mm-dd` for dates.
4. **Sheet naming:** Short, descriptive, no special characters.

## READ Route

For reading existing Excel files, use `execute_code` with pandas:

```python
import pandas as pd
df = pd.read_excel("data.xlsx", sheet_name="Sheet1")
print(df.describe())
print(df.head(20))
```

Do not use `oasis-render` for reading -- use pandas directly via `execute_code`.
