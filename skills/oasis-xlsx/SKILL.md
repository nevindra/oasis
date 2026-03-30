---
name: oasis-xlsx
description: Generate Excel spreadsheets using openpyxl. Use when the user needs .xlsx files with data, formulas, charts, conditional formatting, or styled worksheets.
compatibility: Requires python3 and openpyxl package in sandbox
tags: [document, xlsx, excel, spreadsheet, export]
tools: [shell, file_write, file_read, execute_code]
references: [oasis-design-system]
---

# XLSX Generation

Generate Excel spreadsheets by writing a Python script that uses the openpyxl library directly, then executing it in the sandbox.

## Approach

1. Write a Python script using `openpyxl` to build the workbook
2. Execute the script in the sandbox
3. Validate the output file exists and has reasonable size

## Basic Structure

```python
from openpyxl import Workbook

wb = Workbook()
ws = wb.active
ws.title = "Revenue"

# Add headers
ws.append(["Month", "Revenue", "Expenses", "Profit"])

# Add data rows
data = [
    ["Jan", 120000, 85000, 35000],
    ["Feb", 135000, 90000, 45000],
    ["Mar", 128000, 82000, 46000],
]
for row in data:
    ws.append(row)

wb.save("/path/to/output.xlsx")
```

## Cell Access

```python
# By cell reference (1-indexed)
ws['A1'] = 'Header'
ws['B2'] = 42

# By row and column numbers (1-indexed)
ws.cell(row=1, column=1, value='Header')
ws.cell(row=2, column=2, value=42)

# Read a cell value
val = ws['B2'].value

# Iterate rows
for row in ws.iter_rows(min_row=2, max_row=10, min_col=1, max_col=4, values_only=True):
    print(row)
```

## Formulas

Place Excel formula strings directly in cells. They calculate when opened in Excel.

```python
# Simple formulas
ws['D2'] = '=B2-C2'       # Profit = Revenue - Expenses
ws['E2'] = '=D2/B2'       # Margin = Profit / Revenue

# Aggregation formulas
ws['B15'] = '=SUM(B2:B13)'
ws['B16'] = '=AVERAGE(B2:B13)'
ws['B17'] = '=MAX(B2:B13)'

# Cross-sheet reference
ws['A1'] = "=Revenue!B15"

# VLOOKUP
ws['C2'] = '=VLOOKUP(A2,Data!A:C,3,FALSE)'

# Conditional
ws['F2'] = '=IF(E2>0.3,"High","Low")'
```

## Styling

```python
from openpyxl.styles import Font, PatternFill, Border, Side, Alignment

# Font
ws['A1'].font = Font(
    name='Calibri',
    size=12,
    bold=True,
    italic=False,
    color='FFFFFF'  # No # prefix for openpyxl colors
)

# Fill (background color)
ws['A1'].fill = PatternFill(
    start_color='1B2A4A',
    end_color='1B2A4A',
    fill_type='solid'
)

# Border
thin_border = Border(
    left=Side(style='thin', color='000000'),
    right=Side(style='thin', color='000000'),
    top=Side(style='thin', color='000000'),
    bottom=Side(style='thin', color='000000'),
)
ws['A1'].border = thin_border

# Alignment
ws['A1'].alignment = Alignment(
    horizontal='center',
    vertical='center',
    wrap_text=True
)

# Number format
ws['B2'].number_format = '$#,##0'
ws['E2'].number_format = '0.0%'
ws['F2'].number_format = 'yyyy-mm-dd'
```

### Common Number Formats

| Format | Code | Example |
|--------|------|---------|
| Currency | `$#,##0` | $1,250 |
| Currency (cents) | `$#,##0.00` | $1,250.00 |
| Percentage | `0.0%` | 29.2% |
| Integer with commas | `#,##0` | 1,250 |
| Date | `yyyy-mm-dd` | 2026-03-27 |
| Date (readable) | `mmmm d, yyyy` | March 27, 2026 |
| Accounting | `_($* #,##0.00_)` | $ 1,250.00 |

## Column and Row Sizing

```python
# Column width (in character units, roughly 7 pixels per unit)
ws.column_dimensions['A'].width = 20
ws.column_dimensions['B'].width = 15

# Row height (in points)
ws.row_dimensions[1].height = 30

# Auto-fit approximation (openpyxl has no true auto-fit)
for col in ws.columns:
    max_len = max(len(str(cell.value or '')) for cell in col)
    ws.column_dimensions[col[0].column_letter].width = max_len + 2
```

## Freeze Panes

```python
# Freeze header row (scroll data, headers stay visible)
ws.freeze_panes = 'A2'

# Freeze first column and header row
ws.freeze_panes = 'B2'
```

## Charts

```python
from openpyxl.chart import BarChart, LineChart, PieChart, Reference

# Bar Chart
chart = BarChart()
chart.title = "Monthly Revenue vs Expenses"
chart.x_axis.title = "Month"
chart.y_axis.title = "Amount ($)"
chart.style = 10

# Data references (min_col, min_row, max_col, max_row are 1-indexed)
cats = Reference(ws, min_col=1, min_row=2, max_row=13)       # Category labels
data = Reference(ws, min_col=2, min_row=1, max_row=13, max_col=3)  # Data with headers

chart.add_data(data, titles_from_data=True)
chart.set_categories(cats)
chart.shape = 4
ws.add_chart(chart, "F2")  # Position: top-left cell

# Line Chart
line = LineChart()
line.title = "Trend"
line.add_data(data, titles_from_data=True)
line.set_categories(cats)
ws.add_chart(line, "F18")

# Pie Chart
pie = PieChart()
pie.title = "Revenue Split"
pie_data = Reference(ws, min_col=2, min_row=1, max_row=5)
pie_cats = Reference(ws, min_col=1, min_row=2, max_row=5)
pie.add_data(pie_data, titles_from_data=True)
pie.set_categories(pie_cats)
ws.add_chart(pie, "F34")
```

### Chart Sizing

```python
from openpyxl.chart import BarChart

chart = BarChart()
chart.width = 20   # Width in chart units (approx cm)
chart.height = 12  # Height in chart units (approx cm)
```

## Conditional Formatting

```python
from openpyxl.formatting.rule import ColorScaleRule, CellIsRule, FormulaRule
from openpyxl.styles import PatternFill, Font

# Color scale (gradient from red to green)
ws.conditional_formatting.add(
    'E2:E13',
    ColorScaleRule(
        start_type='min', start_color='F87171',
        end_type='max', end_color='34D399'
    )
)

# Cell value rule (highlight cells > 100000)
ws.conditional_formatting.add(
    'B2:B13',
    CellIsRule(
        operator='greaterThan',
        formula=['100000'],
        fill=PatternFill(bgColor='C6EFCE'),
        font=Font(color='006100')
    )
)

# Formula-based rule (highlight negative profit)
ws.conditional_formatting.add(
    'D2:D13',
    FormulaRule(
        formula=['$D2<0'],
        fill=PatternFill(bgColor='FFC7CE'),
        font=Font(color='9C0006')
    )
)
```

## Multiple Sheets

```python
wb = Workbook()

# First sheet (created by default)
summary = wb.active
summary.title = "Summary"

# Additional sheets
revenue = wb.create_sheet("Revenue")
expenses = wb.create_sheet("Expenses")

# Sheet tab color
revenue.sheet_properties.tabColor = "2D5F8A"
expenses.sheet_properties.tabColor = "E8734A"
```

## Complete Example: Financial Report

```python
from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill, Border, Side, Alignment
from openpyxl.chart import BarChart, Reference
from openpyxl.formatting.rule import ColorScaleRule

wb = Workbook()
ws = wb.active
ws.title = "Revenue"

# Colors from oasis-design-system Corporate palette
HDR_FILL = PatternFill(start_color='1B2A4A', end_color='1B2A4A', fill_type='solid')
HDR_FONT = Font(name='Calibri', size=11, bold=True, color='FFFFFF')
DATA_FONT = Font(name='Calibri', size=11)
BORDER = Border(bottom=Side(style='thin', color='E5E7EB'))

# Headers
headers = ['Month', 'Revenue', 'Expenses', 'Profit', 'Margin']
for col, header in enumerate(headers, 1):
    cell = ws.cell(row=1, column=col, value=header)
    cell.font = HDR_FONT
    cell.fill = HDR_FILL
    cell.alignment = Alignment(horizontal='center')

# Data
data = [
    ['Jan', 120000, 85000], ['Feb', 135000, 90000],
    ['Mar', 128000, 82000], ['Apr', 142000, 95000],
    ['May', 155000, 98000], ['Jun', 148000, 92000],
    ['Jul', 160000, 100000], ['Aug', 168000, 105000],
    ['Sep', 175000, 108000], ['Oct', 182000, 112000],
    ['Nov', 190000, 115000], ['Dec', 210000, 120000],
]
for i, (month, rev, exp) in enumerate(data, 2):
    ws.cell(row=i, column=1, value=month).font = DATA_FONT
    ws.cell(row=i, column=2, value=rev).font = DATA_FONT
    ws.cell(row=i, column=3, value=exp).font = DATA_FONT
    ws.cell(row=i, column=4).value = f'=B{i}-C{i}'
    ws.cell(row=i, column=5).value = f'=D{i}/B{i}'
    for col in range(1, 6):
        ws.cell(row=i, column=col).border = BORDER

# Number formats
for row in range(2, 14):
    ws.cell(row=row, column=2).number_format = '$#,##0'
    ws.cell(row=row, column=3).number_format = '$#,##0'
    ws.cell(row=row, column=4).number_format = '$#,##0'
    ws.cell(row=row, column=5).number_format = '0.0%'

# Totals row
total_row = len(data) + 2
ws.cell(row=total_row, column=1, value='Total').font = Font(bold=True)
ws.cell(row=total_row, column=2, value=f'=SUM(B2:B{total_row-1})')
ws.cell(row=total_row, column=3, value=f'=SUM(C2:C{total_row-1})')
ws.cell(row=total_row, column=4, value=f'=SUM(D2:D{total_row-1})')
ws.cell(row=total_row, column=5, value=f'=D{total_row}/B{total_row}')
for col in range(2, 6):
    ws.cell(row=total_row, column=col).number_format = (
        '$#,##0' if col < 5 else '0.0%'
    )
    ws.cell(row=total_row, column=col).font = Font(bold=True)

# Column widths
ws.column_dimensions['A'].width = 12
for letter in ['B', 'C', 'D']:
    ws.column_dimensions[letter].width = 15
ws.column_dimensions['E'].width = 12

# Freeze header row
ws.freeze_panes = 'A2'

# Conditional formatting on margin column
ws.conditional_formatting.add(
    f'E2:E{total_row-1}',
    ColorScaleRule(
        start_type='min', start_color='F87171',
        end_type='max', end_color='34D399'
    )
)

# Bar chart
chart = BarChart()
chart.title = "Monthly Revenue vs Expenses"
chart.x_axis.title = "Month"
chart.y_axis.title = "Amount ($)"
chart.width = 20
chart.height = 12

cats = Reference(ws, min_col=1, min_row=2, max_row=13)
chart_data = Reference(ws, min_col=2, min_row=1, max_row=13, max_col=3)
chart.add_data(chart_data, titles_from_data=True)
chart.set_categories(cats)
ws.add_chart(chart, "G2")

wb.save('/path/to/financial_report.xlsx')
print('Spreadsheet saved successfully')
```

## Conventions

1. **Header row**: always bold, white text on dark background, frozen at A2
2. **Financial coloring**: blue (#0000FF) for hard-coded inputs, black for formulas, green (#006100) for cross-sheet refs
3. **Number formats**: always use Excel format codes -- `$#,##0` for currency, `0.0%` for percentages, `yyyy-mm-dd` for dates
4. **Sheet order**: summary first, data sheets next, reference/lookup sheets last
5. **Sheet naming**: short, descriptive, no special characters

## Gotchas

1. **Cells are 1-indexed** -- `ws.cell(row=1, column=1)` is A1. There is no row 0 or column 0.
2. **Column widths are in character units** -- roughly 7 pixels per unit. A width of 15 is about 105 pixels.
3. **Row heights are in points** -- 1 point = 1/72 inch. Default row height is about 15 points.
4. **`freeze_panes` takes a cell reference string** -- `'A2'` freezes row 1, `'B2'` freezes row 1 and column A.
5. **Colors have no `#` prefix in openpyxl** -- use `'1B2A4A'` not `'#1B2A4A'`. This applies to Font color, PatternFill, Border Side color, and conditional formatting.
6. **Formulas are strings** -- write `ws['D2'] = '=B2-C2'` with the equals sign as part of the string.
7. **No true auto-fit** -- openpyxl cannot measure rendered text width. Approximate by measuring string length.
8. **Chart data references are 1-indexed** -- `Reference(ws, min_col=2, min_row=1, max_row=13)` refers to B1:B13.
9. **Always validate output** -- check that the file exists and is openable after saving.

## Validation

```bash
ls -la /path/to/output.xlsx
python3 -c "from openpyxl import load_workbook; wb = load_workbook('/path/to/output.xlsx'); print(f'{len(wb.sheetnames)} sheets: {wb.sheetnames}')"
```
