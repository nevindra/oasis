package core

import (
	"encoding/json"
	"testing"
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
