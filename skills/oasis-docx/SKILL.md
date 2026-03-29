---
name: oasis-docx
description: Generate Word documents using python-docx. Use when the user needs .docx files with headings, tables, images, styled text, or professional formatting.
compatibility: Requires python3 and python-docx package in sandbox
tags: [document, docx, word, export]
tools: [shell, file_write, file_read, execute_code]
references: [oasis-design-system]
---

# DOCX Generation

Generate Word documents by writing a Python script that uses the python-docx library directly, then executing it in the sandbox.

## Approach

1. Write a Python script using `python-docx` to build the document
2. Execute the script in the sandbox
3. Validate the output file exists and has reasonable size

## Basic Structure

```python
from docx import Document
from docx.shared import Inches, Pt, Cm, Emu, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.enum.table import WD_TABLE_ALIGNMENT
from docx.enum.section import WD_ORIENT

doc = Document()

# Add content
doc.add_heading('Document Title', level=0)
doc.add_paragraph('Introduction paragraph.')
doc.add_heading('Section Heading', level=1)
doc.add_paragraph('Section content here.')

doc.save('/path/to/output.docx')
```

## Page Setup

```python
from docx.shared import Inches
from docx.enum.section import WD_ORIENT

section = doc.sections[0]

# Margins
section.top_margin = Inches(1)
section.bottom_margin = Inches(1)
section.left_margin = Inches(1.25)
section.right_margin = Inches(1.25)

# Page size (A4)
section.page_width = Inches(8.27)
section.page_height = Inches(11.69)

# Landscape orientation
section.orientation = WD_ORIENT.LANDSCAPE
section.page_width, section.page_height = section.page_height, section.page_width
```

Common page sizes:

| Size | Width | Height |
|------|-------|--------|
| A4 | 8.27in | 11.69in |
| Letter | 8.5in | 11in |
| Legal | 8.5in | 14in |

## Styling Text

```python
from docx.shared import Pt, RGBColor

# Paragraph with styled runs
para = doc.add_paragraph()
run = para.add_run('Bold and colored text')
run.bold = True
run.font.size = Pt(14)
run.font.color.rgb = RGBColor(0x1B, 0x2A, 0x4A)
run.font.name = 'Calibri'

# Italic run in the same paragraph
run2 = para.add_run(' followed by italic text')
run2.italic = True

# Paragraph alignment
para.alignment = WD_ALIGN_PARAGRAPH.CENTER  # LEFT, CENTER, RIGHT, JUSTIFY

# Paragraph spacing
para.paragraph_format.space_before = Pt(6)
para.paragraph_format.space_after = Pt(12)
para.paragraph_format.line_spacing = Pt(18)  # Or use a float like 1.5 for 1.5x
```

## Headings

```python
# level=0 is the Title style, 1-4 are Heading 1-4
doc.add_heading('Title', level=0)
doc.add_heading('Chapter', level=1)
doc.add_heading('Section', level=2)
doc.add_heading('Subsection', level=3)

# Custom-styled heading
heading = doc.add_heading('Custom Heading', level=1)
for run in heading.runs:
    run.font.color.rgb = RGBColor(0x1B, 0x2A, 0x4A)
    run.font.size = Pt(22)
```

## Tables

```python
from docx.shared import Pt, Inches, RGBColor, Cm
from docx.oxml.ns import qn, nsdecls
from docx.oxml import parse_xml

# Create table
table = doc.add_table(rows=1, cols=3)
table.alignment = WD_TABLE_ALIGNMENT.CENTER

# Header row
headers = ['Name', 'Revenue', 'Growth']
for i, text in enumerate(headers):
    cell = table.rows[0].cells[i]
    cell.text = text
    # Style header
    for para in cell.paragraphs:
        for run in para.runs:
            run.bold = True
            run.font.color.rgb = RGBColor(0xFF, 0xFF, 0xFF)
            run.font.size = Pt(10)
    # Header background color
    shading = parse_xml(f'<w:shd {nsdecls("w")} w:fill="1B2A4A"/>')
    cell._element.get_or_add_tcPr().append(shading)

# Data rows
data = [
    ['Product A', '$1.2M', '+25%'],
    ['Product B', '$800K', '+12%'],
    ['Product C', '$450K', '+8%'],
]
for row_data in data:
    row = table.add_row()
    for i, val in enumerate(row_data):
        row.cells[i].text = val

# Set column widths
for i, width in enumerate([Inches(2.5), Inches(1.5), Inches(1.5)]):
    for row in table.rows:
        row.cells[i].width = width
```

### Merging Cells

```python
# Merge cells horizontally
table.cell(0, 0).merge(table.cell(0, 2))

# Merge cells vertically
table.cell(1, 0).merge(table.cell(3, 0))
```

### Table Borders

```python
from docx.oxml.ns import qn, nsdecls
from docx.oxml import parse_xml

def set_cell_border(cell, **kwargs):
    """Set cell borders. Each kwarg is a border name (top, bottom, left, right)
    with a dict of {sz, val, color} where sz is in eighths of a point."""
    tc = cell._element
    tcPr = tc.get_or_add_tcPr()
    tcBorders = parse_xml(f'<w:tcBorders {nsdecls("w")}></w:tcBorders>')
    for edge, attrs in kwargs.items():
        el = parse_xml(
            f'<w:{edge} {nsdecls("w")} w:val="{attrs.get("val", "single")}" '
            f'w:sz="{attrs.get("sz", 4)}" w:color="{attrs.get("color", "000000")}"/>'
        )
        tcBorders.append(el)
    tcPr.append(tcBorders)
```

## Images

```python
from docx.shared import Inches

# Add image with width constraint (height auto-scales)
doc.add_picture('/path/to/chart.png', width=Inches(5))

# Center the image
last_paragraph = doc.paragraphs[-1]
last_paragraph.alignment = WD_ALIGN_PARAGRAPH.CENTER

# Add caption below
caption = doc.add_paragraph('Figure 1: Revenue by Quarter')
caption.alignment = WD_ALIGN_PARAGRAPH.CENTER
caption.style = doc.styles['Caption'] if 'Caption' in [s.name for s in doc.styles] else None
```

## Lists

```python
# Bulleted list
doc.add_paragraph('First item', style='List Bullet')
doc.add_paragraph('Second item', style='List Bullet')
doc.add_paragraph('Third item', style='List Bullet')

# Numbered list
doc.add_paragraph('Step one', style='List Number')
doc.add_paragraph('Step two', style='List Number')
doc.add_paragraph('Step three', style='List Number')
```

## Page Breaks

```python
from docx.enum.text import WD_BREAK

# Add page break
doc.add_page_break()

# Or add a break within a paragraph
para = doc.add_paragraph()
para.add_run().add_break(WD_BREAK.PAGE)
```

## Table of Contents

```python
from docx.oxml.ns import qn

# Insert a TOC field code (user must update fields in Word to populate)
para = doc.add_paragraph()
run = para.add_run()
fldChar1 = run._element.makeelement(qn('w:fldChar'), {qn('w:fldCharType'): 'begin'})
run._element.append(fldChar1)

run2 = para.add_run()
instrText = run2._element.makeelement(qn('w:instrText'), {})
instrText.text = ' TOC \\o "1-3" \\h \\z \\u '
run2._element.append(instrText)

run3 = para.add_run()
fldChar2 = run3._element.makeelement(qn('w:fldChar'), {qn('w:fldCharType'): 'end'})
run3._element.append(fldChar2)
```

## Complete Example: Business Report

```python
from docx import Document
from docx.shared import Inches, Pt, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml.ns import nsdecls
from docx.oxml import parse_xml

doc = Document()

# Page setup
section = doc.sections[0]
section.top_margin = Inches(1)
section.bottom_margin = Inches(1)
section.left_margin = Inches(1.25)
section.right_margin = Inches(1.25)

# Colors from oasis-design-system Corporate palette
PRIMARY = RGBColor(0x1B, 0x2A, 0x4A)
SECONDARY = RGBColor(0x2D, 0x5F, 0x8A)
ACCENT = RGBColor(0xE8, 0x73, 0x4A)

# Title
title = doc.add_heading('Q4 Financial Report', level=0)
for run in title.runs:
    run.font.color.rgb = PRIMARY

subtitle = doc.add_paragraph('Prepared for the Board of Directors')
subtitle.alignment = WD_ALIGN_PARAGRAPH.CENTER
for run in subtitle.runs:
    run.italic = True
    run.font.color.rgb = SECONDARY

doc.add_page_break()

# Section 1
h = doc.add_heading('Executive Summary', level=1)
for run in h.runs:
    run.font.color.rgb = PRIMARY

doc.add_paragraph(
    'Revenue grew 25% year-over-year to $2.1M in Q4, driven by enterprise '
    'segment expansion. Operating margins improved to 42%, up from 35% in Q3.'
)

# Key metrics table
table = doc.add_table(rows=1, cols=4)
headers = ['Metric', 'Q3', 'Q4', 'Change']
for i, text in enumerate(headers):
    cell = table.rows[0].cells[i]
    cell.text = text
    for para in cell.paragraphs:
        for run in para.runs:
            run.bold = True
            run.font.color.rgb = RGBColor(0xFF, 0xFF, 0xFF)
            run.font.size = Pt(10)
    shading = parse_xml(f'<w:shd {nsdecls("w")} w:fill="1B2A4A"/>')
    cell._element.get_or_add_tcPr().append(shading)

data = [
    ['Revenue', '$1.8M', '$2.1M', '+25%'],
    ['Customers', '145', '178', '+23%'],
    ['Margin', '35%', '42%', '+7pp'],
]
for row_data in data:
    row = table.add_row()
    for i, val in enumerate(row_data):
        row.cells[i].text = val

for i, width in enumerate([Inches(2), Inches(1.2), Inches(1.2), Inches(1.2)]):
    for row in table.rows:
        row.cells[i].width = width

doc.add_page_break()

# Section 2
h2 = doc.add_heading('Recommendations', level=1)
for run in h2.runs:
    run.font.color.rgb = PRIMARY

doc.add_paragraph('Expand enterprise sales team by 3 headcount', style='List Number')
doc.add_paragraph('Launch self-serve tier for SMB segment', style='List Number')
doc.add_paragraph('Invest in product-led growth infrastructure', style='List Number')

doc.save('/path/to/report.docx')
print('Report saved successfully')
```

## Gotchas

1. **Measurements require unit objects** -- always use `Inches()`, `Cm()`, `Pt()`, or `Emu()`. Raw numbers are interpreted as EMUs (English Metric Units, 914400 per inch).
2. **Table column widths need explicit setting** -- python-docx does not auto-size columns. Set width on every cell in the column for consistent results.
3. **Style inheritance** -- paragraph styles set font properties, but `add_run()` creates a new run that may not inherit. Always set font properties on the run, not the paragraph.
4. **Heading colors** -- built-in heading styles use theme colors. To override, loop over `heading.runs` and set `run.font.color.rgb` explicitly after adding the heading.
5. **Images need absolute paths** or the script must be run from the correct working directory.
6. **TOC needs manual update** -- the TOC field code inserts a placeholder. The user must open the file in Word and press Ctrl+A then F9 to update fields.
7. **No native chart support** -- python-docx cannot create charts. Generate chart images with matplotlib, save as PNG, and insert with `add_picture()`.
8. **Always validate output** -- after saving, check the file exists and has reasonable size.

## Validation

```bash
ls -la /path/to/output.docx
python3 -c "from docx import Document; d = Document('/path/to/output.docx'); print(f'{len(d.paragraphs)} paragraphs, {len(d.tables)} tables')"
```
