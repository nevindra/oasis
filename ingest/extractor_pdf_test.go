package ingest

import (
	"testing"
)

func TestPDFExtractEmptyContent(t *testing.T) {
	e := NewPDFExtractor()
	_, err := e.Extract(nil)
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestPDFExtractWithMetaEmptyContent(t *testing.T) {
	e := NewPDFExtractor()
	_, err := e.ExtractWithMeta(nil)
	if err == nil {
		t.Error("expected error for empty content")
	}
}
