package pdf

import (
	"testing"

	"github.com/nevindra/oasis/ingest"
)

func TestExtractorImplementsInterface(t *testing.T) {
	var _ ingest.Extractor = (*Extractor)(nil)
}

func TestExtractEmptyContent(t *testing.T) {
	e := NewExtractor()
	_, err := e.Extract(nil)
	if err == nil {
		t.Error("expected error for empty content")
	}
}
