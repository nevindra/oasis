#!/usr/bin/env python3
"""Fill PDF AcroForm fields using pypdf."""

import json
import sys
from pathlib import Path


def main():
    if len(sys.argv) < 3:
        print("Usage: fill.py <input.pdf> <output.pdf> --fields <fields.json>", file=sys.stderr)
        sys.exit(1)

    input_path = sys.argv[1]
    output_path = sys.argv[2]
    fields_path = None

    i = 3
    while i < len(sys.argv):
        if sys.argv[i] == "--fields" and i + 1 < len(sys.argv):
            fields_path = sys.argv[i + 1]
            i += 2
        else:
            i += 1

    if not fields_path:
        print("Error: --fields <fields.json> is required", file=sys.stderr)
        sys.exit(1)

    from pypdf import PdfReader, PdfWriter

    with open(fields_path) as f:
        fields = json.load(f)

    reader = PdfReader(input_path)
    writer = PdfWriter()
    writer.append(reader)

    for page in writer.pages:
        writer.update_page_form_field_values(page, fields)

    with open(output_path, "wb") as f:
        writer.write(f)

    print(str(Path(output_path).resolve()))


if __name__ == "__main__":
    main()
