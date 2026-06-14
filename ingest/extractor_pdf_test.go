package ingest

import (
	"context"
	"testing"
)

func TestPDFExtractEmptyContent(t *testing.T) {
	e := NewPDFExtractor()
	_, err := e.Extract(context.Background(), nil)
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestPDFExtractWithMetaEmptyContent(t *testing.T) {
	e := NewPDFExtractor()
	_, err := e.ExtractWithMeta(context.Background(), nil)
	if err == nil {
		t.Error("expected error for empty content")
	}
}
