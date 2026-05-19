package core

import (
	"encoding/json"
	"reflect"
	"time"
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
	// Unwrap pointer at this level; pointer-optionality is handled by callers
	// that own the struct field. Top-level *T just means "treat as T".
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Special-type fast path.
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage(nil)):
		return map[string]any{}
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
		// []byte (json.RawMessage handled above before falling here).
		return map[string]any{"type": "string"}
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		// []byte is special-cased in Task A4.
		return map[string]any{
			"type":  "array",
			"items": buildSchema(t.Elem(), fieldPath+"[]", visited),
		}
	case reflect.Array:
		return map[string]any{
			"type":  "array",
			"items": buildSchema(t.Elem(), fieldPath+"[]", visited),
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic("oasis.DeriveSchema: field " + fieldOrRoot(fieldPath) +
				" map key must be string, got " + t.Key().String())
		}
		return map[string]any{
			"type":                 "object",
			"additionalProperties": buildSchema(t.Elem(), fieldPath+"{}", visited),
		}
	case reflect.Struct:
		return buildStructSchema(t, fieldPath, visited)
	case reflect.Interface:
		// any / interface{} → {}.
		if t.NumMethod() == 0 {
			return map[string]any{}
		}
		panic(rejectMessage(fieldPath, t, "interface-with-methods"))
	}

	panic(rejectMessage(fieldPath, t, t.Kind().String()))
}

func fieldOrRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}

// buildStructSchema walks a struct, honoring json tags, and produces
// {type, properties, required}. Pointer fields and json:omitempty are
// excluded from required.
func buildStructSchema(t reflect.Type, fieldPath string, visited map[reflect.Type]bool) map[string]any {
	props := make(map[string]any)
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, omitempty, skip := parseJSONTag(f)
		if skip {
			continue
		}

		childPath := fieldPath
		if childPath == "" {
			childPath = name
		} else {
			childPath = childPath + "." + name
		}

		props[name] = buildSchema(f.Type, childPath, visited)

		if !omitempty && f.Type.Kind() != reflect.Ptr {
			required = append(required, name)
		}
	}

	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = anySlice(required)
	}
	return out
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// parseJSONTag returns the effective field name, whether omitempty is set,
// and whether the field should be skipped entirely (json:"-").
func parseJSONTag(f reflect.StructField) (name string, omitempty, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name = f.Name
	if tag == "" {
		return name, false, false
	}
	parts := splitComma(tag)
	if parts[0] != "" {
		name = parts[0]
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

// splitComma is a tiny helper to avoid pulling strings.Split into the same
// list of imports just for one call (keeps the dependency surface minimal).
func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// rejectMessage builds a panic string per the spec's error-message contract:
// includes the field path (or "(root)"), the Go type, and the family that was
// being attempted.
func rejectMessage(fieldPath string, t reflect.Type, family string) string {
	where := fieldPath
	if where == "" {
		where = "(root)"
	}
	return "oasis.DeriveSchema: field " + where + " has unsupported type " + t.String() +
		" (family=" + family + ")"
}
