package ingest

import (
	"context"
	"testing"
)

// mockEmbed returns distinct embeddings for each sentence so we can control similarity.
func mockEmbed(vectors map[string][]float32) EmbedFunc {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			if v, ok := vectors[t]; ok {
				out[i] = v
			} else {
				// Default: zero vector
				out[i] = make([]float32, 3)
			}
		}
		return out, nil
	}
}

func TestSemanticChunker_BasicSplit(t *testing.T) {
	// Two groups of similar sentences with a topic shift in between.
	// Sentences 1-2 are similar (high cosine sim), sentence 3 is different.
	embed := mockEmbed(map[string][]float32{
		"The cat sat on the mat.":     {1, 0, 0},
		"The cat lay on the rug.":     {0.95, 0.05, 0},
		"Quantum physics is complex.": {0, 0, 1},
		"Quantum mechanics is hard.":  {0, 0.05, 0.95},
	})

	// maxTokens=20 → maxChars=80, which is less than the ~103-char test text,
	// forcing it through the semantic splitting path.
	sc := NewSemanticChunker(embed, WithMaxTokens(20), WithBreakpointPercentile(50))
	chunks, err := sc.ChunkContext(context.Background(),
		"The cat sat on the mat. The cat lay on the rug. Quantum physics is complex. Quantum mechanics is hard.")
	if err != nil {
		t.Fatalf("ChunkContext() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks (topic split), got %d: %v", len(chunks), chunks)
	}
}

func TestSemanticChunker_SingleSentence(t *testing.T) {
	embed := mockEmbed(nil)
	sc := NewSemanticChunker(embed)
	chunks, err := sc.ChunkContext(context.Background(), "Just one sentence.")
	if err != nil {
		t.Fatalf("ChunkContext() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestSemanticChunker_EmptyText(t *testing.T) {
	embed := mockEmbed(nil)
	sc := NewSemanticChunker(embed)
	chunks, err := sc.ChunkContext(context.Background(), "")
	if err != nil {
		t.Fatalf("ChunkContext() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestSemanticChunker_ShortText(t *testing.T) {
	embed := mockEmbed(nil)
	sc := NewSemanticChunker(embed, WithMaxTokens(1000))
	chunks, err := sc.ChunkContext(context.Background(), "Short text fits in one chunk.")
	if err != nil {
		t.Fatalf("ChunkContext() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestSemanticChunker_ImplementsChunker(t *testing.T) {
	embed := mockEmbed(nil)
	sc := NewSemanticChunker(embed)

	// Verify Chunker interface
	var _ Chunker = sc

	// Verify ContextChunker interface
	var _ ContextChunker = sc

	// Chunk() should work (uses context.Background internally)
	chunks := sc.Chunk("A sentence. Another sentence.")
	if len(chunks) == 0 {
		t.Error("Chunk() returned no chunks")
	}
}
