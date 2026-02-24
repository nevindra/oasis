package ingest

import (
	"context"
	"math"
	"sort"
	"strings"
)

// SemanticChunker splits text at semantic boundaries detected by embedding
// similarity drops between consecutive sentences. It uses percentile-based
// breakpoint detection: sentences where cosine similarity to the next sentence
// falls below the Nth percentile become chunk boundaries.
type SemanticChunker struct {
	embed      EmbedFunc
	maxBytes   int
	percentile int
	fallback   *RecursiveChunker
}

var _ Chunker = (*SemanticChunker)(nil)
var _ ContextChunker = (*SemanticChunker)(nil)

// NewSemanticChunker creates a SemanticChunker. The embed function is called
// once per Chunk/ChunkContext call to embed all sentences. Pass provider.Embed
// directly — the signature matches EmbedFunc.
func NewSemanticChunker(embed EmbedFunc, opts ...ChunkerOption) *SemanticChunker {
	cfg := defaultChunkerConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &SemanticChunker{
		embed:      embed,
		maxBytes:   cfg.maxTokens * 4,
		percentile: cfg.breakpointPercentile,
		fallback:   NewRecursiveChunker(opts...),
	}
}

// Chunk implements Chunker. Uses context.Background() for the embedding call.
// Prefer ChunkContext when a context is available.
func (sc *SemanticChunker) Chunk(text string) []string {
	chunks, _ := sc.ChunkContext(context.Background(), text)
	return chunks
}

// ChunkContext splits text at semantic boundaries. It embeds all sentences,
// computes consecutive cosine similarities, and splits where similarity drops
// below the configured percentile threshold.
func (sc *SemanticChunker) ChunkContext(ctx context.Context, text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if len(text) <= sc.maxBytes {
		return []string{text}, nil
	}

	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		return []string{text}, nil
	}

	embeddings, err := sc.embed(ctx, sentences)
	if err != nil {
		// Degrade gracefully: fall back to recursive chunking.
		return sc.fallback.Chunk(text), nil
	}
	if len(embeddings) != len(sentences) {
		return sc.fallback.Chunk(text), nil
	}

	similarities := make([]float32, len(sentences)-1)
	for i := 0; i < len(sentences)-1; i++ {
		similarities[i] = cosineSim(embeddings[i], embeddings[i+1])
	}

	threshold := percentileThreshold(similarities, sc.percentile)

	// Group sentences between breakpoints.
	var groups []string
	var current strings.Builder
	for i, s := range sentences {
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(s)

		// Split after this sentence if similarity to next is below threshold.
		if i < len(similarities) && similarities[i] < threshold {
			groups = append(groups, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		groups = append(groups, strings.TrimSpace(current.String()))
	}

	return sc.mergeAndSplit(groups), nil
}

// mergeAndSplit merges small groups up to maxBytes and splits oversized ones.
func (sc *SemanticChunker) mergeAndSplit(groups []string) []string {
	var chunks []string
	var current strings.Builder

	for _, g := range groups {
		if len(g) > sc.maxBytes {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, sc.fallback.Chunk(g)...)
			continue
		}

		needed := len(g)
		if current.Len() > 0 {
			needed = current.Len() + 1 + len(g)
		}

		if needed <= sc.maxBytes {
			if current.Len() > 0 {
				current.WriteByte(' ')
			}
			current.WriteString(g)
		} else {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			current.WriteString(g)
		}
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// splitSentences splits text into sentences using the same boundary detection
// as RecursiveChunker. Falls back to splitting on ". " if no boundaries found.
func splitSentences(text string) []string {
	boundaries := findSentenceBoundaries(text)
	if len(boundaries) == 0 {
		// Fallback: naive split on period-space.
		parts := strings.Split(text, ". ")
		var out []string
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Re-append the period consumed by Split for all non-last parts,
			// and for the last part if the original text ended with ". ".
			if i < len(parts)-1 || strings.HasSuffix(text, ". ") {
				p += "."
			}
			out = append(out, p)
		}
		if len(out) == 0 {
			return []string{text}
		}
		return out
	}

	var sentences []string
	start := 0
	for _, b := range boundaries {
		s := strings.TrimSpace(text[start:b])
		if s != "" {
			sentences = append(sentences, s)
		}
		start = b
	}
	if start < len(text) {
		s := strings.TrimSpace(text[start:])
		if s != "" {
			sentences = append(sentences, s)
		}
	}
	return sentences
}

// cosineSim computes cosine similarity between two vectors.
func cosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// percentileThreshold computes the Nth percentile of a float32 slice.
func percentileThreshold(values []float32, percentile int) float32 {
	if len(values) == 0 {
		return 0
	}
	percentile = max(0, min(percentile, 100))
	sorted := make([]float32, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := float64(percentile) / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper || upper >= len(sorted) {
		return sorted[lower]
	}
	frac := float32(idx - float64(lower))
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
