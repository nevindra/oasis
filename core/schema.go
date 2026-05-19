package core

import (
	"encoding/json"
	"reflect"
)

// SchemaProvider is the opt-out for the reflection-based schema derivation
// performed by Erase. An input type In may implement SchemaProvider to supply
// its own JSON Schema when the reflector cannot express what the tool needs
// (e.g. oneOf, conditional required, recursive shapes).
//
// JSONSchema is called once per Erase[In, Out] invocation. Implementations
// must return a syntactically valid JSON Schema. The returned bytes are
// passed through to the LLM unchanged.
type SchemaProvider interface {
	JSONSchema() json.RawMessage
}

// DeriveSchema returns the JSON Schema for T computed by reflection.
//
// Use this when you build a ToolDefinition by hand (built-in tools that
// don't go through Erase, schema-aware test helpers). Tool authors should
// not call this directly — Erase[In, Out] does it for you.
//
// If T (a pointer-to or value receiver) implements SchemaProvider, the
// override is returned verbatim. Otherwise the reflector walks T's type
// according to the supported-types table (see top-of-file comment) and
// panics on unsupported shapes.
//
// Supported types — emit JSON Schema features accepted by every current
// provider (Gemini, OpenAI, Anthropic):
//   - bool, all int/uint widths, float32/64, string
//   - []T, []byte, map[string]T, struct, *T
//   - any, json.RawMessage → {}
//   - time.Time → {"type":"string","format":"date-time"}
//
// Recognised struct tags:
//   - json:"name,omitempty" — field naming and optionality (stdlib)
//   - describe:"..." — free-text description shown to the LLM
//   - enum:"a,b,c" — comma-separated string enumeration (string fields only)
func DeriveSchema[T any]() json.RawMessage {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	visited := make(map[reflect.Type]bool)
	m := buildSchema(t, "", visited)
	out, err := json.Marshal(m)
	if err != nil {
		// json.Marshal on map[string]any with stdlib-typeable contents is
		// infallible in practice; panicking matches the registration-time
		// error model.
		panic("oasis.DeriveSchema: marshal failed: " + err.Error())
	}
	return out
}

// buildSchema is the recursive reflector. fieldPath carries the dotted
// field path for use in panic messages (e.g. "Where[].Op"). visited tracks
// struct types currently on the recursion stack and panics on revisits.
func buildSchema(t reflect.Type, fieldPath string, visited map[reflect.Type]bool) map[string]any {
	// Stub — populated in subsequent tasks.
	panic("oasis.DeriveSchema: not yet implemented")
}
