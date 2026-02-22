package ingest

import (
	"strings"
	"testing"
)

func TestCSVExtractBasic(t *testing.T) {
	input := "Name,Age,City\nJohn,30,NYC\nJane,25,LA\n"
	e := NewCSVExtractor()
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
	if strings.Count(out, "\n\n") < 1 {
		t.Errorf("expected paragraph separation, got %q", out)
	}
}

func TestCSVExtractEmptyCells(t *testing.T) {
	input := "Name,Age\nJohn,\n,25\n"
	e := NewCSVExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Age: ,") || strings.Contains(out, "Age: \n") {
		t.Errorf("empty cell not handled: %q", out)
	}
}

func TestCSVExtractQuotedFields(t *testing.T) {
	input := "Name,Description\n\"John\",\"Has a comma, here\"\n"
	e := NewCSVExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Has a comma, here") {
		t.Errorf("quoted field not preserved: %q", out)
	}
}

func TestCSVExtractSingleColumn(t *testing.T) {
	input := "Value\n42\n99\n"
	e := NewCSVExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Value: 42") {
		t.Errorf("single column not handled: %q", out)
	}
}

func TestCSVExtractBOM(t *testing.T) {
	input := "\xef\xbb\xbfName,Age\nJohn,30\n"
	e := NewCSVExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Name: John") {
		t.Errorf("BOM not stripped: %q", out)
	}
}

func TestCSVExtractEmpty(t *testing.T) {
	e := NewCSVExtractor()
	out, err := e.Extract([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestCSVExtractHeaderOnly(t *testing.T) {
	e := NewCSVExtractor()
	out, err := e.Extract([]byte("Name,Age\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty output for header-only, got %q", out)
	}
}
