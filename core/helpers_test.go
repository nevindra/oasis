package core_test

import (
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestTextResultJSONEncodes(t *testing.T) {
	r := core.TextResult("hello")
	if string(r.Content) != `"hello"` {
		t.Errorf("TextResult content = %q, want %q", r.Content, `"hello"`)
	}
	// Must decode back to the original string via json.Unmarshal.
	var s string
	if err := json.Unmarshal(r.Content, &s); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if s != "hello" {
		t.Errorf("decoded string = %q, want %q", s, "hello")
	}
}

func TestJSONContentPreservesBytes(t *testing.T) {
	input := []byte(`{"a":1}`)
	raw := core.JSONContent(input)
	if string(raw) != `{"a":1}` {
		t.Errorf("JSONContent = %q, want %q", raw, `{"a":1}`)
	}
}

func TestToolResultContentRoundTrip(t *testing.T) {
	// A ToolResult with TextContent("hi") should marshal with "hi" as the
	// content value (a JSON string literal), not double-encoded bytes.
	r := core.ToolResult{Content: core.TextContent("hi")}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// Content in the wire JSON should be the JSON string "hi", not base64.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	got := string(m["content"])
	if got != `"hi"` {
		t.Errorf("wire content = %q, want %q", got, `"hi"`)
	}
}
