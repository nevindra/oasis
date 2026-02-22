package ingest

import (
	"strings"
	"testing"
)

func TestJSONExtractFlatObject(t *testing.T) {
	input := `{"name": "John", "age": 30}`
	e := NewJSONExtractor()
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

func TestJSONExtractNestedObject(t *testing.T) {
	input := `{"user": {"name": "John", "address": {"city": "NYC"}}}`
	e := NewJSONExtractor()
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

func TestJSONExtractArray(t *testing.T) {
	input := `{"tags": ["go", "ai", "rag"]}`
	e := NewJSONExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tags: go, ai, rag") {
		t.Errorf("expected comma-joined array, got %q", out)
	}
}

func TestJSONExtractArrayOfObjects(t *testing.T) {
	input := `{"users": [{"name": "John"}, {"name": "Jane"}]}`
	e := NewJSONExtractor()
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

func TestJSONExtractTopLevelArray(t *testing.T) {
	input := `[{"name": "John"}, {"name": "Jane"}]`
	e := NewJSONExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: John") {
		t.Errorf("expected field, got %q", out)
	}
}

func TestJSONExtractEmpty(t *testing.T) {
	e := NewJSONExtractor()
	out, err := e.Extract([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty, got %q", out)
	}
}

func TestJSONExtractInvalid(t *testing.T) {
	e := NewJSONExtractor()
	_, err := e.Extract([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestJSONExtractBoolAndNull(t *testing.T) {
	input := `{"active": true, "deleted": false, "note": null}`
	e := NewJSONExtractor()
	out, err := e.Extract([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "active: true") {
		t.Errorf("expected bool, got %q", out)
	}
}
