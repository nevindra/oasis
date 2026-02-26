package postgres

import (
	"strconv"
	"strings"
)

// serializeEmbedding converts []float32 to a string like "[0.1,0.2,0.3]"
// suitable for pgvector's text input format.
func serializeEmbedding(embedding []float32) string {
	parts := make([]string, len(embedding))
	for i, v := range embedding {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// deserializeEmbedding parses pgvector's text format "[0.1,0.2,0.3]" back to []float32.
func deserializeEmbedding(s string) []float32 {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			continue
		}
		out = append(out, float32(v))
	}
	return out
}
