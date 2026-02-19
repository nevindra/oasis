// Package pdf provides a PDF text extractor for the ingest pipeline.
//
// It uses ledongthuc/pdf (BSD-3, pure Go, no CGO) for text extraction.
// This is a separate subpackage so that the dependency is only pulled in
// by users who need PDF support.
package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/nevindra/oasis/ingest"
)

// TypePDF is the content type for PDF documents.
const TypePDF ingest.ContentType = "application/pdf"

// Extractor implements ingest.Extractor and ingest.MetadataExtractor for PDF documents.
type Extractor struct{}

// NewExtractor creates a PDF extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract extracts plain text from a PDF document (backward compatible).
func (e *Extractor) Extract(content []byte) (string, error) {
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// ExtractWithMeta extracts text page-by-page with page number metadata.
func (e *Extractor) ExtractWithMeta(content []byte) (ingest.ExtractResult, error) {
	if len(content) == 0 {
		return ingest.ExtractResult{}, fmt.Errorf("empty PDF content")
	}

	r, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ingest.ExtractResult{}, fmt.Errorf("open pdf: %w", err)
	}

	var text strings.Builder
	var meta []ingest.PageMeta

	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}

		startByte := text.Len()

		pageText, err := extractPageText(page)
		if err != nil {
			continue // skip unreadable pages
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

		pm := ingest.PageMeta{
			PageNumber: i,
			StartByte:  startByte,
			EndByte:    endByte,
		}
		meta = append(meta, pm)
	}

	return ingest.ExtractResult{
		Text: strings.TrimSpace(text.String()),
		Meta: meta,
	}, nil
}

// extractPageText extracts readable text from a single PDF page.
func extractPageText(page pdf.Page) (string, error) {
	text, err := page.GetPlainText(nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}
