package core

import (
	"bytes"
	"encoding/json"
)

// coerceArgs applies structural, information-preserving transforms before
// json.Unmarshal in the erased adapters. Two transforms:
//
//  1. null, empty bytes, or whitespace-only input → {}
//     (LLMs occasionally send literal "null" or an empty body for an absent
//     object argument.)
//  2. A single JSON string whose value parses as an object or array →
//     unwrap one level. (LLMs occasionally send stringified JSON for tools
//     with a JSON-shaped argument.)
//
// All other inputs pass through unchanged so the existing json.Unmarshal
// failure path reports the real problem. Coercion never errors. On the
// happy path (already an object or array) this function performs zero heap
// allocations: it only takes sub-slices of the input.
func coerceArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			inner := bytes.TrimSpace([]byte(s))
			if len(inner) > 0 && (inner[0] == '{' || inner[0] == '[') && json.Valid(inner) {
				return json.RawMessage(inner)
			}
		}
	}
	return raw
}
