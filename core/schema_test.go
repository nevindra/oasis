package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// schemaJSON marshals a derived schema into a normalized map for comparison.
func schemaJSON[T any](t *testing.T) map[string]any {
	t.Helper()
	raw := DeriveSchema[T]()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("derived schema not valid JSON: %v\n%s", err, raw)
	}
	return got
}

func TestDeriveSchema_Scalars(t *testing.T) {
	tests := []struct {
		name     string
		got      map[string]any
		wantType string
	}{
		{"bool", schemaJSON[bool](t), "boolean"},
		{"int", schemaJSON[int](t), "integer"},
		{"int8", schemaJSON[int8](t), "integer"},
		{"int64", schemaJSON[int64](t), "integer"},
		{"uint", schemaJSON[uint](t), "integer"},
		{"uint8", schemaJSON[uint8](t), "integer"},
		{"float32", schemaJSON[float32](t), "number"},
		{"float64", schemaJSON[float64](t), "number"},
		{"string", schemaJSON[string](t), "string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.got["type"]; got != tc.wantType {
				t.Errorf("type = %v, want %q", got, tc.wantType)
			}
		})
	}
}

func TestDeriveSchema_Slice(t *testing.T) {
	got := schemaJSON[[]string](t)
	if got["type"] != "array" {
		t.Errorf("type = %v, want array", got["type"])
	}
	items, _ := got["items"].(map[string]any)
	if items["type"] != "string" {
		t.Errorf("items.type = %v, want string", items["type"])
	}
}

func TestDeriveSchema_MapStringScalar(t *testing.T) {
	got := schemaJSON[map[string]int](t)
	if got["type"] != "object" {
		t.Errorf("type = %v, want object", got["type"])
	}
	ap, _ := got["additionalProperties"].(map[string]any)
	if ap["type"] != "integer" {
		t.Errorf("additionalProperties.type = %v, want integer", ap["type"])
	}
}

type fooStruct struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Note  *string `json:"note"`
}

func TestDeriveSchema_StructWithPointerOptional(t *testing.T) {
	got := schemaJSON[fooStruct](t)
	if got["type"] != "object" {
		t.Errorf("type = %v, want object", got["type"])
	}
	props, _ := got["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Errorf("missing properties.name")
	}
	if _, ok := props["note"]; !ok {
		t.Errorf("missing properties.note")
	}
	required, _ := got["required"].([]any)
	hasName, hasNote := false, false
	for _, r := range required {
		if r == "name" {
			hasName = true
		}
		if r == "note" {
			hasNote = true
		}
	}
	if !hasName {
		t.Errorf("expected 'name' in required")
	}
	if hasNote {
		t.Errorf("pointer field 'note' should NOT be required")
	}
}

func TestDeriveSchema_TimeTime(t *testing.T) {
	got := schemaJSON[time.Time](t)
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
	if got["format"] != "date-time" {
		t.Errorf("format = %v, want date-time", got["format"])
	}
}

func TestDeriveSchema_ByteSlice(t *testing.T) {
	got := schemaJSON[[]byte](t)
	if got["type"] != "string" {
		t.Errorf("type = %v, want string", got["type"])
	}
}

func TestDeriveSchema_RawMessage(t *testing.T) {
	got := schemaJSON[json.RawMessage](t)
	if len(got) != 0 {
		t.Errorf("expected empty schema {}, got %v", got)
	}
}

type describedStruct struct {
	Limit int `json:"limit" describe:"maximum number of records to return"`
}

func TestDeriveSchema_DescribeTag(t *testing.T) {
	got := schemaJSON[describedStruct](t)
	props, _ := got["properties"].(map[string]any)
	limit, _ := props["limit"].(map[string]any)
	if limit["description"] != "maximum number of records to return" {
		t.Errorf("description = %v", limit["description"])
	}
	if limit["type"] != "integer" {
		t.Errorf("type = %v, want integer", limit["type"])
	}
}

type enumStruct struct {
	Format string `json:"format" enum:"csv,json,jsonl" describe:"data format"`
}

func TestDeriveSchema_EnumTag(t *testing.T) {
	got := schemaJSON[enumStruct](t)
	props, _ := got["properties"].(map[string]any)
	format, _ := props["format"].(map[string]any)
	enum, _ := format["enum"].([]any)
	if len(enum) != 3 || enum[0] != "csv" || enum[1] != "json" || enum[2] != "jsonl" {
		t.Errorf("enum = %v, want [csv json jsonl]", enum)
	}
	if format["description"] != "data format" {
		t.Errorf("description = %v", format["description"])
	}
}

type intEnumStruct struct {
	N int `json:"n" enum:"1,2,3"`
}

func TestDeriveSchema_EnumOnNonString_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on enum tag attached to non-string field")
		}
	}()
	_ = DeriveSchema[intEnumStruct]()
}

type embedBase struct {
	BaseField string `json:"base_field"`
}

type embedOuter struct {
	embedBase
	OuterField int `json:"outer_field"`
}

func TestDeriveSchema_AnonymousEmbeddingFlattens(t *testing.T) {
	got := schemaJSON[embedOuter](t)
	props, _ := got["properties"].(map[string]any)
	if _, ok := props["base_field"]; !ok {
		t.Errorf("embedded field 'base_field' not flattened into outer schema")
	}
	if _, ok := props["outer_field"]; !ok {
		t.Errorf("missing 'outer_field'")
	}
	required, _ := got["required"].([]any)
	if !containsAny(required, "base_field") || !containsAny(required, "outer_field") {
		t.Errorf("both fields should be required, got %v", required)
	}
}

func containsAny(haystack []any, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

type embedConflictA struct {
	Field string `json:"field"`
}
type embedConflictB struct {
	Field string `json:"field"`
}
type embedConflictOuter struct {
	embedConflictA
	embedConflictB
}

func TestDeriveSchema_EmbeddedConflict_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on duplicate JSON name from embedded structs")
		}
	}()
	_ = DeriveSchema[embedConflictOuter]()
}

type recursiveNode struct {
	Name string         `json:"name"`
	Next *recursiveNode `json:"next"`
}

func TestDeriveSchema_Recursive_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected panic on recursive type")
			return
		}
		msg, _ := r.(string)
		if msg == "" {
			t.Errorf("panic value not a string: %T %v", r, r)
		}
	}()
	_ = DeriveSchema[recursiveNode]()
}

type exoticIn struct {
	Mode string          `json:"mode"`
	Args json.RawMessage `json:"args"`
}

func (exoticIn) JSONSchema() json.RawMessage {
	return json.RawMessage(`{"custom":true}`)
}

func TestDeriveSchema_SchemaProviderOverride(t *testing.T) {
	raw := DeriveSchema[exoticIn]()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["custom"] != true {
		t.Errorf("expected schema from JSONSchema(), got %v", got)
	}
	if _, hasType := got["type"]; hasType {
		t.Errorf("expected reflector to be bypassed, got 'type' field: %v", got)
	}
}

type complexField struct {
	C complex64 `json:"c"`
}
type chanField struct {
	Ch chan int `json:"ch"`
}
type funcField struct {
	F func() `json:"f"`
}

func TestDeriveSchema_RejectsComplex(t *testing.T) {
	defer func() {
		r, _ := recover().(string)
		if r == "" || !strings.Contains(r, "complex64") {
			t.Errorf("panic message must name the offending type, got %q", r)
		}
		if !strings.Contains(r, "C") {
			t.Errorf("panic message must name the field, got %q", r)
		}
	}()
	_ = DeriveSchema[complexField]()
}

func TestDeriveSchema_RejectsChan(t *testing.T) {
	defer func() {
		r, _ := recover().(string)
		if r == "" || !strings.Contains(r, "chan") {
			t.Errorf("panic message must mention chan, got %q", r)
		}
	}()
	_ = DeriveSchema[chanField]()
}

func TestDeriveSchema_RejectsFunc(t *testing.T) {
	defer func() {
		r, _ := recover().(string)
		if r == "" || !strings.Contains(r, "func") {
			t.Errorf("panic message must mention func, got %q", r)
		}
	}()
	_ = DeriveSchema[funcField]()
}
