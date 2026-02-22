package ingest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// Compile-time interface checks.
var _ Extractor = (*PDFExtractor)(nil)
var _ MetadataExtractor = (*PDFExtractor)(nil)

// PDFExtractor implements Extractor and MetadataExtractor for PDF documents.
type PDFExtractor struct{}

// NewPDFExtractor creates a PDF extractor.
func NewPDFExtractor() *PDFExtractor { return &PDFExtractor{} }

// Extract extracts plain text from a PDF document.
func (e *PDFExtractor) Extract(content []byte) (string, error) {
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// ExtractWithMeta extracts text page-by-page with page number metadata.
func (e *PDFExtractor) ExtractWithMeta(content []byte) (ExtractResult, error) {
	if len(content) == 0 {
		return ExtractResult{}, fmt.Errorf("empty PDF content")
	}
	r, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("open pdf: %w", err)
	}
	var text strings.Builder
	var meta []PageMeta
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		startByte := text.Len()
		pageText, err := pdfExtractPageText(page)
		if err != nil {
			continue
		}
		if pageText == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n\n")
			startByte = text.Len()
		}
		text.WriteString(pageText)
		endByte := text.Len()
		meta = append(meta, PageMeta{
			PageNumber: i,
			StartByte:  startByte,
			EndByte:    endByte,
		})
	}
	return ExtractResult{
		Text: strings.TrimSpace(text.String()),
		Meta: meta,
	}, nil
}

func pdfExtractPageText(page pdf.Page) (string, error) {
	text, err := page.GetPlainText(nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}
