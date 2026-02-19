package json

import (
	"strings"
	"testing"

	"github.com/nevindra/oasis/ingest"
)

var _ ingest.Extractor = (*Extractor)(nil)

func TestExtractFlatObject(t *testing.T) {
	input := `{"name": "John", "age": 30}`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: John") {
		t.Errorf("expected 'name: John', got %q", out)
	}
	if !strings.Contains(out, "age: 30") {
		t.Errorf("expected 'age: 30', got %q", out)
	}
}

func TestExtractNestedObject(t *testing.T) {
	input := `{"user": {"name": "John", "address": {"city": "NYC"}}}`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user.name: John") {
		t.Errorf("expected dotted path, got %q", out)
	}
	if !strings.Contains(out, "user.address.city: NYC") {
		t.Errorf("expected dotted path, got %q", out)
	}
}

func TestExtractArray(t *testing.T) {
	input := `{"tags": ["go", "ai", "rag"]}`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tags: go, ai, rag") {
		t.Errorf("expected comma-joined array, got %q", out)
	}
}

func TestExtractArrayOfObjects(t *testing.T) {
	input := `{"users": [{"name": "John"}, {"name": "Jane"}]}`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "users.name: John") {
		t.Errorf("expected indexed path, got %q", out)
	}
	if !strings.Contains(out, "users.name: Jane") {
		t.Errorf("expected indexed path, got %q", out)
	}
}

func TestExtractTopLevelArray(t *testing.T) {
	input := `[{"name": "John"}, {"name": "Jane"}]`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: John") {
		t.Errorf("expected field, got %q", out)
	}
}

func TestExtractEmpty(t *testing.T) {
	e := NewExtractor()
	out, err := e.Extract([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty, got %q", out)
	}
}

func TestExtractInvalid(t *testing.T) {
	e := NewExtractor()
	_, err := e.Extract([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExtractBoolAndNull(t *testing.T) {
	input := `{"active": true, "deleted": false, "note": null}`
	e := NewExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "active: true") {
		t.Errorf("expected bool, got %q", out)
	}
}
