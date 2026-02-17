package search

import (
	"math"
	"strings"
	"testing"
)

func TestCosineSimilarityIdentical(t *testing.T) {
	a := []float32{1, 2, 3}
	sim := cosineSimilarity(a, a)
	if math.Abs(float64(sim)-1.0) > 0.001 {
		t.Errorf("expected ~1.0, got %f", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim := cosineSimilarity(a, b)
	if math.Abs(float64(sim)) > 0.001 {
		t.Errorf("expected ~0, got %f", sim)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	sim := cosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("expected 0, got %f", sim)
	}
}

func TestFormatRankedResults(t *testing.T) {
	ranked := []rankedChunk{
		{Text: "first result", SourceIndex: 0, SourceTitle: "Title A", Score: 0.95},
		{Text: "second result", SourceIndex: 1, SourceTitle: "Title B", Score: 0.80},
	}
	results := []resultWithContent{
		{Result: braveResult{Title: "Title A", URL: "https://a.com"}},
		{Result: braveResult{Title: "Title B", URL: "https://b.com"}},
	}

	out := formatRankedResults(ranked, results)
	if !strings.Contains(out, "first result") {
		t.Error("missing first result")
	}
	if !strings.Contains(out, "https://a.com") {
		t.Error("missing source URL")
	}
	if !strings.Contains(out, "Sources:") {
		t.Error("missing sources section")
	}
}

func TestDefinitions(t *testing.T) {
	tool := &Tool{
		braveAPIKey: "test-key",
	}
	defs := tool.Definitions()
	if len(defs) != 1 || defs[0].Name != "web_search" {
		t.Error("wrong definitions")
	}
}
