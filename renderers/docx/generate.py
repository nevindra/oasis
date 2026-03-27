#!/usr/bin/env python3
"""Generate DOCX from a JSON content specification using python-docx."""

import argparse
import json
import sys
from pathlib import Path

from docx import Document
from docx.shared import Inches, Pt, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml.ns import qn


# Style presets: font_name, heading_color, body_size, line_spacing
STYLES = {
    "business":  ("Calibri",          RGBColor(0x1B, 0x2A, 0x4A), Pt(11), 1.15),
    "academic":  ("Times New Roman",  RGBColor(0x00, 0x00, 0x00), Pt(12), 2.0),
    "minimal":   ("Inter",            RGBColor(0x11, 0x11, 0x11), Pt(10.5), 1.2),
    "formal":    ("Garamond",         RGBColor(0x1A, 0x1A, 0x1A), Pt(12), 1.15),
}


def apply_style(doc, style_name):
    """Configure base document style."""
    font_name, heading_color, body_size, line_spacing = STYLES.get(
        style_name, STYLES["business"]
    )

    style = doc.styles["Normal"]
    style.font.name = font_name
    style.font.size = body_size
    style.paragraph_format.line_spacing = line_spacing

    for i in range(1, 5):
        heading_style = doc.styles[f"Heading {i}"]
        heading_style.font.name = font_name
        heading_style.font.color.rgb = heading_color

    return font_name, heading_color, body_size


def set_page_setup(doc, page_cfg):
    """Apply page size and margins from spec."""
    if not page_cfg:
        return

    section = doc.sections[0]
    margins = page_cfg.get("margins", {})
    if "top" in margins:
        section.top_margin = Inches(margins["top"])
    if "bottom" in margins:
        section.bottom_margin = Inches(margins["bottom"])
    if "left" in margins:
        section.left_margin = Inches(margins["left"])
    if "right" in margins:
        section.right_margin = Inches(margins["right"])


ALIGN_MAP = {
    "left": WD_ALIGN_PARAGRAPH.LEFT,
    "center": WD_ALIGN_PARAGRAPH.CENTER,
    "right": WD_ALIGN_PARAGRAPH.RIGHT,
    "justify": WD_ALIGN_PARAGRAPH.JUSTIFY,
}


def add_content(doc, blocks, font_name):
    """Process content blocks and add to document."""
    for block in blocks:
        block_type = block.get("type", "")

        if block_type == "heading":
            level = block.get("level", 1)
            doc.add_heading(block["text"], level=min(level, 4))

        elif block_type == "paragraph":
            para = doc.add_paragraph()
            run = para.add_run(block["text"])
            if block.get("bold"):
                run.bold = True
            if block.get("italic"):
                run.italic = True
            if block.get("align"):
                para.alignment = ALIGN_MAP.get(block["align"], WD_ALIGN_PARAGRAPH.LEFT)

        elif block_type == "table":
            headers = block["headers"]
            rows = block["rows"]
            table = doc.add_table(rows=1, cols=len(headers))
            table.style = "Table Grid"

            # Header row.
            for i, header in enumerate(headers):
                cell = table.rows[0].cells[i]
                cell.text = str(header)
                for para in cell.paragraphs:
                    for run in para.runs:
                        run.bold = True

            # Data rows.
            for row_data in rows:
                row = table.add_row()
                for i, val in enumerate(row_data):
                    row.cells[i].text = str(val) if val is not None else ""

            if block.get("caption"):
                doc.add_paragraph(block["caption"]).style = doc.styles["Caption"] if "Caption" in doc.styles else doc.styles["Normal"]

        elif block_type == "image":
            width = Inches(block.get("width", 5))
            doc.add_picture(block["path"], width=width)
            if block.get("caption"):
                cap = doc.add_paragraph(block["caption"])
                cap.alignment = WD_ALIGN_PARAGRAPH.CENTER
                cap.style = doc.styles["Normal"]
                for run in cap.runs:
                    run.font.size = Pt(9)
                    run.font.color.rgb = RGBColor(0x6B, 0x72, 0x80)

        elif block_type == "list":
            items = block.get("items", [])
            ordered = block.get("ordered", False)
            for i, item in enumerate(items):
                style = "List Number" if ordered else "List Bullet"
                doc.add_paragraph(item, style=style)

        elif block_type == "page_break":
            doc.add_page_break()

        elif block_type == "toc":
            para = doc.add_paragraph()
            run = para.add_run()
            fld_char_begin = run._element.makeelement(qn("w:fldChar"), {qn("w:fldCharType"): "begin"})
            run._element.append(fld_char_begin)

            run2 = para.add_run()
            instr = run2._element.makeelement(qn("w:instrText"), {})
            depth = block.get("depth", 3)
            instr.text = f' TOC \\o "1-{depth}" \\h \\z \\u '
            run2._element.append(instr)

            run3 = para.add_run()
            fld_char_end = run3._element.makeelement(qn("w:fldChar"), {qn("w:fldCharType"): "end"})
            run3._element.append(fld_char_end)

        elif block_type == "code":
            para = doc.add_paragraph()
            run = para.add_run(block["text"])
            run.font.name = "Consolas"
            run.font.size = Pt(9)
            para.paragraph_format.left_indent = Inches(0.25)

        elif block_type == "quote":
            para = doc.add_paragraph()
            run = para.add_run(block["text"])
            run.italic = True
            para.paragraph_format.left_indent = Inches(0.5)
            if block.get("author"):
                attr = doc.add_paragraph()
                attr_run = attr.add_run(f"--- {block['author']}")
                attr_run.font.size = Pt(9)
                attr_run.font.color.rgb = RGBColor(0x6B, 0x72, 0x80)
                attr.paragraph_format.left_indent = Inches(0.5)

        elif block_type == "hr":
            para = doc.add_paragraph()
            pBdr = para._element.makeelement(qn("w:pBdr"), {})
            bottom = pBdr.makeelement(qn("w:bottom"), {
                qn("w:val"): "single",
                qn("w:sz"): "4",
                qn("w:space"): "1",
                qn("w:color"): "CCCCCC",
            })
            pBdr.append(bottom)
            pPr = para._element.get_or_add_pPr()
            pPr.append(pBdr)


def main():
    parser = argparse.ArgumentParser(description="Generate DOCX from JSON spec")
    parser.add_argument("input", help="JSON spec file")
    parser.add_argument("output", help="Output DOCX path")
    parser.add_argument("--template", help="Base .docx template file")
    parser.add_argument("--style", default="business", choices=STYLES.keys())
    args = parser.parse_args()

    with open(args.input) as f:
        spec = json.load(f)

    if args.template:
        doc = Document(args.template)
    else:
        doc = Document()

    style_name = spec.get("style", args.style)
    font_name, _, _ = apply_style(doc, style_name)
    set_page_setup(doc, spec.get("page"))
    add_content(doc, spec.get("content", []), font_name)

    doc.save(args.output)
    print(str(Path(args.output).resolve()))


if __name__ == "__main__":
    main()
