package core

import (
	"encoding/json"
	"testing"
)

func TestCoerceArgs_NullAndEmpty(t *testing.T) {
	cases := map[string]json.RawMessage{
		"empty bytes":     nil,
		"zero length":     json.RawMessage(""),
		"literal null":    json.RawMessage("null"),
		"padded null":     json.RawMessage("  null \n"),
		"whitespace only": json.RawMessage("   "),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := coerceArgs(in)
			if string(got) != "{}" {
				t.Errorf("coerceArgs(%q) = %q, want {}", in, got)
			}
		})
	}
}

func TestCoerceArgs_StringifiedObject(t *testing.T) {
	in := json.RawMessage(`"{\"x\":1}"`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want {\"x\":1}", in, got)
	}
}

func TestCoerceArgs_StringifiedArray(t *testing.T) {
	in := json.RawMessage(`"[1,2,3]"`)
	got := coerceArgs(in)
	if string(got) != `[1,2,3]` {
		t.Errorf("coerceArgs(%q) = %q, want [1,2,3]", in, got)
	}
}

func TestCoerceArgs_StringifiedWithWhitespace(t *testing.T) {
	in := json.RawMessage(`"  {\"x\":1}  "`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want {\"x\":1}", in, got)
	}
}

func TestCoerceArgs_PlainStringPassesThrough(t *testing.T) {
	in := json.RawMessage(`"hello"`)
	got := coerceArgs(in)
	if string(got) != `"hello"` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_AlreadyObjectPassesThrough(t *testing.T) {
	in := json.RawMessage(`{"x":1}`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_MalformedPassesThrough(t *testing.T) {
	in := json.RawMessage(`{"x":`)
	got := coerceArgs(in)
	if string(got) != `{"x":` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_StringifiedInvalidJSONPassesThrough(t *testing.T) {
	in := json.RawMessage(`"{not json}"`)
	got := coerceArgs(in)
	if string(got) != `"{not json}"` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}
