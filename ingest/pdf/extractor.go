// Package pdf provides a PDF text extractor for the ingest pipeline.
//
// It uses ledongthuc/pdf (BSD-3, pure Go, no CGO) for text extraction.
// This is a separate subpackage so that the dependency is only pulled in
// by users who need PDF support.
//
// Usage:
//
//	import "github.com/nevindra/oasis/ingest/pdf"
//
//	ingestor := ingest.NewIngestor(store, embedding,
//	    ingest.WithExtractor(ingest.TypePDF, pdf.NewExtractor()),
//	)
package pdf

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/nevindra/oasis/ingest"
)

// TypePDF is the content type for PDF documents.
const TypePDF ingest.ContentType = "application/pdf"

// Extractor implements ingest.Extractor for PDF documents.
type Extractor struct{}

// NewExtractor creates a PDF extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract extracts plain text from a PDF document.
func (e *Extractor) Extract(content []byte) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("empty PDF content")
	}

	r, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}

	plain, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract text: %w", err)
	}

	text, err := io.ReadAll(plain)
	if err != nil {
		return "", fmt.Errorf("read text: %w", err)
	}

	return strings.TrimSpace(string(text)), nil
}
