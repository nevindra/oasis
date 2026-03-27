#!/usr/bin/env python3
"""Fill DOCX template placeholders ({{key}}) with data from a JSON file."""

import argparse
import json
import sys
from pathlib import Path

from docx import Document


def replace_in_paragraph(paragraph, replacements):
    """Replace {{placeholder}} tokens in a paragraph, preserving formatting."""
    full_text = paragraph.text
    if "{{" not in full_text:
        return

    for key, value in replacements.items():
        if key in full_text:
            full_text = full_text.replace(key, str(value))

    # Rebuild runs — assign all text to the first run, clear the rest.
    if paragraph.runs:
        paragraph.runs[0].text = full_text
        for run in paragraph.runs[1:]:
            run.text = ""


def replace_in_table(table, replacements):
    """Replace placeholders in all table cells."""
    for row in table.rows:
        for cell in row.cells:
            for paragraph in cell.paragraphs:
                replace_in_paragraph(paragraph, replacements)


def main():
    parser = argparse.ArgumentParser(description="Fill DOCX template with data")
    parser.add_argument("template", help="Template .docx file")
    parser.add_argument("output", help="Output .docx path")
    parser.add_argument("--data", required=True, help="JSON data file")
    args = parser.parse_args()

    with open(args.data) as f:
        data = json.load(f)

    doc = Document(args.template)

    # Replace in body paragraphs.
    for para in doc.paragraphs:
        replace_in_paragraph(para, data)

    # Replace in tables.
    for table in doc.tables:
        replace_in_table(table, data)

    # Replace in headers and footers.
    for section in doc.sections:
        for para in section.header.paragraphs:
            replace_in_paragraph(para, data)
        for para in section.footer.paragraphs:
            replace_in_paragraph(para, data)

    doc.save(args.output)
    print(str(Path(args.output).resolve()))


if __name__ == "__main__":
    main()
