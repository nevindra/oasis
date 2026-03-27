# XLSX Conventions

Standard conventions for spreadsheet generation.

## Color Coding (Financial Standard)

| Color | Hex | Usage |
|-------|-----|-------|
| Blue | #0000FF | Hard-coded input values (user can edit) |
| Black | #000000 | Formulas and calculated values |
| Green | #006100 | Cross-sheet references |
| Red | #FF0000 | Negative values, warnings |

## Header Row

- Always row 1
- Bold white text on dark background (#1B2A4A)
- Freeze panes at A2 (so headers stay visible)
- Center-aligned

## Number Formatting Rules

1. **Currency:** Always use `$#,##0` or `$#,##0.00` — never raw numbers for money
2. **Percentages:** Store as decimals (0.25), format as `0.0%` — never store as 25
3. **Dates:** Store as Excel dates, format as `yyyy-mm-dd` or `mmmm d, yyyy`
4. **Integers:** Use `#,##0` with comma separators
5. **Accounting:** Use `_($* #,##0.00_)` for aligned columns

## Sheet Organization

1. **Summary sheet first** — the reader sees the big picture before details
2. **Data sheets next** — raw data supporting the summary
3. **Reference/lookup sheets last** — helper tables, constants
4. **Tab colors** — use theme colors to visually group related sheets

## Formula Best Practices

1. **Reference named ranges** or explicit cells — never hardcode values in formulas
2. **One formula pattern per column** — if D2 is `=B2-C2`, then D3 should be `=B3-C3`
3. **SUM for totals, not addition** — `=SUM(B2:B13)` not `=B2+B3+...+B13`
4. **Cross-sheet references** — prefix with sheet name: `=Revenue!B2`

## Common Patterns

### Monthly Financial Report
- Columns: Month, Revenue, Expenses, Profit, Margin
- Formulas: Profit = Revenue - Expenses, Margin = Profit / Revenue
- Summary row: SUM for totals, AVERAGE for margin
- Chart: Bar chart comparing Revenue vs Expenses

### Data Export
- First row: column headers
- Freeze panes at A2
- Auto-width columns
- No formatting (raw data for import)
