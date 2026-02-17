// Package pdf provides a PDF text extractor for the ingest pipeline.
//
// It uses pdfcpu (Apache-2.0, pure Go, no CGO) for text extraction.
// This is a separate subpackage so that the pdfcpu dependency is only
// pulled in by users who need PDF support.
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
	"fmt"

	"github.com/nevindra/oasis/ingest"
)

// TypePDF is the content type for PDF documents.
const TypePDF ingest.ContentType = "application/pdf"

// Extractor implements ingest.Extractor for PDF documents using pdfcpu.
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
	// TODO: implement using pdfcpu once the dependency is added.
	// For now, return an error indicating the dependency is needed.
	return "", fmt.Errorf("PDF extraction not yet implemented: add pdfcpu dependency")
}
