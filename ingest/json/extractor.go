// Package json provides a JSON text extractor for the ingest pipeline.
// Recursively walks arbitrary JSON structures producing readable key-value text.
package json

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/ingest"
)

// TypeJSON is the content type for JSON documents.
const TypeJSON = ingest.TypeJSON

// Extractor implements ingest.Extractor for JSON documents.
type Extractor struct{}

// NewExtractor creates a JSON extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract converts JSON content to readable key-value text.
// Objects produce "key: value" lines with dotted paths for nesting.
// Arrays of primitives are comma-joined. Arrays of objects are iterated.
func (e *Extractor) Extract(content []byte) (string, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return "", nil
	}

	var data any
	if err := json.Unmarshal(content, &data); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}

	var lines []string
	flatten("", data, &lines)
	return strings.Join(lines, "\n"), nil
}

func flatten(prefix string, v any, lines *[]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, child, lines)
		}
	case []any:
		if allPrimitive(val) {
			strs := make([]string, len(val))
			for i, item := range val {
				strs[i] = formatValue(item)
			}
			*lines = append(*lines, fmt.Sprintf("%s: %s", prefix, strings.Join(strs, ", ")))
		} else {
			for _, item := range val {
				flatten(prefix, item, lines)
			}
		}
	case nil:
		// skip null values
	default:
		label := prefix
		if label == "" {
			label = "value"
		}
		*lines = append(*lines, fmt.Sprintf("%s: %s", label, formatValue(val)))
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

func formatValue(v any) string {
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
