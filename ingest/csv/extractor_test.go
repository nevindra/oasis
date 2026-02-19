package csv

import (
	"strings"
	"testing"

	"github.com/nevindra/oasis/ingest"
)

var _ ingest.Extractor = (*Extractor)(nil)

func TestExtractBasic(t *testing.T) {
	input := "Name,Age,City\nJohn,30,NYC\nJane,25,LA\n"
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Name: John") {
		t.Errorf("expected labeled field, got %q", out)
	}
	if !strings.Contains(out, "Age: 30") {
		t.Errorf("expected labeled field, got %q", out)
	}
	// Two rows should produce two paragraphs separated by blank line
	if strings.Count(out, "\n\n") < 1 {
		t.Errorf("expected paragraph separation, got %q", out)
	}
}

func TestExtractEmptyCells(t *testing.T) {
	input := "Name,Age\nJohn,\n,25\n"
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	// Empty cells should be omitted from output
	if strings.Contains(out, "Age: ,") || strings.Contains(out, "Age: \n") {
		t.Errorf("empty cell not handled: %q", out)
	}
}

func TestExtractQuotedFields(t *testing.T) {
	input := "Name,Description\n\"John\",\"Has a comma, here\"\n"
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Has a comma, here") {
		t.Errorf("quoted field not preserved: %q", out)
	}
}

func TestExtractSingleColumn(t *testing.T) {
	input := "Value\n42\n99\n"
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Value: 42") {
		t.Errorf("single column not handled: %q", out)
	}
}

func TestExtractBOM(t *testing.T) {
	input := "\xef\xbb\xbfName,Age\nJohn,30\n"
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Name: John") {
		t.Errorf("BOM not stripped: %q", out)
	}
}

func TestExtractEmpty(t *testing.T) {
	e := NewExtractor()
	out, err := e.Extract([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestExtractHeaderOnly(t *testing.T) {
	e := NewExtractor()
	out, err := e.Extract([]byte("Name,Age\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty output for header-only, got %q", out)
	}
}
