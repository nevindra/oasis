package docx

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/nevindra/oasis/ingest"
)

// Compile-time interface checks.
var _ ingest.Extractor = (*Extractor)(nil)
var _ ingest.MetadataExtractor = (*Extractor)(nil)

func TestExtractEmpty(t *testing.T) {
	e := NewExtractor()
	_, err := e.Extract(nil)
	if err == nil {
		t.Error("expected error for nil content")
	}
}

func TestExtractInvalid(t *testing.T) {
	e := NewExtractor()
	_, err := e.Extract([]byte("not a zip"))
	if err == nil {
		t.Error("expected error for invalid content")
	}
}

func TestExtractMinimalDocx(t *testing.T) {
	content := buildTestDocx(t, []testParagraph{
		{text: "Hello World", style: ""},
		{text: "Second paragraph", style: ""},
	})

	e := NewExtractor()
	out, err := e.Extract(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hello World") {
		t.Errorf("missing text: %q", out)
	}
	if !strings.Contains(out, "Second paragraph") {
		t.Errorf("missing text: %q", out)
	}
}

func TestExtractWithHeadings(t *testing.T) {
	content := buildTestDocx(t, []testParagraph{
		{text: "Chapter 1", style: "Heading1"},
		{text: "Some content", style: ""},
		{text: "Section 1.1", style: "Heading2"},
		{text: "More content", style: ""},
	})

	e := NewExtractor()
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Chapter 1") {
		t.Errorf("missing heading: %q", result.Text)
	}

	hasHeading := false
	for _, m := range result.Meta {
		if m.Heading == "Chapter 1" || m.Heading == "Section 1.1" {
			hasHeading = true
		}
	}
	if !hasHeading {
		t.Error("expected heading metadata")
	}
}

func TestExtractHeadingByteOffsets(t *testing.T) {
	content := buildTestDocx(t, []testParagraph{
		{text: "Intro", style: "Heading1"},
		{text: "Body text here", style: ""},
		{text: "Next", style: "Heading1"},
		{text: "More body", style: ""},
	})

	e := NewExtractor()
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range result.Meta {
		if m.StartByte > m.EndByte {
			t.Errorf("heading %q: start %d > end %d", m.Heading, m.StartByte, m.EndByte)
		}
		if m.EndByte > len(result.Text) {
			t.Errorf("heading %q: end %d > text len %d", m.Heading, m.EndByte, len(result.Text))
		}
	}
}

func TestExtractWithTable(t *testing.T) {
	content := buildTestDocxWithTable(t,
		[]string{"Name", "Age"},
		[][]string{{"John", "30"}, {"Jane", "25"}},
	)

	e := NewExtractor()
	out, err := e.Extract(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Name: John") || !strings.Contains(out, "Age: 30") {
		t.Errorf("table not converted to labeled format: %q", out)
	}
	if !strings.Contains(out, "Name: Jane") || !strings.Contains(out, "Age: 25") {
		t.Errorf("second row missing: %q", out)
	}
}

func TestExtractTableEmptyCells(t *testing.T) {
	content := buildTestDocxWithTable(t,
		[]string{"Name", "Age"},
		[][]string{{"John", ""}, {"", "25"}},
	)

	e := NewExtractor()
	out, err := e.Extract(content)
	if err != nil {
		t.Fatal(err)
	}
	// Empty cells should be omitted from labeled output.
	if strings.Contains(out, "Age: ,") || strings.Contains(out, "Name: ,") {
		t.Errorf("empty cell not handled: %q", out)
	}
}

func TestExtractWithImage(t *testing.T) {
	content := buildTestDocxWithImage(t, "image1.png", []byte{0x89, 0x50, 0x4E, 0x47})

	e := NewExtractor()
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		t.Fatal(err)
	}

	foundImage := false
	for _, m := range result.Meta {
		for _, img := range m.Images {
			if img.Base64 != "" {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Error("expected image in metadata")
	}
}

func TestExtractMissingDocumentXML(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Write a dummy file instead of word/document.xml.
	w, _ := zw.Create("word/styles.xml")
	w.Write([]byte("<styles/>"))
	zw.Close()

	e := NewExtractor()
	_, err := e.Extract(buf.Bytes())
	if err == nil {
		t.Error("expected error for missing document.xml")
	}
	if !strings.Contains(err.Error(), "missing word/document.xml") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- test helpers ---

type testParagraph struct {
	text  string
	style string
}

func buildTestDocx(t *testing.T, paragraphs []testParagraph) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString("\n")
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	body.WriteString("\n<w:body>")
	for _, p := range paragraphs {
		body.WriteString("<w:p>")
		if p.style != "" {
			body.WriteString(fmt.Sprintf(`<w:pPr><w:pStyle w:val="%s"/></w:pPr>`, p.style))
		}
		body.WriteString(fmt.Sprintf("<w:r><w:t>%s</w:t></w:r>", p.text))
		body.WriteString("</w:p>")
	}
	body.WriteString("</w:body></w:document>")

	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTestDocxWithTable(t *testing.T, headers []string, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString("\n")
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	body.WriteString("\n<w:body><w:tbl>")

	// Header row.
	body.WriteString("<w:tr>")
	for _, h := range headers {
		body.WriteString(fmt.Sprintf("<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>", h))
	}
	body.WriteString("</w:tr>")

	// Data rows.
	for _, row := range rows {
		body.WriteString("<w:tr>")
		for _, cell := range row {
			body.WriteString(fmt.Sprintf("<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>", cell))
		}
		body.WriteString("</w:tr>")
	}

	body.WriteString("</w:tbl></w:body></w:document>")

	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body.String())); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTestDocxWithImage(t *testing.T, imageName string, imageData []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Write a simple document.xml with a paragraph.
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString("\n")
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	body.WriteString("\n<w:body>")
	body.WriteString("<w:p><w:r><w:t>Document with image</w:t></w:r></w:p>")
	body.WriteString("</w:body></w:document>")

	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body.String())); err != nil {
		t.Fatal(err)
	}

	// Write the image file.
	iw, err := zw.Create("word/media/" + imageName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iw.Write(imageData); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
