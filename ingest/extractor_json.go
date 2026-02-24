package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Compile-time interface check.
var _ Extractor = (*JSONExtractor)(nil)

// JSONExtractor implements Extractor for JSON documents.
// Recursively walks arbitrary JSON structures producing readable key-value text.
type JSONExtractor struct{}

// NewJSONExtractor creates a JSON extractor.
func NewJSONExtractor() *JSONExtractor { return &JSONExtractor{} }

// maxJSONDepth limits recursion in flatten to prevent stack overflow
// from deeply nested JSON input.
const maxJSONDepth = 100

// Extract converts JSON content to readable key-value text.
func (e *JSONExtractor) Extract(content []byte) (string, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return "", nil
	}
	var data any
	if err := json.Unmarshal(content, &data); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	var lines []string
	flatten("", data, &lines, 0)
	return strings.Join(lines, "\n"), nil
}

func flatten(prefix string, v any, lines *[]string, depth int) {
	if depth >= maxJSONDepth {
		label := prefix
		if label == "" {
			label = "value"
		}
		*lines = append(*lines, fmt.Sprintf("%s: <truncated>", label))
		return
	}
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, child, lines, depth+1)
		}
	case []any:
		if allPrimitive(val) {
			strs := make([]string, len(val))
			for i, item := range val {
				strs[i] = formatJSONValue(item)
			}
			*lines = append(*lines, fmt.Sprintf("%s: %s", prefix, strings.Join(strs, ", ")))
		} else {
			for _, item := range val {
				flatten(prefix, item, lines, depth+1)
			}
		}
	case nil:
		// skip null values
	default:
		label := prefix
		if label == "" {
			label = "value"
		}
		*lines = append(*lines, fmt.Sprintf("%s: %s", label, formatJSONValue(val)))
	}
}

func allPrimitive(arr []any) bool {
	for _, v := range arr {
		switch v.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// formatJSONValue formats a primitive JSON value as a string.
func formatJSONValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", val)
	}
}
